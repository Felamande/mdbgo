package gomdb

import (
	"fmt"
)

// ReadCatalog reads the MSysObjects table and populates the catalog.
// If objType is ObjAny, all object types are included.
func (mdb *MdbHandle) ReadCatalog(objType int) error {
	if mdb.Catalog != nil && len(mdb.Catalog) > 0 {
		// Already read
		return nil
	}

	// Create a dummy catalog entry for MSysObjects (page 2)
	dummyEntry := &CatalogEntry{
		Mdb:        mdb,
		ObjectName: "MSysObjects",
		ObjectType: ObjTable,
		TablePg:    2,
	}

	table, err := mdb.ReadTable(dummyEntry)
	if err != nil {
		return fmt.Errorf("gomdb: unable to read MSysObjects table: %w", err)
	}
	defer mdb.FreeTableDef(table)

	if err := mdb.ReadColumns(table); err != nil {
		return fmt.Errorf("gomdb: unable to read MSysObjects columns: %w", err)
	}

	findCol := func(name string) int {
		for i, col := range table.Columns {
			if equalFold(col.Name, name) {
				return i
			}
		}
		return -1
	}
	idIdx := findCol("Id")
	nameIdx := findCol("Name")
	typeIdx := findCol("Type")
	flagsIdx := findCol("Flags")
	lvPropIdx := findCol("LvProp")
	if idIdx < 0 || nameIdx < 0 || typeIdx < 0 || flagsIdx < 0 || lvPropIdx < 0 {
		return fmt.Errorf("gomdb: unable to find all required MSysObjects columns")
	}

	idCol := table.Columns[idIdx]
	nameCol := table.Columns[nameIdx]
	typeCol := table.Columns[typeIdx]
	flagsCol := table.Columns[flagsIdx]
	lvPropCol := table.Columns[lvPropIdx]

	mdb.RewindTable(table)

	for {
		hasRow, err := mdb.FetchRow(table)
		if err != nil {
			return fmt.Errorf("gomdb: error reading MSysObjects: %w", err)
		}
		if !hasRow {
			break
		}

		typ, _ := mdb.Int64Value(typeCol)
		flags, _ := mdb.Int64Value(flagsCol)
		objName := mdb.columnValueToString(nameCol)

		if objType == ObjAny || int(typ) == objType {
			entry := &CatalogEntry{
				Mdb:        mdb,
				ObjectName: objName,
				ObjectType: int(typ) & 0x7F,
				Flags:      int(flags),
			}

			// Parse table page from Id (low 24 bits)
			if id, ok := mdb.Int64Value(idCol); ok {
				entry.TablePg = uint32(id & 0x00FFFFFF)
			}

			// Read LvProp (KKD data) if present
			if !lvPropCol.CurValueIsNull {
				kkd, kkdLen, err := mdb.OleReadFull(lvPropCol, nil)
				if err == nil && kkdLen > 0 {
					entry.Props = kkdToProps(mdb, kkd, kkdLen)
				}
			}

			mdb.numCatalog++
			mdb.Catalog = append(mdb.Catalog, entry)
		}
	}

	return nil
}

// GetCatalogEntryByName finds a catalog entry by name (case-insensitive).
func (mdb *MdbHandle) GetCatalogEntryByName(name string) *CatalogEntry {
	for _, entry := range mdb.Catalog {
		if equalFold(entry.ObjectName, name) {
			return entry
		}
	}
	return nil
}

// bindColumnByName binds a column by name and returns its zero-based index.
func (mdb *MdbHandle) bindColumnByName(table *MdbTableDef, name string) int {
	for i, col := range table.Columns {
		if equalFold(col.Name, name) {
			col.BindPtr = make([]byte, colBindSize(col))
			return i
		}
	}
	return -1
}

// GetBoundValue returns the bound string value for a column index.
func (mdb *MdbHandle) GetBoundValue(table *MdbTableDef, idx int) string {
	col := table.Columns[idx]
	if col.BindPtr == nil {
		return ""
	}
	if col.BindLen > 0 && col.BindLen <= len(col.BindPtr) {
		value := col.BindPtr[:col.BindLen]
		return string(value[:clen(value)])
	}
	return string(col.BindPtr[:clen(col.BindPtr)])
}

// clen returns the length of a byte slice up to the first null byte.
func clen(b []byte) int {
	for i, v := range b {
		if v == 0 {
			return i
		}
	}
	return len(b)
}
