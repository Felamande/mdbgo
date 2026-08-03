package gomdb

import "fmt"

// MdbTableDef represents a table definition in an MDB database.
type MdbTableDef struct {
	Entry *CatalogEntry
	Name  string

	NumCols    int
	Columns    []*MdbColumn
	NumRows    int
	NumVarCols int

	NumIdxs     int
	NumRealIdxs int
	Indices     []*MdbIndex

	FirstDataPg uint32
	IndexStart  int

	// Page tracking for iteration
	CurPgNum  uint32
	CurPhysPg uint32
	CurRow    int
	NoSkipDel int

	// Usage maps
	MapBasePg     uint32
	MapSz         int
	UsageMap      []byte
	FreemapBasePg uint32
	FreemapSz     int
	FreeUsageMap  []byte

	// Sarg tree (WHERE clause)
	SargTree *SargNode

	// Scan strategy
	Strategy int
	ScanIdx  *MdbIndex
	MdbIdx   *MdbHandle
	Chain    *IndexChain

	// Temp table support
	IsTempTable    bool
	TempTablePages [][]byte

	// Pre-allocated field buffer for row cracking (reused across rows)
	fieldsBuf     []MdbField
	varOffsetsBuf []int
	crackLayouts  []crackLayout

	Props *Properties
}

// MdbIndex represents an index on a table.
type MdbIndex struct {
	IndexNum    int
	Name        string
	IndexType   int
	FirstPg     uint32
	NumRows     int
	NumKeys     int
	KeyColNum   [MaxIdxCols]int // 1-based column numbers
	KeyColOrder [MaxIdxCols]int // Asc or Desc
	Flags       int
	Table       *MdbTableDef
}

// IndexChain tracks state while walking an index's B-tree.
type IndexChain struct {
	CurDepth      int
	LastLeafFound uint32
	CleanUpMode   int
	Pages         [10]*MdbIndexPage
}

// MdbIndexPage holds state for a single index page.
type MdbIndexPage struct {
	Pg         uint32
	Offset     int
	StartPos   int
	Len        int
	RC         int
	CacheValue [256]byte
	IdxStarts  [2000]int
}

// MdbField represents a parsed field value from a row.
type MdbField struct {
	Value   []byte
	Siz     int
	Start   int
	IsNull  bool
	IsFixed bool
	ColNum  int
	Offset  int
}

// ReadTable reads a table definition from a catalog entry.
func (mdb *MdbHandle) ReadTable(entry *CatalogEntry) (*MdbTableDef, error) {
	mfmt := mdb.fmt

	if err := mdb.readPage(entry.TablePg); err != nil {
		return nil, fmt.Errorf("gomdb: unable to read table page %d: %w", entry.TablePg, err)
	}
	if mdb.pgBuf[0] != 0x02 {
		return nil, fmt.Errorf("gomdb: page %d is not a valid table definition page (type=%02x)", entry.TablePg, mdb.pgBuf[0])
	}

	table := &MdbTableDef{
		Entry: entry,
		Name:  entry.ObjectName,
	}

	table.NumRows = GetInt32(mdb.pgBuf[:], mfmt.TabNumRowsOffset)
	table.NumVarCols = GetInt16(mdb.pgBuf[:], mfmt.TabNumColsOffset-2)
	table.NumCols = GetInt16(mdb.pgBuf[:], mfmt.TabNumColsOffset)
	table.NumIdxs = GetInt32(mdb.pgBuf[:], mfmt.TabNumIdxsOffset)
	table.NumRealIdxs = GetInt32(mdb.pgBuf[:], mfmt.TabNumRidxsOffset)

	// Read usage map
	pgRow := GetInt32(mdb.pgBuf[:], mfmt.TabUsageMapOffset)
	buf, rowStart, mapSz, err := mdb.findPgRow(pgRow)
	if err != nil {
		return nil, fmt.Errorf("gomdb: unable to find usage map: %w", err)
	}
	if mapSz < 1 {
		return nil, fmt.Errorf("gomdb: invalid usage map size: %d", mapSz)
	}
	table.MapSz = mapSz
	table.UsageMap = make([]byte, mapSz)
	copy(table.UsageMap, buf[rowStart:rowStart+mapSz])

	// Read free space map
	pgRow = GetInt32(mdb.pgBuf[:], mfmt.TabFreeMapOffset)
	buf, rowStart, freemapSz, err := mdb.findPgRow(pgRow)
	if err != nil {
		return nil, fmt.Errorf("gomdb: unable to find free map: %w", err)
	}
	table.FreemapSz = freemapSz
	table.FreeUsageMap = make([]byte, freemapSz)
	copy(table.FreeUsageMap, buf[rowStart:rowStart+freemapSz])

	table.FirstDataPg = uint32(GetInt16(mdb.pgBuf[:], mfmt.TabFirstDpgOffset))

	// Copy table-level properties from entry
	if entry.Props != nil {
		for _, p := range entry.Props {
			if p.Name == "" {
				table.Props = p
				break
			}
		}
	}

	return table, nil
}

