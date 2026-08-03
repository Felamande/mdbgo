package gomdb

import "fmt"

// CrackRow parses a raw row into individual fields.
// This is a direct port of mdb_crack_row from mdbtools write.c.
func (mdb *MdbHandle) CrackRow(table *MdbTableDef, rowStart, rowSize int) ([]MdbField, error) {
	rowEnd := rowStart + rowSize - 1

	// Read row column count
	var rowCols, colCountSize int
	if mdb.IsJet3() {
		rowCols = int(mdb.pgBuf[rowStart])
		colCountSize = 1
	} else {
		rowCols = GetInt16(mdb.pgBuf[:], rowStart)
		colCountSize = 2
	}

	bitmaskSz := (rowCols + 7) / 8
	if bitmaskSz >= rowEnd-rowStart {
		return nil, fmt.Errorf("gomdb: invalid page buffer in crack_row")
	}

	// Null mask is at the end of the row
	nullMask := mdb.pgBuf[rowEnd-bitmaskSz+1:]

	// Read variable column offsets table (from end of row for Jet4)
	var rowVarCols int
	var varColOffsets []int
	if table.NumVarCols > 0 {
		if mdb.IsJet3() {
			rowVarCols = int(mdb.pgBuf[rowEnd-bitmaskSz])
		} else {
			rowVarCols = GetInt16(mdb.pgBuf[:], rowEnd-bitmaskSz-1)
		}

		need := rowVarCols + 1
		if cap(table.varOffsetsBuf) < need {
			table.varOffsetsBuf = make([]int, need)
		}
		varColOffsets = table.varOffsetsBuf[:need]
		clear(varColOffsets)

		if mdb.IsJet3() {
			crackRow3Offsets(mdb, rowStart, rowEnd, bitmaskSz, rowVarCols, varColOffsets)
		} else {
			crackRow4Offsets(mdb, rowEnd, bitmaskSz, rowVarCols, varColOffsets)
		}
	}

	rowFixedCols := rowCols - rowVarCols
	if rowFixedCols < 0 {
		rowFixedCols = 0
	}
	fixedColsFound := 0

	// Reuse pre-allocated field buffer, or allocate once
	if cap(table.fieldsBuf) < table.NumCols {
		table.fieldsBuf = make([]MdbField, table.NumCols)
	}
	fields := table.fieldsBuf[:table.NumCols]

	for i := 0; i < table.NumCols; i++ {
		col := table.Columns[i]
		f := &fields[i]
		f.Value = nil
		f.ColNum = i
		f.IsFixed = col.IsFixed

		// Null bit check — uses col->col_num, NOT row_col_num
		byteNum := col.ColNum / 8
		bitNum := col.ColNum % 8

		if byteNum < len(nullMask) {
			if nullMask[byteNum]&(1<<bitNum) != 0 {
				f.IsNull = false
			} else {
				f.IsNull = true
			}
		} else {
			f.IsNull = true
		}

		if col.IsFixed && fixedColsFound < rowFixedCols {
			colStart := col.FixedOffset + colCountSize
			f.Start = rowStart + colStart
			f.Siz = col.ColSize
			if f.Siz > 0 && rowStart+colStart+f.Siz <= len(mdb.pgBuf) {
				f.Value = mdb.pgBuf[rowStart+colStart : rowStart+colStart+f.Siz]
			}
			fixedColsFound++
		} else if !col.IsFixed && col.VarColNum < len(varColOffsets)-1 {
			colStart := varColOffsets[col.VarColNum]
			f.Start = rowStart + colStart
			f.Siz = varColOffsets[col.VarColNum+1] - colStart
			if f.Siz > 0 && rowStart+colStart+f.Siz <= len(mdb.pgBuf) {
				f.Value = mdb.pgBuf[rowStart+colStart : rowStart+colStart+f.Siz]
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
func crackRow4Offsets(mdb *MdbHandle, rowEnd, bitmaskSz, rowVarCols int, offsets []int) {
	for i := 0; i < rowVarCols+1; i++ {
		offsets[i] = GetInt16(mdb.pgBuf[:], rowEnd-bitmaskSz-3-(i*2))
	}
}

// crackRow3Offsets reads variable column offsets for Jet3 rows.
func crackRow3Offsets(mdb *MdbHandle, rowStart, rowEnd, bitmaskSz, rowVarCols int, offsets []int) {
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
		for jumpsUsed < numJumps && i == int(mdb.pgBuf[rowEnd-bitmaskSz-jumpsUsed-1]) {
			jumpsUsed++
		}
		offsets[i] = int(mdb.pgBuf[colPtr-i]) + (jumpsUsed * 256)
	}
}

