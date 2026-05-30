package gomdb

import "fmt"

// ReadIndices reads the index definitions for a table.
func (mdb *MdbHandle) ReadIndices(table *MdbTableDef) error {
	mfmt := mdb.fmt

	table.Indices = make([]*MdbIndex, 0, table.NumIdxs)

	// Calculate starting positions
	var idx2Sz, typeOffset int
	if mdb.IsJet3() {
		mdb.curPos = table.IndexStart + 39*table.NumRealIdxs
		idx2Sz = 20
		typeOffset = 19
	} else {
		mdb.curPos = table.IndexStart + 52*table.NumRealIdxs
		idx2Sz = 28
		typeOffset = 23
	}

	// Read index definitions (idx2 entries)
	idxBuf := make([]byte, idx2Sz)
	numIdxsNotType2 := 0
	for i := 0; i < table.NumIdxs; i++ {
		if n := mdb.ReadPgIfN(idxBuf, idx2Sz); n < idx2Sz {
			return fmt.Errorf("gomdb: unable to read index %d definition", i)
		}
		idx := &MdbIndex{
			Table:     table,
			IndexNum:  GetInt16(idxBuf, 4),
			IndexType: int(idxBuf[typeOffset]),
		}
		table.Indices = append(table.Indices, idx)
		if idx.IndexType != 2 {
			numIdxsNotType2++
		}
	}
	if numIdxsNotType2 < table.NumRealIdxs {
		table.NumRealIdxs = numIdxsNotType2
	}

	// Read index names
	for i := 0; i < table.NumIdxs; i++ {
		idx := table.Indices[i]
		var nameSz int
		if mdb.IsJet3() {
			nameSz = int(mdb.ReadPgIf8())
		} else {
			nameSz = int(mdb.ReadPgIf16())
		}
		tmpBuf := make([]byte, nameSz)
		if n := mdb.ReadPgIfN(tmpBuf, nameSz); n < nameSz {
			return fmt.Errorf("gomdb: unable to read index %d name", i)
		}
		idx.Name = UnicodeToUTF8(tmpBuf, mdb.IsJet4())
	}

	// Save position and read alternate page for column definitions
	mdb.readAltPage(table.Entry.TablePg)
	indexStartPg := mdb.curPg // remember which page we were on
	mdb.readPage(table.Entry.TablePg)
	mdb.curPos = table.IndexStart

	for i := 0; i < table.NumRealIdxs; i++ {
		if !mdb.IsJet3() {
			mdb.curPos += 4 // skip unknown marker
		}

		// Find matching index
		var idx *MdbIndex
		for j := 0; j < table.NumIdxs; j++ {
			idx = table.Indices[j]
			if idx.IndexType != 2 && idx.IndexNum == i {
				break
			}
			idx = nil
		}
		if idx == nil {
			continue
		}

		idx.NumRows = GetInt32(mdb.altPgBuf[:],
			mfmt.TabColsStartOffset+idx.IndexNum*mfmt.TabRidxEntrySize)

		// Read key columns
		keyNum := 0
		for j := 0; j < MaxIdxCols; j++ {
			colNum := int(mdb.ReadPgIf16())
			if colNum == 0xFFFF {
				mdb.curPos++
				continue
			}
			// Map internal column number to column index
			cleanedColNum := -1
			for k := 0; k < table.NumCols; k++ {
				if table.Columns[k].ColNum == colNum {
					cleanedColNum = k
					break
				}
			}
			if cleanedColNum == -1 {
				mdb.curPos++
				continue
			}
			idx.KeyColNum[keyNum] = cleanedColNum + 1 // 1-based
			order := mdb.ReadPgIf8()
			if order != 0 {
				idx.KeyColOrder[keyNum] = Asc
			} else {
				idx.KeyColOrder[keyNum] = Desc
			}
			keyNum++
		}
		idx.NumKeys = keyNum

		// Skip usage map info
		mdb.curPos += 4
		idx.FirstPg = mdb.ReadPgIf32()

		if !mdb.IsJet3() {
			mdb.curPos += 4
		}
		idx.Flags = int(mdb.ReadPgIf8())
		if !mdb.IsJet3() {
			mdb.curPos += 5
		}
	}

	// Restore original page
	mdb.readPage(indexStartPg)
	return nil
}
