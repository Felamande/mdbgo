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
