package gomdb

import "fmt"

// CreateTempTable creates a temporary table (used for LIST TABLES, DESCRIBE TABLE).
func (mdb *MdbHandle) CreateTempTable(name string) *MdbTableDef {
	entry := &CatalogEntry{
		Mdb:        mdb,
		ObjectName: name,
		ObjectType: ObjTable,
		TablePg:    0,
	}

	table := &MdbTableDef{
		Entry:          entry,
		Name:           name,
		Columns:        make([]*MdbColumn, 0),
		IsTempTable:    true,
		TempTablePages: make([][]byte, 0),
	}

	return table
}

// TempTableAddCol adds a column to a temporary table.
func (mdb *MdbHandle) TempTableAddCol(table *MdbTableDef, col *MdbColumn) {
	col.Table = table
	col.ColNum = table.NumCols
	if !col.IsFixed {
		col.VarColNum = table.NumVarCols
		table.NumVarCols++
	}

	newCol := *col
	table.Columns = append(table.Columns, &newCol)
	table.NumCols++
}

// FillTempCol fills a MdbColumn with the specified attributes.
func FillTempCol(col *MdbColumn, name string, colSize, colType int, isFixed bool) {
	col.Name = name
	col.ColType = colType
	if colType == TypeText || colType == TypeMemo {
		col.ColSize = colSize
	} else {
		col.ColSize = ColFixedSize(colType)
	}
	col.IsFixed = isFixed
}

// FillTempField fills a MdbField with the specified attributes.
func FillTempField(field *MdbField, value []byte, siz int, isFixed bool, isNull bool, start int, colNum int) {
	field.Value = value
	field.Siz = siz
	field.IsFixed = isFixed
	field.IsNull = isNull
	field.Start = start
	field.ColNum = colNum
}

// AddRowToTempTable adds a row to a temporary table's page buffer.
func (mdb *MdbHandle) AddRowToTempTable(table *MdbTableDef, rowData []byte, rowSize int) {
	mfmt := mdb.fmt

	// If no pages exist yet, or current page is full, create a new one
	if len(table.TempTablePages) == 0 {
		newPage := make([]byte, mfmt.PgSize)
		table.TempTablePages = append(table.TempTablePages, newPage)
	}

	curPage := table.TempTablePages[len(table.TempTablePages)-1]

	// Get current row count
	currentRows := GetInt16(curPage, mfmt.RowCountOffset)

	// Calculate where to place the new row
	// Row offset table is at row_count_offset + 2, with 2 bytes per row
	// Data starts from end of page going backwards
	var dataStart int
	if currentRows == 0 {
		dataStart = mfmt.PgSize - rowSize
	} else {
		lastRowStart := GetInt16(curPage, mfmt.RowCountOffset+currentRows*2) & OffsetMask
		dataStart = lastRowStart - rowSize
	}

	// Check if there's enough space
	headerNeeded := mfmt.RowCountOffset + 2 + (currentRows+1)*2
	if dataStart < headerNeeded {
		// Create a new page
		newPage := make([]byte, mfmt.PgSize)
		table.TempTablePages = append(table.TempTablePages, newPage)
		curPage = newPage
		currentRows = 0
		dataStart = mfmt.PgSize - rowSize
	}

	// Set row count
	b := curPage[mfmt.RowCountOffset : mfmt.RowCountOffset+2]
	b[0] = byte(currentRows + 1)
	b[1] = byte((currentRows + 1) >> 8)

	// Set row offset
	rowOffset := mfmt.RowCountOffset + 2 + currentRows*2
	curPage[rowOffset] = byte(dataStart & 0xFF)
	curPage[rowOffset+1] = byte((dataStart >> 8) & 0xFF)

	// Copy row data
	copy(curPage[dataStart:dataStart+rowSize], rowData)

	// Update reference
	table.TempTablePages[len(table.TempTablePages)-1] = curPage
}

