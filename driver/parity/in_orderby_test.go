package parity

import "testing"

// TestInOrderByKnownDivergence documents that IN and ORDER BY are gomdb-only
// features: the mdbtools SQL grammar behind cmdb has neither clause, so those
// queries fail there while gomdb evaluates them. Keep asserting both sides so
// either driver changing behavior trips this test.
func TestInOrderByKnownDivergence(t *testing.T) {
	c, g := openPair(t, "lm.mdb")
	queries := []struct {
		query    string
		wantRows int
	}{
		{"SELECT OID FROM [MibTree] WHERE OID IN ('1.2.1.1.1.1','1.2.1.1.1','1.2.1.1','1.2.1')", 4},
		{"SELECT OID FROM [MibTree] WHERE OID IN ('1.2.1.1.1.1','1.2.1.1.1','1.2.1.1','1.2.1') ORDER BY OID", 4},
		{"SELECT TOP 1 OID FROM [MibTree] WHERE OID IN ('1.2.1.1.1.1','1.2.1.1.1','1.2.1.1','1.2.1') ORDER BY Len(OID) DESC", 1},
	}
	for _, tc := range queries {
		if rows, err := c.Query(tc.query); err == nil {
			rows.Close()
			t.Errorf("cmdb unexpectedly accepted %q", tc.query)
		}
		_, rows := querySQL(t, g, tc.query, "gomdb")
		if len(rows) != tc.wantRows {
			t.Errorf("gomdb %q rows = %d, want %d", tc.query, len(rows), tc.wantRows)
		}
	}
}

// TestTopParity verifies that the TOP clause (supported by both drivers)
// returns identical row sets.
func TestTopParity(t *testing.T) {
	c, g := openPair(t, "lm.mdb")
	queries := []string{
		"SELECT TOP 1 OID FROM [MibTree] WHERE OID = '1.2.1.1.1.1'",
		"SELECT TOP 3 OID FROM [MibTree]",
		"SELECT TOP 500 OID FROM [MibTree]",
	}
	for _, q := range queries {
		_, cRows := querySQL(t, c, q, "cmdb")
		_, gRows := querySQL(t, g, q, "gomdb")
		if len(cRows) != len(gRows) {
			t.Fatalf("%q row count cmdb=%d gomdb=%d", q, len(cRows), len(gRows))
		}
		// TOP without ORDER BY has no row-order contract; compare as sets.
		sortRows(cRows)
		sortRows(gRows)
		for i := range cRows {
			for j := range cRows[i] {
				if equal, kind := valuesEqual(cRows[i][j], gRows[i][j]); !equal {
					t.Errorf("%q row=%d col=%d [%s] cmdb=%v gomdb=%v", q, i+1, j+1, kind, cRows[i][j], gRows[i][j])
				}
			}
		}
	}
}
