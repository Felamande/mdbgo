package gomdb

import (
	"context"
	"database/sql"
	"os"
	"testing"
)

// BenchmarkFinal is the end-to-end driver benchmark: each sub-benchmark opens
// the database, runs a real query through database/sql, scans every value
// into interface{}, and closes the connection — the same path real clients
// exercise. lm.mdb is gitignored, so the large-file cases skip when absent.
func BenchmarkFinal(b *testing.B) {
	lm := "../../testdata/lm.mdb"
	_, err := os.Stat(lm)
	lmPresent := err == nil

	scanAll := func(b *testing.B, db *sql.DB, query string) {
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
		if err := rows.Err(); err != nil {
			b.Fatal(err)
		}
	}

	openRun := func(b *testing.B, path, query string) {
		b.Helper()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			db, err := sql.Open(DriverName, path)
			if err != nil {
				b.Fatal(err)
			}
			scanAll(b, db, query)
			db.Close()
		}
	}

	b.Run("LmSelectAll", func(b *testing.B) {
		if !lmPresent {
			b.Skipf("%s not present", lm)
		}
		openRun(b, lm, "SELECT * FROM [MibTree]")
	})
	b.Run("LmWhere", func(b *testing.B) {
		if !lmPresent {
			b.Skipf("%s not present", lm)
		}
		openRun(b, lm, "SELECT * FROM [MibTree] WHERE CmdID > 10000")
	})
	b.Run("LmProjection", func(b *testing.B) {
		if !lmPresent {
			b.Skipf("%s not present", lm)
		}
		openRun(b, lm, "SELECT OID, MIBName FROM [MibTree]")
	})
	b.Run("SmallSelectAll", func(b *testing.B) {
		openRun(b, "../../testdata/people.mdb", "SELECT id, name, active, nickname, created_at FROM people")
	})
	b.Run("SmallWhere", func(b *testing.B) {
		openRun(b, "../../testdata/people.mdb", "SELECT id, name FROM people WHERE id = 1")
	})
	b.Run("TypedSelectAll", func(b *testing.B) {
		openRun(b, "../../testdata/typed.mdb",
			"SELECT id, flag, val_byte, val_short, val_int, val_long, val_single, val_double, val_currency, val_datetime, val_text, val_memo FROM typed")
	})
	b.Run("ReusedConn", func(b *testing.B) {
		b.ReportAllocs()
		db, err := sql.Open(DriverName, "../../testdata/people.mdb")
		if err != nil {
			b.Fatal(err)
		}
		defer db.Close()
		if err := db.Ping(); err != nil {
			b.Fatal(err)
		}
		for i := 0; i < b.N; i++ {
			scanAll(b, db, "SELECT id, name, active FROM people WHERE id > 0")
		}
	})
}
