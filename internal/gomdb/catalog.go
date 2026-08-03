package gomdb

import (
	"fmt"
	"os"
	"sync"
)

// cachedCatalogEntry is a parsed MSysObjects row without handle-specific
// back-references, so the result can be shared across connections.
type cachedCatalogEntry struct {
	name    string
	rawType int
	tablePg uint32
	flags   int
	props   []*Properties
}

type catalogFileCache struct {
	size    int64
	mtime   int64
	entries []cachedCatalogEntry
}

var (
	catalogCacheMu sync.Mutex
	catalogCache   = make(map[string]*catalogFileCache)
)

const maxCatalogCacheEntries = 64

// ReadCatalog reads the MSysObjects table and populates the catalog.
// If objType is ObjAny, all object types are included.
func (mdb *MdbHandle) ReadCatalog(objType int) error {
	if mdb.Catalog != nil && len(mdb.Catalog) > 0 {
		// Already read
		return nil
	}

	entries, err := mdb.catalogEntries()
	if err != nil {
		return err
	}
	for i := range entries {
		e := &entries[i]
		if objType == ObjAny || e.rawType == objType {
			mdb.Catalog = append(mdb.Catalog, &CatalogEntry{
				Mdb:        mdb,
				ObjectName: e.name,
				ObjectType: e.rawType & 0x7F,
				TablePg:    e.tablePg,
				Flags:      e.flags,
				Props:      e.props,
			})
		}
	}
	mdb.numCatalog = len(mdb.Catalog)
	return nil
}

// catalogEntries returns the parsed MSysObjects rows, reusing a per-file cache
// when the file is unchanged. The returned slice must not be modified.
func (mdb *MdbHandle) catalogEntries() ([]cachedCatalogEntry, error) {
	if path, size, mtime, ok := mdb.catalogFileInfo(); ok {
		catalogCacheMu.Lock()
		if e, hit := catalogCache[path]; hit && e.size == size && e.mtime == mtime {
			entries := e.entries
			catalogCacheMu.Unlock()
			return entries, nil
		}
		catalogCacheMu.Unlock()

		entries, err := mdb.buildCatalogEntries()
		if err != nil {
			return nil, err
		}
		catalogCacheMu.Lock()
		if len(catalogCache) >= maxCatalogCacheEntries {
			clear(catalogCache)
		}
		catalogCache[path] = &catalogFileCache{size: size, mtime: mtime, entries: entries}
		catalogCacheMu.Unlock()
		return entries, nil
	}
	return mdb.buildCatalogEntries()
}

func (mdb *MdbHandle) catalogFileInfo() (path string, size, mtime int64, ok bool) {
	if mdb.f == nil || mdb.f.path == "" {
		return "", 0, 0, false
	}
	f, isFile := mdb.f.stream.(*os.File)
	if !isFile {
		return "", 0, 0, false
	}
	fi, err := f.Stat()
	if err != nil {
		return "", 0, 0, false
	}
	return mdb.f.path, fi.Size(), fi.ModTime().UnixNano(), true
}

// buildCatalogEntries reads MSysObjects and parses every row without the
// per-handle filtering, so the result can be cached per file.
func (mdb *MdbHandle) buildCatalogEntries() ([]cachedCatalogEntry, error) {
	// Create a dummy catalog entry for MSysObjects (page 2)
	dummyEntry := &CatalogEntry{
		Mdb:        mdb,
		ObjectName: "MSysObjects",
		ObjectType: ObjTable,
		TablePg:    2,
	}

	table, err := mdb.ReadTable(dummyEntry)
	if err != nil {
		return nil, fmt.Errorf("gomdb: unable to read MSysObjects table: %w", err)
	}
	defer mdb.FreeTableDef(table)

	if err := mdb.ReadColumns(table); err != nil {
		return nil, fmt.Errorf("gomdb: unable to read MSysObjects columns: %w", err)
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
		return nil, fmt.Errorf("gomdb: unable to find all required MSysObjects columns")
	}

	idCol := table.Columns[idIdx]
	nameCol := table.Columns[nameIdx]
	typeCol := table.Columns[typeIdx]
	flagsCol := table.Columns[flagsIdx]
	lvPropCol := table.Columns[lvPropIdx]

	mdb.RewindTable(table)

	var entries []cachedCatalogEntry
	for {
		hasRow, err := mdb.FetchRow(table)
		if err != nil {
			return nil, fmt.Errorf("gomdb: error reading MSysObjects: %w", err)
		}
		if !hasRow {
			break
		}

		typ, _ := mdb.Int64Value(typeCol)
		flags, _ := mdb.Int64Value(flagsCol)
		objName := mdb.columnValueToString(nameCol)

		entry := cachedCatalogEntry{
			name:    objName,
			rawType: int(typ),
			flags:   int(flags),
		}

		// Parse table page from Id (low 24 bits)
		if id, ok := mdb.Int64Value(idCol); ok {
			entry.tablePg = uint32(id & 0x00FFFFFF)
		}

		// Read LvProp (KKD data) if present
		if !lvPropCol.CurValueIsNull {
			kkd, kkdLen, err := mdb.OleReadFull(lvPropCol, nil)
			if err == nil && kkdLen > 0 {
				entry.props = kkdToProps(mdb, kkd, kkdLen)
			}
		}

		entries = append(entries, entry)
	}

	return entries, nil
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
