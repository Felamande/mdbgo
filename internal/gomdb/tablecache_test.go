package gomdb

import (
	"testing"
)

func TestTableCacheConsistency(t *testing.T) {
	tableCacheMu.Lock()
	clear(tableCache)
	tableCacheMu.Unlock()

	query := "SELECT id, name, active FROM people"
	run := func() [][]any {
		q, err := OpenQuery("../../testdata/people.mdb", query)
		if err != nil {
			t.Fatal(err)
		}
		defer q.Close()
		var rows [][]any
		for {
			ok, err := q.Next()
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				break
			}
			row := make([]any, 3)
			for i := 0; i < 3; i++ {
				row[i] = q.DriverValue(i)
			}
			rows = append(rows, row)
		}
		return rows
	}

	first := run()  // populates the table cache
	second := run() // cache hit
	if len(first) == 0 || len(first) != len(second) {
		t.Fatalf("row count mismatch: first=%d second=%d", len(first), len(second))
	}
	for i := range first {
		for c := range first[i] {
			if !fastValueEqual(first[i][c], second[i][c]) {
				t.Fatalf("row %d col %d mismatch: %#v vs %#v", i, c, first[i][c], second[i][c])
			}
		}
	}

	tableCacheMu.Lock()
	fc := tableCache["../../testdata/people.mdb"]
	tableCacheMu.Unlock()
	if fc == nil || fc.tables["people"] == nil {
		t.Fatal("table cache was not populated")
	}
}
