// Package parity verifies that the cmdb (C mdbtools) and gomdb (pure Go)
// drivers produce identical results against the same test databases.
//
// Converted from the one-off diagnostic programs in temp/ (main.go,
// deepcmp.go, finalcmp.go, fullcmp.go): table lists, DESCRIBE schemas,
// and SELECT * row data must match between the two drivers.
package parity

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	cmdb "github.com/Felamande/mdbgo/driver/cmdb"
	gomdb "github.com/Felamande/mdbgo/driver/gomdb"
)

// lm.mdb is large (27k rows) and gitignored; the others are tracked.
var parityFiles = []string{
	"people.mdb",
	"typed.mdb",
	"nulltest.mdb",
	"chinese.mdb",
	"lm.mdb",
}

func openPair(t *testing.T, file string) (*sql.DB, *sql.DB) {
	t.Helper()
	path := "../../testdata/" + file
	if _, err := os.Stat(path); err != nil {
		t.Skipf("%s not present", path)
	}
	drivers := []struct {
		name string
		drv  string
	}{
		{"cmdb", cmdb.DriverName},
		{"gomdb", gomdb.DriverName},
	}
	var dbs []*sql.DB
	for _, d := range drivers {
		db, err := sql.Open(d.drv, path)
		if err != nil {
			t.Fatalf("%s open %s: %v", d.name, file, err)
		}
		if err := db.PingContext(context.Background()); err != nil {
			db.Close()
			t.Fatalf("%s ping %s: %v", d.name, file, err)
		}
		t.Cleanup(func() { db.Close() })
		dbs = append(dbs, db)
	}
	return dbs[0], dbs[1]
}

func TestTableListParity(t *testing.T) {
	for _, file := range parityFiles {
		t.Run(file, func(t *testing.T) {
			c, g := openPair(t, file)
			cTables := listTables(t, c, "cmdb")
			gTables := listTables(t, g, "gomdb")

			var diffs []string
			for _, name := range sortedUnion(cTables, gTables) {
				if cTables[name] != gTables[name] {
					diffs = append(diffs, fmt.Sprintf("%-30s cmdb=%-5v gomdb=%-5v", name, cTables[name], gTables[name]))
				}
			}
			if len(diffs) > 0 {
				t.Fatalf("table list mismatch:\n  %s", strings.Join(diffs, "\n  "))
			}
		})
	}
}

type colDef struct {
	name string
	typ  string
	size int64
}

func TestSchemaParity(t *testing.T) {
	for _, file := range parityFiles {
		c, g := openPair(t, file)
		for _, table := range sortedKeys(listTables(t, c, "cmdb")) {
			t.Run(file+"/"+table, func(t *testing.T) {
				cCols := describeTable(t, c, table, "cmdb")
				gCols := describeTable(t, g, table, "gomdb")

				if len(cCols) != len(gCols) {
					t.Fatalf("column count cmdb=%d gomdb=%d", len(cCols), len(gCols))
				}
				for i := range cCols {
					if cCols[i] != gCols[i] {
						t.Errorf("column %d: cmdb=(%s,%s,%d) gomdb=(%s,%s,%d)",
							i, cCols[i].name, cCols[i].typ, cCols[i].size,
							gCols[i].name, gCols[i].typ, gCols[i].size)
					}
				}
			})
		}
	}
}

func TestDataParity(t *testing.T) {
	for _, file := range parityFiles {
		c, g := openPair(t, file)
		for _, table := range sortedKeys(listTables(t, c, "cmdb")) {
			t.Run(file+"/"+table, func(t *testing.T) {
				cCols, cRows := queryAll(t, c, table, "cmdb")
				gCols, gRows := queryAll(t, g, table, "gomdb")

				if len(cCols) != len(gCols) {
					t.Fatalf("query column count cmdb=%d gomdb=%d", len(cCols), len(gCols))
				}
				if len(cRows) != len(gRows) {
					t.Fatalf("row count cmdb=%d gomdb=%d", len(cRows), len(gRows))
				}

				diffs := 0
				for i := range cRows {
					for j := range cRows[i] {
						if equal, kind := valuesEqual(cRows[i][j], gRows[i][j]); !equal {
							diffs++
							if diffs <= 10 {
								t.Errorf("row=%d col=%d (%s) [%s] cmdb=%v gomdb=%v",
									i+1, j+1, cCols[j], kind, cRows[i][j], gRows[i][j])
							}
						}
					}
				}
				if diffs > 10 {
					t.Errorf("... and %d more value differences", diffs-10)
				}
			})
		}
	}
}

