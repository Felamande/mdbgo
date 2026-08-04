package gomdb

import (
	"os"
	"testing"
)

func TestInAndOrderByOnMibTree(t *testing.T) {
	if _, err := os.Stat(fastLmPath); err != nil {
		t.Skipf("%s not present", fastLmPath)
	}

	const inQuery = "SELECT OID FROM [MibTree] WHERE OID IN ('1.2.1.1.1.1','1.2.1.1.1','1.2.1.1','1.2.1')"

	// The user's example: TOP 1 + ORDER BY Len(OID) DESC must pick the
	// longest matching OID.
	q, err := OpenQuery(fastLmPath, "SELECT TOP 1 * FROM [MibTree] WHERE OID IN ('1.2.1.1.1.1','1.2.1.1.1','1.2.1.1','1.2.1') ORDER BY Len(OID) DESC")
	if err != nil {
		t.Fatalf("example query: %v", err)
	}
	defer q.Close()
	if n := len(q.ColumnInfo()); n != 44 {
		t.Fatalf("SELECT TOP 1 * column count = %d, want 44", n)
	}
	ok, err := q.Next()
	if err != nil || !ok {
		t.Fatalf("TOP 1 example returned ok=%v err=%v", ok, err)
	}
	if got := q.DriverValue(0); got != "1.2.1.1.1.1" {
		t.Fatalf("TOP 1 OID = %v, want 1.2.1.1.1.1", got)
	}
	if ok, _ := q.Next(); ok {
		t.Fatal("TOP 1 returned more than one row")
	}

	rows := consumeQueryRows(t, fastLmPath, inQuery)
	if len(rows) != 4 {
		t.Fatalf("IN query matched %d rows, want 4", len(rows))
	}
	want := map[string]bool{"1.2.1.1.1.1": true, "1.2.1.1.1": true, "1.2.1.1": true, "1.2.1": true}
	for _, row := range rows {
		if !want[row[0].(string)] {
			t.Errorf("unexpected IN match %v", row)
		}
	}

	desc := consumeQueryRows(t, fastLmPath, inQuery+" ORDER BY OID DESC")
	wantOrder := []string{"1.2.1.1.1.1", "1.2.1.1.1", "1.2.1.1", "1.2.1"}
	for i, want := range wantOrder {
		if got := desc[i][0]; got != want {
			t.Fatalf("ORDER BY OID DESC row %d = %v, want %s", i, got, want)
		}
	}

	top3 := consumeQueryRows(t, fastLmPath, "SELECT TOP 3 OID FROM [MibTree] ORDER BY OID ASC")
	if len(top3) != 3 {
		t.Fatalf("TOP 3 returned %d rows", len(top3))
	}
	if got := top3[0][0]; got != "1" {
		t.Fatalf("TOP 3 first OID = %v, want 1", got)
	}

	// TOP N PERCENT without ORDER BY still runs (fast path) and bounds rows.
	pct := consumeQueryRows(t, fastLmPath, "SELECT TOP 1 PERCENT OID FROM [MibTree]")
	if len(pct) == 0 || len(pct) > 300 {
		t.Fatalf("TOP 1 PERCENT returned %d rows, want 1..300", len(pct))
	}
}

// TestInLongestOIDFromPrefixSet checks the exact OID prefix set used by the
// longest-OID lookup: only the four shortest prefixes exist in lm.mdb, and
// WHERE IN + ORDER BY Len(OID) DESC must return the longest of them.
func TestInLongestOIDFromPrefixSet(t *testing.T) {
	if _, err := os.Stat(fastLmPath); err != nil {
		t.Skipf("%s not present", fastLmPath)
	}

	const inList = "('2.4.2.2.1.1.1.0.0.2.0','2.4.2.2.1.1.1.0.0.2','2.4.2.2.1.1.1.0.0','2.4.2.2.1.1.1.0','2.4.2.2.1.1.1','2.4.2.2.1.1','2.4.2.2.1','2.4.2.2')"

	// The IN list itself matches exactly the four OIDs present in lm.mdb.
	rows := consumeQueryRows(t, fastLmPath, "SELECT OID FROM [MibTree] WHERE OID IN "+inList)
	want := map[string]bool{
		"2.4.2.2.1.1.1": true,
		"2.4.2.2.1.1":   true,
		"2.4.2.2.1":     true,
		"2.4.2.2":       true,
	}
	if len(rows) != len(want) {
		t.Fatalf("IN query matched %d rows, want %d: %v", len(rows), len(want), rows)
	}
	for _, row := range rows {
		oid := row[0].(string)
		if !want[oid] {
			t.Errorf("unexpected IN match %q", oid)
		}
	}

	// TOP 1 with ORDER BY Len(OID) DESC must pick the longest existing OID.
	q, err := OpenQuery(fastLmPath, "SELECT TOP 1 OID FROM [MibTree] WHERE OID IN "+inList+" ORDER BY Len(OID) DESC")
	if err != nil {
		t.Fatalf("TOP 1 longest-OID query: %v", err)
	}
	defer q.Close()
	ok, err := q.Next()
	if err != nil || !ok {
		t.Fatalf("TOP 1 longest-OID query returned ok=%v err=%v", ok, err)
	}
	if got := q.DriverValue(0); got != "2.4.2.2.1.1.1" {
		t.Fatalf("longest OID = %v, want 2.4.2.2.1.1.1", got)
	}
	if ok, _ := q.Next(); ok {
		t.Fatal("TOP 1 returned more than one row")
	}
}