// ReadTableByName reads a table definition by table name and object type.
func (mdb *MdbHandle) ReadTableByName(name string, objType int) (*MdbTableDef, error) {
	if err := mdb.ReadCatalog(objType); err != nil {
		return nil, err
	}

	for _, entry := range mdb.Catalog {
		if equalFold(entry.ObjectName, name) {
			return mdb.ReadTable(entry)
		}
	}

	return nil, fmt.Errorf("gomdb: table %q not found", name)
}

// ReadColumns reads the column definitions for a table.
func (mdb *MdbHandle) ReadColumns(table *MdbTableDef) error {
	mfmt := mdb.fmt

	table.Columns = make([]*MdbColumn, 0, table.NumCols)

	// Read column attributes
	curPos := mfmt.TabColsStartOffset + table.NumRealIdxs*mfmt.TabRidxEntrySize
	mdb.curPos = curPos

	colBuf := make([]byte, mfmt.TabColEntrySize)
	for i := 0; i < table.NumCols; i++ {
		if n := mdb.ReadPgIfN(colBuf, mfmt.TabColEntrySize); n < mfmt.TabColEntrySize {
			return fmt.Errorf("gomdb: unable to read column %d attributes", i)
		}

		col := &MdbColumn{
			Table:   table,
			ColType: int(colBuf[0]),
			ColNum:  int(colBuf[mfmt.ColNumOffset]),
		}
		col.setNullMask()

		col.VarColNum = GetInt16(colBuf, mfmt.TabColOffsetVar)
		col.RowColNum = GetInt16(colBuf, mfmt.TabRowColNumOffset)

		if col.ColType == TypeNumeric || col.ColType == TypeMoney ||
			col.ColType == TypeFloat || col.ColType == TypeDouble {
			col.ColScale = int(colBuf[mfmt.ColScaleOffset])
			col.ColPrec = int(colBuf[mfmt.ColPrecOffset])
		}

		col.IsFixed = colBuf[mfmt.ColFlagsOffset]&0x01 != 0
		col.IsLongAuto = colBuf[mfmt.ColFlagsOffset]&0x04 != 0
		col.IsUUIDAuto = colBuf[mfmt.ColFlagsOffset]&0x40 != 0

		col.FixedOffset = GetInt16(colBuf, mfmt.TabColOffsetFixed)

		if col.ColType != TypeBool {
			col.ColSize = GetInt16(colBuf, mfmt.ColSizeOffset)
		}

		table.Columns = append(table.Columns, col)
	}

	// Read column names
	for i := 0; i < table.NumCols; i++ {
		col := table.Columns[i]
		var nameSz int
		if mdb.IsJet3() {
			nameSz = int(mdb.ReadPgIf8())
		} else {
			nameSz = int(mdb.ReadPgIf16())
		}
		tmpBuf := make([]byte, nameSz)
		if n := mdb.ReadPgIfN(tmpBuf, nameSz); n < nameSz {
			return fmt.Errorf("gomdb: unable to read column %d name", i)
		}
		col.Name = UnicodeToUTF8(tmpBuf, mdb.IsJet4())
	}

	// Sort columns by col_num
	sortColumnsByNum(table.Columns)

	// Attach column-level properties
	if table.Entry.Props != nil {
		for _, col := range table.Columns {
			for _, props := range table.Entry.Props {
				if props.Name != "" && props.Name == col.Name {
					col.Props = props
					break
				}
			}
		}
	}

	table.IndexStart = mdb.curPos
	return nil
}

// sortColumnsByNum sorts columns by their ColNum field (ascending).
func sortColumnsByNum(cols []*MdbColumn) {
	// Insertion sort — simple and fine for small number of columns
	for i := 1; i < len(cols); i++ {
		key := cols[i]
		j := i - 1
		for j >= 0 && cols[j].ColNum > key.ColNum {
			cols[j+1] = cols[j]
			j--
		}
		cols[j+1] = key
	}
}

// setNullMask precomputes the null-mask byte/bit position for ColNum.
func (col *MdbColumn) setNullMask() {
	col.NullByte = col.ColNum / 8
	col.NullBit = uint8(1 << (col.ColNum % 8))
	col.NullReady = true
}

// IsUserTable returns true if this is a user table (not system, not linked).
func IsUserTable(entry *CatalogEntry) bool {
	return entry.ObjectType == ObjTable && (entry.Flags&0x80000002) == 0
}

// IsSystemTable returns true if this is a system table.
func IsSystemTable(entry *CatalogEntry) bool {
	return entry.ObjectType == ObjTable && (entry.Flags&0x80000002) != 0
}

// ColIsShortDate checks if a DateTime column is formatted as a short date.
func ColIsShortDate(col *MdbColumn) bool {
	if col.Props == nil {
		return false
	}
	format, ok := col.Props.Hash["Format"]
	return ok && format == "Short Date"
}

// equalFold performs case-insensitive string comparison (ASCII only, like g_ascii_strcasecmp).
func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 32
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 32
		}
		if ca != cb {
			return false
		}
	}
	return true
}