// PackRow packs fields into a Jet4 row buffer for a temp table.
// The row format matches what CrackRow (mdb_crack_row) expects:
//
//	[2 bytes: total col count] [fixed data] [var data] [var offsets+count] [null mask]
func (mdb *MdbHandle) PackRow(table *MdbTableDef, rowBuf []byte, numFields int, fields []MdbField) int {
	// Count variable columns via the table definition
	varColCount := 0
	fixedColCount := 0
	for _, col := range table.Columns {
		if col.IsFixed {
			fixedColCount++
		} else {
			varColCount++
		}
	}
	totalCols := varColCount + fixedColCount

	// Fixed column data starts at offset 2 (after 2-byte column count header)
	pos := 2

	// Write fixed column data
	for _, col := range table.Columns {
		if !col.IsFixed {
			continue
		}
		var field *MdbField
		for fi := 0; fi < numFields; fi++ {
			if fields[fi].ColNum == col.ColNum {
				field = &fields[fi]
				break
			}
		}
		if field == nil || field.IsNull || field.Value == nil {
			for range col.ColSize {
				rowBuf[pos] = 0
				pos++
			}
		} else {
			copy(rowBuf[pos:], field.Value)
			pos += len(field.Value)
		}
	}

	// Write variable column data + build offset table
	varOffsets := make([]int, varColCount+1)
	var varFields []*MdbField
	for _, col := range table.Columns {
		if col.IsFixed {
			continue
		}
		var field *MdbField
		for fi := 0; fi < numFields; fi++ {
			if fields[fi].ColNum == col.ColNum {
				field = &fields[fi]
				break
			}
		}
		varFields = append(varFields, field)
	}

	
	for i, field := range varFields {
		varOffsets[i] = pos
		if field != nil && !field.IsNull && field.Value != nil {
			copy(rowBuf[pos:], field.Value)
			pos += len(field.Value)
		}
	}
	varOffsets[varColCount] = pos

	// End-of-row layout: [offsets reversed] [varCount(2B)] [nullMask]
	nullMaskSize := (totalCols + 7) / 8

	// Variable column offset table (2 bytes each, offset[n] first, offset[0] closest to varCount)
	for i := varColCount; i >= 0; i-- {
		rowBuf[pos] = byte(varOffsets[i] & 0xFF)
		pos++
		rowBuf[pos] = byte((varOffsets[i] >> 8) & 0xFF)
		pos++
	}

	// Variable column count at end (2 bytes for Jet4)
	rowBuf[pos] = byte(varColCount & 0xFF)
	pos++
	rowBuf[pos] = byte((varColCount >> 8) & 0xFF)
	pos++

	// Null mask (at the very end)
	nullStart := pos
	for i := 0; i < nullMaskSize; i++ {
		rowBuf[pos] = 0
		pos++
	}

	// Set null mask bits: 1 = not null
	for _, col := range table.Columns {
		fieldIdx := col.ColNum
		if fieldIdx >= totalCols {
			continue
		}
		var isNotNull bool
		if col.IsFixed {
			for fi := 0; fi < numFields; fi++ {
				if fields[fi].ColNum == col.ColNum {
					isNotNull = !fields[fi].IsNull
					break
				}
			}
		} else {
			vi := 0
			for _, c := range table.Columns {
				if c.IsFixed {
					continue
				}
				if c.ColNum == col.ColNum {
					break
				}
				vi++
			}
			if vi < len(varFields) && varFields[vi] != nil {
				isNotNull = !varFields[vi].IsNull
			}
		}
		if isNotNull {
			byteNum := col.ColNum / 8
			bitNum := col.ColNum % 8
			rowBuf[nullStart+byteNum] |= (1 << bitNum)
		}
	}

	// Write 2-byte column count header at the very start
	rowBuf[0] = byte(totalCols & 0xFF)
	rowBuf[1] = byte((totalCols >> 8) & 0xFF)

	return pos
}

// ListTables implements the "LIST TABLES" SQL command.
func (mdb *MdbHandle) ListTables(sql *SQL) error {
	if err := mdb.ReadCatalog(ObjTable); err != nil {
		return err
	}

	ttable := mdb.CreateTempTable("#listtables")
	col := &MdbColumn{}
	FillTempCol(col, "Tables", 30, TypeText, false)
	col.RowColNum = 1
	mdb.TempTableAddCol(ttable, col)
	sql.AddColumn("Tables")

	var fields [1]MdbField
	rowBuf := make([]byte, mdb.fmt.PgSize)

	for _, entry := range mdb.Catalog {
		if IsUserTable(entry) {
			ucs2 := ASCIItoUCS2(entry.ObjectName)
			FillTempField(&fields[0], ucs2, len(ucs2), false, false, 0, 0)
			rowSize := mdb.PackRow(ttable, rowBuf, 1, fields[:])
			mdb.AddRowToTempTable(ttable, rowBuf, rowSize)
			ttable.NumRows++
		}
	}

	sql.CurTable = ttable
	return nil
}

// DescribeTable implements the "DESCRIBE TABLE name" SQL command.
func (mdb *MdbHandle) DescribeTable(sql *SQL, tableName string) error {
	table, err := mdb.ReadTableByName(tableName, ObjTable)
	if err != nil {
		return fmt.Errorf("gomdb: describe table: %w", err)
	}
	defer mdb.FreeTableDef(table)

	if err := mdb.ReadColumns(table); err != nil {
		return fmt.Errorf("gomdb: describe table columns: %w", err)
	}

	ttable := mdb.CreateTempTable("#describe")

	col1 := &MdbColumn{}
	FillTempCol(col1, "Column Name", 30, TypeText, false)
	col1.RowColNum = 1
	mdb.TempTableAddCol(ttable, col1)
		sql.AddColumn("Column Name")

	col2 := &MdbColumn{}
	FillTempCol(col2, "Type", 20, TypeText, false)
	col2.RowColNum = 2
	mdb.TempTableAddCol(ttable, col2)
		sql.AddColumn("Type")

	col3 := &MdbColumn{}
	FillTempCol(col3, "Size", 10, TypeText, false)
	col3.RowColNum = 3
	mdb.TempTableAddCol(ttable, col3)
		sql.AddColumn("Size")

	var fields [3]MdbField
	rowBuf := make([]byte, mdb.fmt.PgSize)

	for _, col := range table.Columns {
		ucs2 := ASCIItoUCS2(col.Name)
		FillTempField(&fields[0], ucs2, len(ucs2), false, false, 0, 0)

		typeName := ColTypeName(col.ColType)
		ucs2 = ASCIItoUCS2(typeName)
		FillTempField(&fields[1], ucs2, len(ucs2), false, false, 0, 1)

		sizeStr := fmt.Sprintf("%d", col.ColSize)
		ucs2 = ASCIItoUCS2(sizeStr)
		FillTempField(&fields[2], ucs2, len(ucs2), false, false, 0, 2)

		rowSize := mdb.PackRow(ttable, rowBuf, 3, fields[:])
		mdb.AddRowToTempTable(ttable, rowBuf, rowSize)
		ttable.NumRows++
	}

	sql.CurTable = ttable
	return nil
}