func TestInAndOrderByOnNullTest(t *testing.T) {
	const path = "../../testdata/nulltest.mdb"
	if _, err := os.Stat(path); err != nil {
		t.Skipf("%s not present", path)
	}

	// IN on an integer column: NULL rows never match, unparseable elements
	// are dropped, and quoted numeric elements still convert.
	rows := consumeQueryRows(t, path, "SELECT id FROM nulltest WHERE val_int IN (42, 1)")
	if len(rows) != 1 || rows[0][0] != int64(2) {
		t.Fatalf("val_int IN (42,1) = %v, want [2]", rows)
	}
	rows = consumeQueryRows(t, path, "SELECT id FROM nulltest WHERE val_int IN ('42', 99)")
	if len(rows) != 1 || rows[0][0] != int64(2) {
		t.Fatalf("val_int IN ('42',99) = %v, want [2]", rows)
	}
	if rows = consumeQueryRows(t, path, "SELECT id FROM nulltest WHERE val_int IN ('abc', 1)"); len(rows) != 0 {
		t.Fatalf("val_int IN ('abc',1) = %v, want no rows", rows)
	}

	// Boolean IN follows existing bool semantics.
	rows = consumeQueryRows(t, path, "SELECT id FROM nulltest WHERE val_bool IN (TRUE)")
	if len(rows) != 1 || rows[0][0] != int64(2) {
		t.Fatalf("val_bool IN (TRUE) = %v, want [2]", rows)
	}

	// Text IN.
	rows = consumeQueryRows(t, path, "SELECT id FROM nulltest WHERE val_text IN ('not null')")
	if len(rows) != 1 || rows[0][0] != int64(2) {
		t.Fatalf("val_text IN ('not null') = %v, want [2]", rows)
	}
	if rows = consumeQueryRows(t, path, "SELECT id FROM nulltest WHERE val_text IN ('')"); len(rows) != 0 {
		t.Fatalf("val_text IN ('') = %v, want no rows", rows)
	}
	if rows = consumeQueryRows(t, path, "SELECT id FROM nulltest WHERE val_int IN ('')"); len(rows) != 0 {
		t.Fatalf("val_int IN ('') = %v, want no rows", rows)
	}

	// ORDER BY: NULLs first ASC, last DESC.
	rows = consumeQueryRows(t, path, "SELECT id FROM nulltest ORDER BY val_int ASC")
	if len(rows) != 2 || rows[0][0] != int64(1) || rows[1][0] != int64(2) {
		t.Fatalf("ORDER BY val_int ASC = %v, want [1 2]", rows)
	}
	rows = consumeQueryRows(t, path, "SELECT id FROM nulltest ORDER BY val_int DESC")
	if rows[0][0] != int64(2) || rows[1][0] != int64(1) {
		t.Fatalf("ORDER BY val_int DESC = %v, want [2 1]", rows)
	}

	// TOP applies after ORDER BY.
	rows = consumeQueryRows(t, path, "SELECT TOP 1 id FROM nulltest ORDER BY id DESC")
	if len(rows) != 1 || rows[0][0] != int64(2) {
		t.Fatalf("TOP 1 ORDER BY id DESC = %v, want [2]", rows)
	}
}