// valuesEqual compares two scanned values, tolerating float round-trip error
// like the reference mdbtools comparison did.
func valuesEqual(a, b interface{}) (equal bool, kind string) {
	if a == nil && b == nil {
		return true, ""
	}
	if a == nil || b == nil {
		return false, "null"
	}
	if ab, ok := a.([]byte); ok {
		if bb, ok := b.([]byte); ok {
			if len(ab) != len(bb) {
				return false, "string"
			}
			for i := range ab {
				if ab[i] != bb[i] {
					return false, "string"
				}
			}
			return true, ""
		}
		return false, "string"
	}
	if at, ok := a.(time.Time); ok {
		if bt, ok := b.(time.Time); ok {
			return at.Equal(bt), "time"
		}
		return false, "null"
	}
	af, aIsF := toFloat(a)
	bf, bIsF := toFloat(b)
	if aIsF && bIsF {
		diff := af - bf
		if diff < 0 {
			diff = -diff
		}
		if af == 0 && bf == 0 {
			return true, ""
		}
		relDiff := diff
		if af != 0 {
			relDiff = diff / af
		}
		if relDiff < 0 {
			relDiff = -relDiff
		}
		if diff < 1e-9 || relDiff < 1e-7 {
			return true, ""
		}
		return false, "float"
	}
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b), "string"
}

func toFloat(v interface{}) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int64:
		return float64(x), true
	}
	return 0, false
}

// ── helpers ──

func listTables(t *testing.T, db *sql.DB, label string) map[string]bool {
	t.Helper()
	rows, err := db.Query("LIST TABLES")
	if err != nil {
		t.Fatalf("%s LIST TABLES: %v", label, err)
	}
	defer rows.Close()
	tables := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("%s LIST TABLES scan: %v", label, err)
		}
		tables[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s LIST TABLES: %v", label, err)
	}
	return tables
}

func describeTable(t *testing.T, db *sql.DB, table, label string) []colDef {
	t.Helper()
	rows, err := db.Query(fmt.Sprintf("DESCRIBE TABLE [%s]", table))
	if err != nil {
		t.Fatalf("%s DESCRIBE %s: %v", label, table, err)
	}
	defer rows.Close()
	var cols []colDef
	for rows.Next() {
		var name, typ, sizeStr string
		if err := rows.Scan(&name, &typ, &sizeStr); err != nil {
			t.Fatalf("%s DESCRIBE %s scan: %v", label, table, err)
		}
		var size int64
		fmt.Sscanf(sizeStr, "%d", &size)
		cols = append(cols, colDef{name: name, typ: typ, size: size})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s DESCRIBE %s: %v", label, table, err)
	}
	return cols
}

func queryAll(t *testing.T, db *sql.DB, table, label string) ([]string, [][]interface{}) {
	t.Helper()
	rows, err := db.Query(fmt.Sprintf("SELECT * FROM [%s]", table))
	if err != nil {
		t.Fatalf("%s SELECT %s: %v", label, table, err)
	}
	defer rows.Close()
	colNames, err := rows.Columns()
	if err != nil {
		t.Fatalf("%s SELECT %s columns: %v", label, table, err)
	}
	var result [][]interface{}
	for rows.Next() {
		vals := make([]interface{}, len(colNames))
		ptrs := make([]interface{}, len(colNames))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("%s SELECT %s scan: %v", label, table, err)
		}
		result = append(result, vals)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s SELECT %s: %v", label, table, err)
	}
	return colNames, result
}

func sortedKeys(m map[string]bool) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

func sortedUnion(a, b map[string]bool) []string {
	all := make(map[string]bool)
	for k := range a {
		all[k] = true
	}
	for k := range b {
		all[k] = true
	}
	return sortedKeys(all)
}
