package puredb

import "fmt"

// ReadNextDpg reads the next data page for a table into the page buffer.
// Returns the physical page number, or 0 if no more pages.
func (mdb *MdbHandle) ReadNextDpg(table *MdbTableDef) error {
	entry := table.Entry

	// Try fast path using usage map
	nextPg := mapFindNext(mdb, table.UsageMap, table.MapSz, int(table.CurPhysPg))
	if nextPg < 0 {
		// Unknown map type — fall through to brute force
		nextPg = 0
	}
	if nextPg == 0 {
		return fmt.Errorf("puredb: no more data pages (EOF)")
	}
	if nextPg == int(table.CurPhysPg) {
		return fmt.Errorf("puredb: infinite loop detected in page traversal")
	}

	if err := mdb.readPage(uint32(nextPg)); err != nil {
		return fmt.Errorf("puredb: error reading page %d: %w", nextPg, err)
	}

	table.CurPhysPg = uint32(nextPg)

	// Verify this page belongs to our table
	if mdb.pgBuf[0] == PageData && GetInt32(mdb.pgBuf[:], 4) == int(entry.TablePg) {
		return nil
	}

	// Page doesn't match — fall back to brute force scan
	for {
		table.CurPhysPg++
		if err := mdb.readPage(table.CurPhysPg); err != nil {
			return err
		}
		if mdb.pgBuf[0] == PageData && GetInt32(mdb.pgBuf[:], 4) == int(entry.TablePg) {
			return nil
		}
	}
}

// mapFindNext finds the next allocated page from a usage map.
// Returns the page number, 0 if none found, -1 on error.
func mapFindNext(mdb *MdbHandle, usageMap []byte, mapSz int, startPg int) int {
	if mapSz < 5 {
		return 0
	}

	mapType := usageMap[0]
	if mapType == 0 {
		return mapFindNext0(usageMap, mapSz, startPg)
	} else if mapType == 1 {
		return mapFindNext1(mdb, usageMap, mapSz, startPg)
	}

	return -1
}

// mapFindNext0 handles type-0 usage maps (inline bitmaps).
func mapFindNext0(usageMap []byte, mapSz int, startPg int) int {
	pgnum := GetInt32(usageMap, 1)
	usageBitmap := usageMap[5:]
	usageBitLen := (mapSz - 5) * 8

	i := 0
	if startPg >= pgnum {
		i = startPg - pgnum + 1
	}
	for ; i < usageBitLen; i++ {
		if usageBitmap[i/8]&(1<<(i%8)) != 0 {
			return pgnum + i
		}
	}
	return 0
}

// mapFindNext1 handles type-1 usage maps (extended bitmaps).
func mapFindNext1(mdb *MdbHandle, usageMap []byte, mapSz int, startPg int) int {
	usageBitLen := (mdb.fmt.PgSize - 4) * 8
	maxMapPgs := (mapSz - 1) / 4

	mapInd := (startPg + 1) / usageBitLen
	offset := (startPg + 1) % usageBitLen

	for ; mapInd < maxMapPgs; mapInd++ {
		mapPg := GetInt32(usageMap, mapInd*4+1)
		if mapPg == 0 {
			continue
		}
		if err := mdb.readAltPage(uint32(mapPg)); err != nil {
			return -1
		}
		usageBitmap := mdb.altPgBuf[4:]

		for i := offset; i < usageBitLen; i++ {
			if usageBitmap[i/8]&(1<<(i%8)) != 0 {
				return mapInd*usageBitLen + i
			}
		}
		offset = 0
	}
	return 0
}
