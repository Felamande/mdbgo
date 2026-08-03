package gomdb

import "fmt"

// decodeScratch holds per-row buffers that are reused by row cracking,
// sarg evaluation, and value formatting. A fast scan gives every worker its
// own scratch so pages can be decoded in parallel without touching the
// handle's shared buffers.
type decodeScratch struct {
	fields     []MdbField
	varOffsets []int
	unicode    []byte
	layouts    []crackLayout
}

// crackLayout is the per-column data needed to crack rows, packed into a
// compact struct so the hot loop avoids chasing column pointers.
type crackLayout struct {
	isFixed     bool
	nullByte    uint8
	nullBit     uint8
	fixedOffset int32
	colSize     int32
	varNum      int32
}

func buildCrackLayouts(table *MdbTableDef) []crackLayout {
	layouts := make([]crackLayout, len(table.Columns))
	for i, col := range table.Columns {
		layouts[i] = crackLayout{
			isFixed:     col.IsFixed,
			nullByte:    uint8(col.NullByte),
			nullBit:     col.NullBit,
			fixedOffset: int32(col.FixedOffset),
			colSize:     int32(col.ColSize),
			varNum:      int32(col.VarColNum),
		}
	}
	return layouts
}

// CrackRow parses a raw row into individual fields.
// This is a direct port of mdb_crack_row from mdbtools write.c.
func (mdb *MdbHandle) CrackRow(table *MdbTableDef, rowStart, rowSize int) ([]MdbField, error) {
	if len(table.crackLayouts) != table.NumCols {
		table.crackLayouts = buildCrackLayouts(table)
	}
	s := &decodeScratch{fields: table.fieldsBuf, varOffsets: table.varOffsetsBuf}
	s.layouts = table.crackLayouts
	fields, err := crackRowInto(mdb, table, mdb.pgBuf, rowStart, rowSize, s, true)
	table.fieldsBuf = s.fields
	table.varOffsetsBuf = s.varOffsets
	return fields, err
}

