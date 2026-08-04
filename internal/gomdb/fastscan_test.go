package gomdb

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

const fastLmPath = "../../testdata/lm.mdb"

func openQueryMode(t *testing.T, query string, fast bool) *Query {
	t.Helper()
	if _, err := os.Stat(fastLmPath); err != nil {
		t.Skipf("%s not present", fastLmPath)
	}
	old := fastScanEnabled
	fastScanEnabled = fast
	defer func() { fastScanEnabled = old }()
	q, err := OpenQuery(fastLmPath, query)
	if err != nil {
		t.Fatalf("OpenQuery(%q): %v", query, err)
	}
	if (q.fast != nil) != fast {
		kind := "sync"
		if q.fast != nil {
			kind = "fast"
		}
		t.Fatalf("query %q opened in %s mode, want fast=%v", query, kind, fast)
	}
	return q
}

func consumeRows(t *testing.T, q *Query) [][]any {
	t.Helper()
	ncols := len(q.ColumnInfo())
	var rows [][]any
	for {
		ok, err := q.Next()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		row := make([]any, ncols)
		for c := 0; c < ncols; c++ {
			row[c] = q.DriverValue(c)
		}
		rows = append(rows, row)
	}
	return rows
}

func TestFastScanMatchesSync(t *testing.T) {
	queries := []string{
		"SELECT * FROM [MibTree]",
		"SELECT OID, MIBName, IsLeaf, ExcelLine FROM [MibTree]",
		"SELECT * FROM [MibTree] WHERE CmdID > 10000",
		"SELECT OID, MIBName FROM [MibTree] WHERE MIBName LIKE 'sys%'",
		"SELECT OID, MIBName FROM [MibTree] WHERE IsLeaf = 1 AND ExcelLine > 0",
		"SELECT OID FROM [MibTree] WHERE OID IN ('1.2.1.1.1.1','1.2.1.1.1','1.2.1.1','1.2.1')",
		"SELECT TOP 500 OID FROM [MibTree]",
		"SELECT * FROM [MibTree] LIMIT 500",
	}
	for _, query := range queries {
		t.Run(strings.ReplaceAll(query, " ", "_"), func(t *testing.T) {
			fast := openQueryMode(t, query, true)
			defer fast.Close()
			sync := openQueryMode(t, query, false)
			defer sync.Close()

			fastRows := consumeRows(t, fast)
			syncRows := consumeRows(t, sync)
			if len(fastRows) != len(syncRows) {
				t.Fatalf("row count fast=%d sync=%d", len(fastRows), len(syncRows))
			}
			for i := range fastRows {
				if len(fastRows[i]) != len(syncRows[i]) {
					t.Fatalf("row %d column count fast=%d sync=%d", i, len(fastRows[i]), len(syncRows[i]))
				}
				for c := range fastRows[i] {
					if !fastValueEqual(fastRows[i][c], syncRows[i][c]) {
						t.Fatalf("row %d col %d fast=%#v sync=%#v", i, c, fastRows[i][c], syncRows[i][c])
					}
				}
			}
		})
	}
}

func fastValueEqual(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if af, ok := a.(float64); ok {
		bf, ok := b.(float64)
		if !ok {
			return false
		}
		return af == bf
	}
	if at, ok := a.(time.Time); ok {
		bt, ok := b.(time.Time)
		return ok && at.Equal(bt)
	}
	if ab, ok := a.([]byte); ok {
		bb, ok := b.([]byte)
		return ok && string(ab) == string(bb)
	}
	return fmt.Sprintf("%#v", a) == fmt.Sprintf("%#v", b)
}

func TestFastScanLegacyGettersMatchSync(t *testing.T) {
	query := "SELECT OID, IsLeaf, ExcelLine, MIBDesc, MIBName, privilege FROM [MibTree] LIMIT 3000"
	fast := openQueryMode(t, query, true)
	defer fast.Close()
	sync := openQueryMode(t, query, false)
	defer sync.Close()

	for {
		fo, ferr := fast.Next()
		so, serr := sync.Next()
		if ferr != nil || serr != nil {
			t.Fatalf("Next errors: fast=%v sync=%v", ferr, serr)
		}
		if fo != so {
			t.Fatalf("row availability fast=%v sync=%v", fo, so)
		}
		if !fo {
			break
		}
		for c := 0; c < 6; c++ {
			if fast.Value(c) != sync.Value(c) {
				t.Fatalf("Value(%d) fast=%q sync=%q", c, fast.Value(c), sync.Value(c))
			}
			if fast.IsNull(c) != sync.IsNull(c) {
				t.Fatalf("IsNull(%d) fast=%v sync=%v", c, fast.IsNull(c), sync.IsNull(c))
			}
		}
		if fv, ok := fast.Int64Value(1); ok {
			if sv, ok2 := sync.Int64Value(1); !ok2 || fv != sv {
				t.Fatalf("Int64Value(1) fast=%d,%v sync=%d,%v", fv, ok, sv, ok2)
			}
		}
		if fv, ok := fast.Int64Value(2); ok {
			if sv, ok2 := sync.Int64Value(2); !ok2 || fv != sv {
				t.Fatalf("Int64Value(2) fast=%d,%v sync=%d,%v", fv, ok, sv, ok2)
			}
		}
	}
}

func TestFastScanCloseStopsGoroutines(t *testing.T) {
	before := runtime.NumGoroutine()
	for i := 0; i < 3; i++ {
		q := openQueryMode(t, "SELECT * FROM [MibTree] LIMIT 1", true)
		if ok, err := q.Next(); err != nil || !ok {
			t.Fatalf("Next = %v, %v", ok, err)
		}
		q.Close()
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("goroutines leaked: before=%d after=%d", before, runtime.NumGoroutine())
}
