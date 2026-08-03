package gomdb

import (
	"encoding/binary"
	"strconv"
	"strings"
	"testing"
)

// TestTempTableRoundTrip writes rows into a temporary table and reads them
// back through RewindTable/FetchRow, verifying bound column values survive the
// PackRow -> AddRowToTempTable -> CrackRow cycle. Converted from the one-off
// probes temp/diag3.go and temp/diag4.go, which debugged exactly this path.
func TestTempTableRoundTrip(t *testing.T) {
	mdb := &MdbHandle{
		f:   &MdbFile{jetVersion: Jet4},
		fmt: &Jet4FormatConstants,
	}
	table := mdb.CreateTempTable("#roundtrip")

	textCol := &MdbColumn{}
	FillTempCol(textCol, "name", 30, TypeText, false)
	textCol.RowColNum = 1
	mdb.TempTableAddCol(table, textCol)

	// Fixed columns sit right after the 2-byte column count header. Temp
	// tables have no file to read FixedOffset from, so it must be set by the
	// caller (0 for the first fixed column).
	idCol := &MdbColumn{}
	FillTempCol(idCol, "id", 0, TypeLongInt, true)
	idCol.RowColNum = 2
	idCol.FixedOffset = 0
	mdb.TempTableAddCol(table, idCol)

	rows := []struct {
		name string
		id   int64
		null bool
	}{
		{"Ada", 1, false},
		{"Grace", 2, false},
		{"", 3, true},
	}

	var fields [2]MdbField
	rowBuf := make([]byte, mdb.fmt.PgSize)
	for _, r := range rows {
		name := ASCIItoUCS2(r.name)
		FillTempField(&fields[0], name, len(name), false, r.null, 0, 0)
		id := make([]byte, 4)
		binary.LittleEndian.PutUint32(id, uint32(r.id))
		FillTempField(&fields[1], id, 4, true, false, 0, 1)
		rowSize := mdb.PackRow(table, rowBuf, 2, fields[:])
		mdb.AddRowToTempTable(table, rowBuf, rowSize)
		table.NumRows++
	}

	for _, col := range table.Columns {
		col.BindPtr = make([]byte, 256)
	}

	mdb.RewindTable(table)
	for i, r := range rows {
		ok, err := mdb.FetchRow(table)
		if err != nil {
			t.Fatalf("FetchRow row %d: %v", i, err)
		}
		if !ok {
			t.Fatalf("FetchRow row %d: unexpected end of table", i)
		}
		if got := mdb.GetBoundValue(table, 0); got != r.name {
			t.Errorf("row %d name = %q, want %q", i, got, r.name)
		}
		if got := mdb.GetBoundValue(table, 1); got != strconv.FormatInt(r.id, 10) {
			t.Errorf("row %d id = %q, want %d", i, got, r.id)
		}
		if r.null {
			if !table.Columns[0].CurValueIsNull {
				t.Errorf("row %d name: want NULL", i)
			}
		} else if want := len(ASCIItoUCS2(r.name)); table.Columns[0].CurValueLen != want {
			t.Errorf("row %d name CurValueLen = %d, want %d", i, table.Columns[0].CurValueLen, want)
		}
	}
	if ok, err := mdb.FetchRow(table); err != nil || ok {
		t.Fatalf("FetchRow past end = %v, %v; want false, nil", ok, err)
	}
}

// TestTempTableMultiPageRoundTrip verifies FetchRow walks rows that spill
// across multiple temporary-table pages.
func TestTempTableMultiPageRoundTrip(t *testing.T) {
	mdb := &MdbHandle{
		f:   &MdbFile{jetVersion: Jet4},
		fmt: &Jet4FormatConstants,
	}
	table := mdb.CreateTempTable("#multipage")

	col := &MdbColumn{}
	FillTempCol(col, "value", 200, TypeText, false)
	col.RowColNum = 1
	mdb.TempTableAddCol(table, col)

	const n = 200
	fields := [1]MdbField{}
	rowBuf := make([]byte, mdb.fmt.PgSize)
	for i := 0; i < n; i++ {
		value := "value-" + strconv.Itoa(i) + strings.Repeat("x", 80)
		ucs2 := ASCIItoUCS2(value)
		FillTempField(&fields[0], ucs2, len(ucs2), false, false, 0, 0)
		rowSize := mdb.PackRow(table, rowBuf, 1, fields[:])
		mdb.AddRowToTempTable(table, rowBuf, rowSize)
		table.NumRows++
	}
	if pages := len(table.TempTablePages); pages < 2 {
		t.Fatalf("rows fit in %d page(s), want spill across multiple pages", pages)
	}

	// TempTableAddCol copies the column, so bind the table's copy.
	table.Columns[0].BindPtr = make([]byte, 256)
	mdb.RewindTable(table)
	for i := 0; i < n; i++ {
		ok, err := mdb.FetchRow(table)
		if err != nil || !ok {
			t.Fatalf("FetchRow %d: ok=%v err=%v", i, ok, err)
		}
		want := "value-" + strconv.Itoa(i) + strings.Repeat("x", 80)
		if got := mdb.GetBoundValue(table, 0); got != want {
			t.Fatalf("row %d value = %q, want %q", i, got, want)
		}
	}
	if ok, err := mdb.FetchRow(table); err != nil || ok {
		t.Fatalf("FetchRow past end = %v, %v; want false, nil", ok, err)
	}
}
