//go:build windows

package odbcbench

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"

	_ "github.com/alexbrainman/odbc"
)

// odbcDsn builds a Microsoft Access ODBC connection string. The pwd
// parameter is the database password (the driver ignores empty passwords for
// unprotected databases).
func odbcDsn(filename string, pwd string) string {
	return fmt.Sprintf("DRIVER={Microsoft Access Driver (*.mdb)};DBQ=%s;pwd=%s", filename, pwd)
}

const (
	lmPath = "../../testdata/lm.mdb"
	lmPwd  = "(/1ac2"
)

func lmDB(b *testing.B) *sql.DB {
	b.Helper()
	if _, err := os.Stat(lmPath); err != nil {
		b.Skipf("%s not present", lmPath)
	}
	db, err := sql.Open("odbc", odbcDsn(lmPath, lmPwd))
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

// BenchmarkOdbcLmSelectAll mirrors the gomdb/cmdb lm_bench_test.go harness:
// open, scan every column of every row into interface{}, close.
func BenchmarkOdbcLmSelectAll(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		db := lmDB(b)
		scanAll(b, db, "SELECT * FROM [MibTree]")
		db.Close()
	}
}

// BenchmarkOdbcLmWhere is the same harness with the ConsistentFileInfo sarg
// query (the original CmdID column does not exist in MibTree, so that
// predicate was a no-op in the pure-Go and cgo drivers).
func BenchmarkOdbcLmWhere(b *testing.B) {
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

// BenchmarkOdbcLmWhereParallel runs the same filtered scan concurrently:
// every parallel worker opens its own ODBC connection per iteration, exactly
// like BenchmarkOdbcLmWhere but with RunParallel.
func BenchmarkOdbcLmWhereParallel(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			db := lmDB(b)
			scanAll(b, db, "SELECT * FROM [MibTree] WHERE ConsistentFileInfo > 0")
			db.Close()
		}
	})
}

func BenchmarkLmWhereOIDIn(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		db := lmDB(b)
		scanAll(b, db, "SELECT OID FROM [MibTree] WHERE OID IN ('2.4.2.2.1.1.1.0.0.2.0','2.4.2.2.1.1.1.0.0.2','2.4.2.2.1.1.1.0.0','2.4.2.2.1.1.1.0','2.4.2.2.1.1.1','2.4.2.2.1.1','2.4.2.2.1','2.4.2.2')")
		db.Close()
	}
}

func BenchmarkLmOrderByTop(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		db := lmDB(b)
		scanAll(b, db, "SELECT TOP 1 * FROM [MibTree] WHERE OID IN ('2.4.2.2.1.1.1.0.0.2.0','2.4.2.2.1.1.1.0.0.2','2.4.2.2.1.1.1.0.0','2.4.2.2.1.1.1.0','2.4.2.2.1.1.1','2.4.2.2.1.1','2.4.2.2.1','2.4.2.2') ORDER BY Len(OID) DESC")
		db.Close()
	}
}
