package parity

import (
	"database/sql"
	"fmt"
	"sort"
	"testing"
)

// likeScenarios exercise WHERE ... LIKE through the full SQL path of both
// drivers. Converted from the one-off probes temp/diag_like.go and
// temp/diag_like2.go, which tracked a gomdb over-match on multibyte text.
// Patterns cover ASCII prefix/suffix/underscore, multibyte Chinese, and
// empty values.
var likeScenarios = []struct {
	file  string
	query string
}{
	{"typed.mdb", "SELECT id, val_text FROM typed WHERE val_text LIKE 'hello%'"},
	{"typed.mdb", "SELECT id, val_text FROM typed WHERE val_text LIKE '%world'"},
	{"typed.mdb", "SELECT id, val_text FROM typed WHERE val_text LIKE '_ello%'"},
	{"typed.mdb", "SELECT id, val_text FROM typed WHERE val_text LIKE '%'"},
	{"chinese.mdb", "SELECT id, name FROM chinese WHERE name LIKE '%张%'"},
	{"chinese.mdb", "SELECT id, name FROM chinese WHERE name LIKE '张_'"},
	{"chinese.mdb", "SELECT id, name FROM chinese WHERE name LIKE '混合%'"},
	{"lm.mdb", "SELECT CmdID FROM [CmdTree] WHERE CmdDesc LIKE '%查询光模块%'"},
	{"lm.mdb", "SELECT CmdID FROM [CmdTree] WHERE CmdDesc LIKE '%配置%'"},
	{"lm.mdb", "SELECT CmdID FROM [CmdTree] WHERE CmdDesc LIKE '%。%'"},
}

// TestLikeKnownDivergences documents intentional behavior differences from the
// mdbtools reference (cmdb) that gomdb deliberately does not reproduce:
//
//  1. LIKE on Memo/Hyperlink columns never matches in cmdb, even for ASCII
//     patterns; gomdb evaluates LIKE against memo values. (Multibyte LIKE on
//     Text columns agrees between the drivers.)
//  2. '%_%' matches the empty string in cmdb but not in gomdb.
//
// Assert the current behavior so either driver changing trips this test.
func TestLikeKnownDivergences(t *testing.T) {
	cases := []struct {
		file      string
		query     string
		cmdbWant  int
		gomdbWant int
	}{
		{"typed.mdb", "SELECT id, val_memo FROM typed WHERE val_memo LIKE '%memo%'", 0, 1},
		{"typed.mdb", "SELECT id, val_text FROM typed WHERE val_text LIKE '%_%'", 3, 2},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			c, g := openPair(t, tc.file)
			_, cRows := querySQL(t, c, tc.query, "cmdb")
			_, gRows := querySQL(t, g, tc.query, "gomdb")
			if got := len(cRows); got != tc.cmdbWant {
				t.Errorf("cmdb matched %d rows, want %d", got, tc.cmdbWant)
			}
			if got := len(gRows); got != tc.gomdbWant {
				t.Errorf("gomdb matched %d rows, want %d", got, tc.gomdbWant)
			}
		})
	}
}

func TestLikeParity(t *testing.T) {
	for _, sc := range likeScenarios {
		t.Run(sc.file, func(t *testing.T) {
			c, g := openPair(t, sc.file)
			cCols, cRows := querySQL(t, c, sc.query, "cmdb")
			_, gRows := querySQL(t, g, sc.query, "gomdb")
			if len(cRows) != len(gRows) {
				t.Fatalf("row count cmdb=%d gomdb=%d", len(cRows), len(gRows))
			}

			// WHERE result order is not a driver contract; compare as sorted rows.
			sortRows(cRows)
			sortRows(gRows)
			for i := range cRows {
				for j := range cRows[i] {
					if equal, kind := valuesEqual(cRows[i][j], gRows[i][j]); !equal {
						t.Errorf("row=%d col=%d (%s) [%s] cmdb=%v gomdb=%v",
							i+1, j+1, cCols[j], kind, cRows[i][j], gRows[i][j])
					}
				}
			}
		})
	}
}

func sortRows(rows [][]interface{}) {
	sort.Slice(rows, func(i, j int) bool {
		return fmt.Sprintf("%v", rows[i]) < fmt.Sprintf("%v", rows[j])
	})
}

// querySQL runs an arbitrary query and collects the scanned rows.
func querySQL(t *testing.T, db *sql.DB, query, label string) ([]string, [][]interface{}) {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatalf("%s query %q: %v", label, query, err)
	}
	defer rows.Close()
	colNames, err := rows.Columns()
	if err != nil {
		t.Fatalf("%s query %q columns: %v", label, query, err)
	}
	var result [][]interface{}
	for rows.Next() {
		vals := make([]interface{}, len(colNames))
		ptrs := make([]interface{}, len(colNames))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("%s query %q scan: %v", label, query, err)
		}
		result = append(result, vals)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s query %q: %v", label, query, err)
	}
	return colNames, result
}
