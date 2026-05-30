package puredb

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

// PackRow packs fields into a row buffer for a temp table.
func (mdb *MdbHandle) PackRow(table *MdbTableDef, rowBuf []byte, numFields int, fields []MdbField) int {
	pos := 0

	// Number of variable columns
	varColCount := 0
	for _, col := range table.Columns {
		if !col.IsFixed {
			varColCount++
		}
	}
	rowBuf[pos] = byte(varColCount)
	pos++

	// Null mask placeholder (will be filled later)
	nullMaskSize := (table.NumCols + 7) / 8
	nullMaskPos := pos
	for i := 0; i < nullMaskSize; i++ {
		rowBuf[pos] = 0
		pos++
	}

	// Variable column offset table placeholder
	varOffsetPos := pos
	pos += varColCount

	// Write variable column data
	varOffsetIdx := 0
	for _, col := range table.Columns {
		if col.IsFixed {
			continue
		}

		// Find matching field
		var field *MdbField
		for fi := 0; fi < numFields; fi++ {
			if fields[fi].ColNum == col.ColNum {
				field = &fields[fi]
				break
			}
		}
		if field == nil {
			continue
		}

		// Set null bit if needed
		if field.IsNull {
			byteNum := (col.RowColNum - 1) / 8
			bitNum := (col.RowColNum - 1) % 8
			rowBuf[nullMaskPos+byteNum] &^= (1 << bitNum) // clear bit = null
		} else {
			byteNum := (col.RowColNum - 1) / 8
			bitNum := (col.RowColNum - 1) % 8
			rowBuf[nullMaskPos+byteNum] |= (1 << bitNum) // set bit = not null
		}

		// Set variable offset
		if varOffsetIdx < varColCount {
			off := pos - varOffsetPos - varColCount
			rowBuf[varOffsetPos+varOffsetIdx*2] = byte(off & 0xFF)
			rowBuf[varOffsetPos+varOffsetIdx*2+1] = byte((off >> 8) & 0xFF)
			varOffsetIdx++
		}

		// Copy data
		if !field.IsNull && field.Value != nil {
			copy(rowBuf[pos:], field.Value)
			pos += field.Siz
		}
	}

	// Fixed column data
	fixedNum := 0
	for _, col := range table.Columns {
		if col.IsFixed {
			fixedNum++
		}
	}

	// Fixed null mask
	fixedNullPos := pos
	for i := 0; i < nullMaskSize; i++ {
		rowBuf[pos] = 0
		pos++
	}

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
		if field == nil {
			continue
		}

		if field.IsNull {
			byteNum := (col.RowColNum - 1) / 8
			bitNum := (col.RowColNum - 1) % 8
			rowBuf[fixedNullPos+byteNum] &^= (1 << bitNum)
		} else {
			byteNum := (col.RowColNum - 1) / 8
			bitNum := (col.RowColNum - 1) % 8
			rowBuf[fixedNullPos+byteNum] |= (1 << bitNum)

			if field.Value != nil {
				copy(rowBuf[pos:], field.Value)
				pos += field.Siz
			}
		}
	}

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
		return fmt.Errorf("puredb: describe table: %w", err)
	}
	defer mdb.FreeTableDef(table)

	if err := mdb.ReadColumns(table); err != nil {
		return fmt.Errorf("puredb: describe table columns: %w", err)
	}

	ttable := mdb.CreateTempTable("#describe")

	col1 := &MdbColumn{}
	FillTempCol(col1, "Column Name", 30, TypeText, false)
	col1.RowColNum = 1
	mdb.TempTableAddCol(ttable, col1)

	col2 := &MdbColumn{}
	FillTempCol(col2, "Type", 20, TypeText, false)
	col2.RowColNum = 2
	mdb.TempTableAddCol(ttable, col2)

	col3 := &MdbColumn{}
	FillTempCol(col3, "Size", 10, TypeText, false)
	col3.RowColNum = 3
	mdb.TempTableAddCol(ttable, col3)

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