// crackRowInto parses a row from an explicit page buffer using caller-owned
// scratch. This is the shared implementation used by the synchronous path
// (page = mdb.pgBuf, scratch = table buffers) and by fast-scan workers.
// When needValues is false (fast scans without sargs), per-field Value slices
// are skipped; only Start/Siz/IsNull are populated.
func crackRowInto(mdb *MdbHandle, table *MdbTableDef, page []byte, rowStart, rowSize int, s *decodeScratch, needValues bool) ([]MdbField, error) {
	rowEnd := rowStart + rowSize - 1

	// Read row column count
	var rowCols, colCountSize int
	if mdb.IsJet3() {
		rowCols = int(page[rowStart])
		colCountSize = 1
	} else {
		rowCols = GetInt16(page, rowStart)
		colCountSize = 2
	}

	bitmaskSz := (rowCols + 7) / 8
	if bitmaskSz >= rowEnd-rowStart {
		return nil, fmt.Errorf("gomdb: invalid page buffer in crack_row")
	}

	// Null mask is at the end of the row
	nullMask := page[rowEnd-bitmaskSz+1:]

	// Read variable column offsets table (from end of row for Jet4)
	var rowVarCols int
	var varColOffsets []int
	if table.NumVarCols > 0 {
		if mdb.IsJet3() {
			rowVarCols = int(page[rowEnd-bitmaskSz])
		} else {
			rowVarCols = GetInt16(page, rowEnd-bitmaskSz-1)
		}

		need := rowVarCols + 1
		if cap(s.varOffsets) < need {
			s.varOffsets = make([]int, need)
		}
		varColOffsets = s.varOffsets[:need]

		if mdb.IsJet3() {
			crackRow3OffsetsIn(mdb, page, rowStart, rowEnd, bitmaskSz, rowVarCols, varColOffsets)
		} else {
			crackRow4OffsetsIn(page, rowEnd, bitmaskSz, rowVarCols, varColOffsets)
		}
	}

	rowFixedCols := rowCols - rowVarCols
	if rowFixedCols < 0 {
		rowFixedCols = 0
	}
	fixedColsFound := 0

	// Reuse pre-allocated field buffer, or allocate once
	if cap(s.fields) < table.NumCols {
		s.fields = make([]MdbField, table.NumCols)
	}
	fields := s.fields[:table.NumCols]

	layouts := s.layouts
	if len(layouts) != table.NumCols {
		layouts = nil
	}
	for i := 0; i < table.NumCols; i++ {
		var lp *crackLayout
		if layouts != nil {
			lp = &layouts[i]
		}
		f := &fields[i]
		if needValues {
			f.Value = nil
			f.ColNum = i
			if lp != nil {
				f.IsFixed = lp.isFixed
			} else {
				f.IsFixed = table.Columns[i].IsFixed
			}
		}

		// Null bit check — uses col->col_num, NOT row_col_num. The mask
		// offset is precomputed at column-definition time.
		var byteNum int
		var bitNum byte
		var isFixed bool
		var varNum int
		var colStart, colSize int
		if lp != nil {
			byteNum = int(lp.nullByte)
			bitNum = lp.nullBit
			isFixed = lp.isFixed
			varNum = int(lp.varNum)
			colStart = int(lp.fixedOffset)
			colSize = int(lp.colSize)
		} else {
			col := table.Columns[i]
			byteNum, bitNum = col.NullByte, byte(col.NullBit)
			if !col.NullReady {
				byteNum = col.ColNum / 8
				bitNum = byte(1 << (col.ColNum % 8))
			}
			isFixed = col.IsFixed
			varNum = col.VarColNum
			colStart = col.FixedOffset
			colSize = col.ColSize
		}

		if byteNum < len(nullMask) {
			f.IsNull = nullMask[byteNum]&bitNum == 0
		} else {
			f.IsNull = true
		}

		if isFixed && fixedColsFound < rowFixedCols {
			colStart += colCountSize
			f.Start = rowStart + colStart
			f.Siz = colSize
			if needValues && f.Siz > 0 && rowStart+colStart+f.Siz <= len(page) {
				f.Value = page[rowStart+colStart : rowStart+colStart+f.Siz]
			}
			fixedColsFound++
		} else if !isFixed && varNum < len(varColOffsets)-1 {
			colStart := varColOffsets[varNum]
			f.Start = rowStart + colStart
			f.Siz = varColOffsets[varNum+1] - colStart
			if needValues && f.Siz > 0 && rowStart+colStart+f.Siz <= len(page) {
				f.Value = page[rowStart+colStart : rowStart+colStart+f.Siz]
			}
		} else {
			f.Start = 0
			f.Value = nil
			f.Siz = 0
			f.IsNull = true
		}

		// Validate field bounds
		if f.Start+f.Siz > rowStart+rowSize {
			f.Start = rowStart
			f.Siz = 0
		}
	}

	return fields, nil
}

// crackRow4Offsets reads variable column offsets for Jet4 rows.
func crackRow4OffsetsIn(page []byte, rowEnd, bitmaskSz, rowVarCols int, offsets []int) {
	for i := 0; i < rowVarCols+1; i++ {
		offsets[i] = GetInt16(page, rowEnd-bitmaskSz-3-(i*2))
	}
}

// crackRow3Offsets reads variable column offsets for Jet3 rows.
func crackRow3OffsetsIn(mdb *MdbHandle, page []byte, rowStart, rowEnd, bitmaskSz, rowVarCols int, offsets []int) {
	rowLen := rowEnd - rowStart + 1
	numJumps := (rowLen - 1) / 256
	colPtr := rowEnd - bitmaskSz - numJumps - 1

	// If last jump is a dummy, ignore it
	if (colPtr-rowStart-rowVarCols)/256 < numJumps {
		numJumps--
	}

	if bitmaskSz+numJumps+1 > rowEnd {
		return
	}

	if colPtr >= mdb.Fmt().PgSize || colPtr < rowVarCols {
		return
	}

	jumpsUsed := 0
	for i := 0; i < rowVarCols+1; i++ {
		for jumpsUsed < numJumps && i == int(page[rowEnd-bitmaskSz-jumpsUsed-1]) {
			jumpsUsed++
		}
		offsets[i] = int(page[colPtr-i]) + (jumpsUsed * 256)
	}
}