func TestInLargeListUsesSearchPath(t *testing.T) {
	path := "../../testdata/nulltest.mdb"
	if _, err := os.Stat(path); err != nil {
		t.Skipf("%s not present", path)
	}
	// >8 elements exercises the resolve-time sort/dedupe + binary search.
	rows := consumeQueryRows(t, path, "SELECT id FROM nulltest WHERE val_int IN (1,2,3,4,5,6,7,8,9,42)")
	if len(rows) != 1 || rows[0][0] != int64(2) {
		t.Fatalf("large int IN = %v, want [2]", rows)
	}

	lm := fastLmPath
	if _, err := os.Stat(lm); err == nil {
		rows = consumeQueryRows(t, lm, "SELECT OID FROM [MibTree] WHERE OID IN ('1.2.1','1.2.1','1.2.1','x1','x2','x3','x4','x5','x6','x7')")
		if len(rows) != 1 || rows[0][0] != "1.2.1" {
			t.Fatalf("large text IN with duplicates = %v, want [1.2.1]", rows)
		}
	}
}

func TestPlanReuseWithOrderBy(t *testing.T) {
	path := "../../testdata/people.mdb"
	if _, err := os.Stat(path); err != nil {
		t.Skipf("%s not present", path)
	}
	mdb, err := OpenMDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()

	q, err := OpenQueryOnHandle(mdb, "SELECT id, name FROM people ORDER BY id DESC")
	if err != nil {
		t.Fatal(err)
	}
	first := consumeRows(t, q)
	if len(first) != 2 {
		t.Fatalf("rows = %v, want 2", first)
	}
	p := q.CapturePlan()
	q.Close()
	if p == nil {
		t.Fatal("CapturePlan returned nil for ORDER BY query")
	}
	defer p.release()

	for i := 0; i < 3; i++ {
		q2, err := p.Execute(mdb)
		if err != nil {
			t.Fatalf("Execute #%d: %v", i, err)
		}
		rows := consumeRows(t, q2)
		q2.Close()
		if len(rows) != len(first) {
			t.Fatalf("execute %d rows = %d, want %d", i, len(rows), len(first))
		}
		for r := range rows {
			for c := range rows[r] {
				if !fastValueEqual(rows[r][c], first[r][c]) {
					t.Fatalf("execute %d row %d col %d: %#v vs %#v", i, r, c, rows[r][c], first[r][c])
				}
			}
		}
	}
}

func TestOrderByParseErrors(t *testing.T) {
	path := "../../testdata/people.mdb"
	if _, err := os.Stat(path); err != nil {
		t.Skipf("%s not present", path)
	}
	queries := []string{
		"SELECT id FROM people WHERE id IN ()",
		"SELECT id FROM people ORDER BY",
		"SELECT id FROM people ORDER BY LEN",
		"SELECT id FROM people ORDER BY LEN(id + 1)",
	}
	for _, q := range queries {
		qr, err := OpenQuery(path, q)
		if err == nil {
			qr.Close()
			t.Errorf("query %q: expected parse error", q)
		}
	}
}

// consumeQueryRows opens a query and returns all rows as DriverValue slices.
func consumeQueryRows(t *testing.T, path, query string) [][]any {
	t.Helper()
	q, err := OpenQuery(path, query)
	if err != nil {
		t.Fatalf("OpenQuery(%q): %v", query, err)
	}
	defer q.Close()
	return consumeRows(t, q)
}

func TestInEvalAllocs(t *testing.T) {
	mdb := &MdbHandle{
		f:          &MdbFile{jetVersion: Jet4},
		fmt:        &Jet4FormatConstants,
		unicodeBuf: make([]byte, 0, 64),
	}
	node := &SargNode{
		Op:      OpIn,
		ValType: TypeText,
		InBytes: [][]byte{[]byte("AB"), []byte("CD"), []byte("EF")},
	}
	src := []byte{'A', 0, 'B', 0} // UTF-16LE ASCII field bytes
	field := MdbField{Value: src, Siz: len(src), IsNull: false}
	s := &decodeScratch{unicode: make([]byte, 0, 64)}
	col := &MdbColumn{ColType: TypeText}

	allocs := testing.AllocsPerRun(1000, func() {
		if !testSargScratch(mdb, col, node, &field, mdb.pgBuf, s) {
			t.Fatal("IN text eval unexpectedly returned false")
		}
	})
	if allocs != 0 {
		t.Fatalf("scratch IN eval allocated %.1f times per run, want 0", allocs)
	}

	allocs = testing.AllocsPerRun(1000, func() {
		if !testSargScratch(mdb, col, node, &field, mdb.pgBuf, nil) {
			t.Fatal("IN text eval unexpectedly returned false")
		}
	})
	if allocs != 0 {
		t.Fatalf("sync IN eval allocated %.1f times per run, want 0", allocs)
	}
}
