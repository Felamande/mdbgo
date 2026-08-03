package gomdb

import (
	"context"
	"database/sql"
	"testing"
)

// sharedBenchTables lists the test databases and representative queries.
var benchTables = []struct {
	label string
	path  string
	query string
}{
	{"people/select_all", "../../testdata/people.mdb", "SELECT id, name, active, nickname, created_at FROM people"},
	{"people/where_eq", "../../testdata/people.mdb", "SELECT id, name FROM people WHERE id = 1"},
	{"people/where_gt", "../../testdata/people.mdb", "SELECT id, name, active FROM people WHERE id > 0"},
	{"typed/select_all", "../../testdata/typed.mdb", "SELECT id, flag, val_byte, val_short, val_int, val_long, val_single, val_double, val_currency, val_datetime, val_text, val_memo FROM typed"},
	{"typed/where_eq", "../../testdata/typed.mdb", "SELECT id, val_text FROM typed WHERE id = 1"},
	{"typed/where_like", "../../testdata/typed.mdb", "SELECT id, val_text FROM typed WHERE val_text LIKE 'hello%'"},
	{"nulltest/select_all", "../../testdata/nulltest.mdb", "SELECT id, val_int, val_text, val_dt, val_double, val_bool FROM nulltest"},
	{"chinese/select_all", "../../testdata/chinese.mdb", "SELECT id, name, description, score FROM chinese"},
	{"chinese/where_eq", "../../testdata/chinese.mdb", "SELECT id, name FROM chinese WHERE id = 1"},
}

// --- Open + Ping ---

func BenchmarkOpenPing(b *testing.B) {
	for i := 0; i < b.N; i++ {
		db, err := sql.Open(DriverName, "../../testdata/people.mdb")
		if err != nil {
			b.Fatal(err)
		}
		if err := db.PingContext(context.Background()); err != nil {
			db.Close()
			b.Fatal(err)
		}
		db.Close()
	}
}

// --- Query + full scan ---

func BenchmarkQueryAndScan(b *testing.B) {
	for _, bt := range benchTables {
		b.Run(bt.label, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				db, err := sql.Open(DriverName, bt.path)
				if err != nil {
					b.Fatal(err)
				}
				rows, err := db.QueryContext(context.Background(), bt.query)
				if err != nil {
					db.Close()
					b.Fatal(err)
				}
				cols, _ := rows.Columns()
				buf := make([]interface{}, len(cols))
				for i := range cols {
					buf[i] = new(interface{})
				}
				for rows.Next() {
					rows.Scan(buf...)
				}
				rows.Close()
				db.Close()
			}
		})
	}
}

// --- Parallel queries ---

func BenchmarkQueryParallel(b *testing.B) {
	for _, bt := range benchTables {
		b.Run(bt.label, func(b *testing.B) {
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					db, err := sql.Open(DriverName, bt.path)
					if err != nil {
						b.Fatal(err)
					}
					rows, err := db.QueryContext(context.Background(), bt.query)
					if err != nil {
						db.Close()
						b.Fatal(err)
					}
					cols, _ := rows.Columns()
					buf := make([]interface{}, len(cols))
					for i := range cols {
						buf[i] = new(interface{})
					}
					for rows.Next() {
						rows.Scan(buf...)
					}
					rows.Close()
					db.Close()
				}
			})
		})
	}
}

// --- LIST TABLES ---

func BenchmarkListTables(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		db, err := sql.Open(DriverName, "../../testdata/people.mdb")
		if err != nil {
			b.Fatal(err)
		}
		rows, err := db.QueryContext(context.Background(), "LIST TABLES")
		if err != nil {
			db.Close()
			b.Fatal(err)
		}
		for rows.Next() {
			var name string
			rows.Scan(&name)
		}
		rows.Close()
		db.Close()
	}
}

// --- DESCRIBE TABLE ---

func BenchmarkDescribeTable(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		db, err := sql.Open(DriverName, "../../testdata/typed.mdb")
		if err != nil {
			b.Fatal(err)
		}
		rows, err := db.QueryContext(context.Background(), "DESCRIBE TABLE typed")
		if err != nil {
			db.Close()
			b.Fatal(err)
		}
		for rows.Next() {
			var colName, colType, colSize string
			rows.Scan(&colName, &colType, &colSize)
		}
		rows.Close()
		db.Close()
	}
}

// --- Date handling ---

func BenchmarkDateQuery(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		db, err := sql.Open(DriverName, "../../testdata/typed.mdb")
		if err != nil {
			b.Fatal(err)
		}
		rows, err := db.QueryContext(context.Background(), "SELECT id, val_datetime FROM typed")
		if err != nil {
			db.Close()
			b.Fatal(err)
		}
		for rows.Next() {
			var id int64
			var dt interface{}
			rows.Scan(&id, &dt)
		}
		rows.Close()
		db.Close()
	}
}

// --- Column metadata ---

func BenchmarkColumnTypes(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		db, err := sql.Open(DriverName, "../../testdata/typed.mdb")
		if err != nil {
			b.Fatal(err)
		}
		rows, err := db.QueryContext(context.Background(), "SELECT id, flag, val_byte, val_short, val_int, val_long, val_single, val_double, val_currency, val_datetime, val_text, val_memo FROM typed WHERE id = 1")
		if err != nil {
			db.Close()
			b.Fatal(err)
		}
		types, _ := rows.ColumnTypes()
		for _, ct := range types {
			_ = ct.DatabaseTypeName()
			_ = ct.ScanType()
			ct.Length()
			ct.Nullable()
		}
		rows.Close()
		db.Close()
	}
}

// BenchmarkQueryReusedConn measures repeated queries on a single live
// connection, where prepared plans are cached and re-executed.
func BenchmarkQueryReusedConn(b *testing.B) {
	db, err := sql.Open(DriverName, "../../testdata/people.mdb")
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		rows, err := db.Query("SELECT id, name, active FROM people WHERE id > 0")
		if err != nil {
			b.Fatal(err)
		}
		for rows.Next() {
			var id int
			var name string
			var active bool
			if err := rows.Scan(&id, &name, &active); err != nil {
				b.Fatal(err)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			b.Fatal(err)
		}
	}
}
