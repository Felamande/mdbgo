package gomdb

import "sync"

// tableTemplate is the immutable schema of a table: all metadata needed to
// run queries, without per-connection state. Templates are shared across
// connections via a per-file cache; each query clones the template so scan
// state (page cursors, bound values, sarg trees) stays connection-local.
type tableTemplate struct {
	name         string
	numCols      int
	numVarCols   int
	numRows      int
	numIdxs      int
	numRealIdxs  int
	firstDataPg  uint32
	indexStart   int
	mapSz        int
	freemapSz    int
	usageMap     []byte
	freeUsageMap []byte
	props        *Properties
	columns      []MdbColumn
}

type fileTableCache struct {
	size   int64
	mtime  int64
	tables map[string]*tableTemplate
}

var (
	tableCacheMu sync.Mutex
	tableCache   = make(map[string]*fileTableCache)
)

const maxTableCacheFiles = 64

// readTableCached returns a fresh MdbTableDef for a catalog entry, reusing
// the per-file schema cache when possible.
func (mdb *MdbHandle) readTableCached(entry *CatalogEntry) (*MdbTableDef, error) {
	if path, size, mtime, ok := mdb.catalogFileInfo(); ok {
		tableCacheMu.Lock()
		if fc, hit := tableCache[path]; hit && fc.size == size && fc.mtime == mtime {
			if tmpl, ok := fc.tables[entry.ObjectName]; ok {
				tableCacheMu.Unlock()
				return mdb.tableFromTemplate(tmpl, entry), nil
			}
		}
		tableCacheMu.Unlock()
	}

	tmpl, err := mdb.loadTableTemplate(entry)
	if err != nil {
		return nil, err
	}
	if path, size, mtime, ok := mdb.catalogFileInfo(); ok {
		tableCacheMu.Lock()
		fc := tableCache[path]
		if fc == nil || fc.size != size || fc.mtime != mtime {
			if len(tableCache) >= maxTableCacheFiles {
				clear(tableCache)
			}
			fc = &fileTableCache{size: size, mtime: mtime, tables: map[string]*tableTemplate{}}
			tableCache[path] = fc
		}
		fc.tables[entry.ObjectName] = tmpl
		tableCacheMu.Unlock()
	}
	return mdb.tableFromTemplate(tmpl, entry), nil
}

// loadTableTemplate reads and parses a table definition plus its columns.
func (mdb *MdbHandle) loadTableTemplate(entry *CatalogEntry) (*tableTemplate, error) {
	table, err := mdb.ReadTable(entry)
	if err != nil {
		return nil, err
	}
	defer mdb.FreeTableDef(table)
	if err := mdb.ReadColumns(table); err != nil {
		return nil, err
	}

	tmpl := &tableTemplate{
		name:         table.Name,
		numCols:      table.NumCols,
		numVarCols:   table.NumVarCols,
		numRows:      table.NumRows,
		numIdxs:      table.NumIdxs,
		numRealIdxs:  table.NumRealIdxs,
		firstDataPg:  table.FirstDataPg,
		indexStart:   table.IndexStart,
		mapSz:        table.MapSz,
		freemapSz:    table.FreemapSz,
		usageMap:     table.UsageMap,
		freeUsageMap: table.FreeUsageMap,
		props:        table.Props,
		columns:      make([]MdbColumn, len(table.Columns)),
	}
	for i, col := range table.Columns {
		c := *col
		c.Table = nil
		tmpl.columns[i] = c
	}
	return tmpl, nil
}

// tableFromTemplate materializes a query-local table definition.
func (mdb *MdbHandle) tableFromTemplate(tmpl *tableTemplate, entry *CatalogEntry) *MdbTableDef {
	table := &MdbTableDef{
		Entry:        entry,
		Name:         tmpl.name,
		NumCols:      tmpl.numCols,
		NumVarCols:   tmpl.numVarCols,
		NumRows:      tmpl.numRows,
		NumIdxs:      tmpl.numIdxs,
		NumRealIdxs:  tmpl.numRealIdxs,
		FirstDataPg:  tmpl.firstDataPg,
		IndexStart:   tmpl.indexStart,
		MapSz:        tmpl.mapSz,
		UsageMap:     tmpl.usageMap,
		FreemapSz:    tmpl.freemapSz,
		FreeUsageMap: tmpl.freeUsageMap,
		Props:        tmpl.props,
	}
	table.Columns = make([]*MdbColumn, len(tmpl.columns))
	for i := range tmpl.columns {
		c := tmpl.columns[i]
		c.Table = table
		table.Columns[i] = &c
	}
	return table
}
