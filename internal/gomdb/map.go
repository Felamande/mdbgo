package gomdb

import (
	"errors"
)

// errNoMorePages is a sentinel returned when no data pages exist for a table.
var errNoMorePages = errors.New("no more data pages")

// ReadNextDpg reads the next data page for a table into the page buffer.
// Returns the physical page number, or 0 if no more pages.
func (mdb *MdbHandle) ReadNextDpg(table *MdbTableDef) error {
	entry := table.Entry

	// Use the allocation map until it reaches a normal EOF. A negative result,
	// an unreadable candidate, or a page that belongs to another table marks a
	// broken/incomplete map and enables the compatibility fallback below.
	mapBroken := false
	for {
		nextPg := mapFindNext(mdb, table.UsageMap, table.MapSz, int(table.CurPhysPg))
		if nextPg == 0 || nextPg == int(table.CurPhysPg) {
			if !mapBroken {
				// A non-empty table with no first map hit has an incomplete
				// usage map (seen in a few damaged/legacy files). Recover once
				// with the compatibility scan, but never rescan after a normal
				// page-by-page EOF.
				if nextPg == 0 && table.CurPhysPg == 0 && table.NumRows > 0 {
					mapBroken = true
					break
				}
				return errNoMorePages
			}
			break
		}
		if nextPg < 0 {
			mapBroken = true
			break
		}
		if err := mdb.readPage(uint32(nextPg)); err != nil {
			mapBroken = true
			break
		}
		table.CurPhysPg = uint32(nextPg)
		if mdb.pgBuf[0] == PageData && GetInt32(mdb.pgBuf[:], 4) == int(entry.TablePg) {
			return nil
		}
		mapBroken = true
	}

	// Fall back to brute force only for a map that proved unusable. Scanning
	// after a normal map EOF turns every completed query into a scan to file EOF.
	for {
		table.CurPhysPg++
		if err := mdb.readPage(table.CurPhysPg); err != nil {
			return errNoMorePages
		}
		if mdb.pgBuf[0] == PageData && GetInt32(mdb.pgBuf[:], 4) == int(entry.TablePg) {
			return nil
		}
	}
}

// mapFindNext finds the next allocated page from a usage map.
// Returns the page number, 0 if none found, -1 on error.
func mapFindNext(mdb *MdbHandle, usageMap []byte, mapSz int, startPg int) int {
	if mapSz < 1 || mapSz > len(usageMap) {
		return -1
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
	if mapSz < 5 || mapSz > len(usageMap) {
		return -1
	}
	pgnum := GetInt32(usageMap, 1)
	usageBitmap := usageMap[5:mapSz]
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
	if mapSz < 5 || mapSz > len(usageMap) {
		return -1
	}
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
