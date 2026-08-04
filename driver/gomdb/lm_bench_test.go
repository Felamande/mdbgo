package gomdb

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
)

// Benchmarks against lm.mdb, the large (27k-row) workload used to compare
// the pure-Go driver with the cgo driver. Skipped when the file is absent;
// lm.mdb is gitignored.
const lmPath = "../../testdata/lm.mdb"

func lmDB(b *testing.B) *sql.DB {
	b.Helper()
	if _, err := os.Stat(lmPath); err != nil {
		b.Skipf("%s not present", lmPath)
	}
	db, err := sql.Open(DriverName, lmPath)
	if err != nil {
		b.Fatal(err)
	}
	return db
}

func scanAll(b *testing.B, db *sql.DB, query string) {
	b.Helper()
	rows, err := db.QueryContext(context.Background(), query)
	if err != nil {
		b.Fatal(err)
	}
	cols, _ := rows.Columns()
	buf := make([]interface{}, len(cols))
	for i := range cols {
		buf[i] = new(interface{})
	}
	for rows.Next() {
		if err := rows.Scan(buf...); err != nil {
			b.Fatal(err)
		}
	}
	rows.Close()
}

func BenchmarkLmSelectAll(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		db := lmDB(b)
		scanAll(b, db, "SELECT * FROM [MibTree]")
		db.Close()
	}
}

func BenchmarkLmWhere(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		db := lmDB(b)
		scanAll(b, db, "SELECT * FROM [MibTree] WHERE ConsistentFileInfo > 0")
		db.Close()
	}
}

func BenchmarkLmWhereOID(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		db := lmDB(b)
		scanAll(b, db, "SELECT * FROM [MibTree] WHERE OID = '2.7.24.18.1.3'")
		db.Close()
	}
}

// BenchmarkLmWhereOIDIn measures the IN predicate on the fast-scan path; the
// typed element lists are precomputed, so per-row evaluation is allocation-free.
func BenchmarkLmWhereOIDIn(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		db := lmDB(b)
		scanAll(b, db, "SELECT OID FROM [MibTree] WHERE OID IN ('2.4.2.2.1.1.1.0.0.2.0','2.4.2.2.1.1.1.0.0.2','2.4.2.2.1.1.1.0.0','2.4.2.2.1.1.1.0','2.4.2.2.1.1.1','2.4.2.2.1.1','2.4.2.2.1','2.4.2.2') ")
		db.Close()
	}
}

// BenchmarkLmOrderByTop exercises the full example: IN + TOP 1 + ORDER BY
// Len(OID) DESC, which materializes and sorts the matched rows.
func BenchmarkLmOrderByTop(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		db := lmDB(b)
		scanAll(b, db, "SELECT TOP 1 * FROM [MibTree] WHERE OID IN ('2.4.2.2.1.1.1.0.0.2.0','2.4.2.2.1.1.1.0.0.2','2.4.2.2.1.1.1.0.0','2.4.2.2.1.1.1.0','2.4.2.2.1.1.1','2.4.2.2.1.1','2.4.2.2.1','2.4.2.2') ORDER BY Len(OID) DESC")
		db.Close()
	}
}

// BenchmarkLmOIDLongestFallback is the alternative lookup strategy used
// before IN support: walk the OID prefixes from longest to shortest with a
// point query each and return the first match. With lm.mdb this misses on
// the four longest prefixes and hits on 2.4.2.2.1.1.1, so every lookup pays
// five table scans. Compare against BenchmarkLmOrderByTop, which finds the
// same row with a single materialized scan.
func BenchmarkLmOIDLongestFallback(b *testing.B) {
	b.ReportAllocs()
	const oid = "2.4.2.2.1.1.1.0.0.2.0"
	oidParts := strings.Split(oid, ".")
	for i := 0; i < b.N; i++ {
		db := lmDB(b)
		// No cache: every iteration walks prefixes long-to-short, matching
		// the cost of a cache miss in the application-level loop.
		for j := len(oidParts); j > 0; j-- {
			oidJoin := strings.Join(oidParts[:j], ".")
			rows, err := db.QueryContext(context.Background(), "SELECT * FROM [MibTree] WHERE OID = ? LIMIT 1", oidJoin)
			if err != nil {
				b.Fatal(err)
			}
			cols, _ := rows.Columns()
			buf := make([]interface{}, len(cols))
			for k := range cols {
				buf[k] = new(interface{})
			}
			found := false
			for rows.Next() {
				if err := rows.Scan(buf...); err != nil {
					b.Fatal(err)
				}
				found = true
				break
			}
			rows.Close()
			if found {
				break
			}
		}
		db.Close()
	}
}

// BenchmarkLmWhereParallel runs the same filtered scan concurrently: every
// parallel worker opens its own connection per iteration, exactly like
// BenchmarkQueryParallel in gomdb_bench_test.go.
func BenchmarkLmWhereParallel(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			db := lmDB(b)
			scanAll(b, db, "SELECT * FROM [MibTree] WHERE ConsistentFileInfo > 0")
			db.Close()
		}
	})
}
