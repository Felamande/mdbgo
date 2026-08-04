package cmdb

import (
	"context"
	"database/sql"
	"os"
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

func BenchmarkLmWhereOIDIn(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		db := lmDB(b)
		scanAll(b, db, "SELECT OID FROM [MibTree] WHERE OID IN ('2.4.2.2.1.1.1.0.0.2.0','2.4.2.2.1.1.1.0.0.2','2.4.2.2.1.1.1.0.0','2.4.2.2.1.1.1.0','2.4.2.2.1.1.1','2.4.2.2.1.1','2.4.2.2.1','2.4.2.2')")
		db.Close()
	}
}
