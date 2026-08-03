package gomdb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	backend "github.com/Felamande/mdbgo/internal/gomdb"
)

func TestDriverOpenAndUnsupportedOperations(t *testing.T) {
	if _, err := (&Driver{}).Open(" "); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("Open empty error = %v, want empty path error", err)
	}

	db, err := sql.Open(DriverName, "missing.mdb")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.ExecContext(context.Background(), "CREATE TABLE x (id int)"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("ExecContext error = %v, want read-only error", err)
	}

	if _, err := db.Begin(); err == nil || !strings.Contains(err.Error(), "transactions") {
		t.Fatalf("Begin error = %v, want transactions error", err)
	}

	if _, err := db.PrepareContext(context.Background(), " "); err == nil || !strings.Contains(err.Error(), "empty query") {
		t.Fatalf("Prepare empty error = %v, want empty query error", err)
	}

	if _, err := interpolateQuery("SELECT * FROM [what?] WHERE name = '?' AND id = ?", []driver.NamedValue{}); err == nil || !strings.Contains(err.Error(), "not enough") {
		t.Fatalf("interpolateQuery error = %v, want not enough error", err)
	}
}

func TestInterpolateQuery(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		args    []driver.NamedValue
		want    string
		wantErr string
	}{
		{
			name:  "strings are escaped",
			query: "SELECT * FROM people WHERE id = ? AND name = ? AND literal = '?'",
			args: []driver.NamedValue{
				{Ordinal: 1, Value: int64(7)},
				{Ordinal: 2, Value: "O'Brien"},
			},
			want: "SELECT * FROM people WHERE id = 7 AND name = 'O''Brien' AND literal = '?'",
		},
		{
			name:  "different literal types",
			query: "SELECT * FROM t WHERE a = ? AND b = ? AND c = ? AND d = ? AND e = ?",
			args: []driver.NamedValue{
				{Ordinal: 1, Value: true},
				{Ordinal: 2, Value: false},
				{Ordinal: 3, Value: float64(1.25)},
				{Ordinal: 4, Value: []byte("bytes")},
				{Ordinal: 5, Value: nil},
			},
			want: "SELECT * FROM t WHERE a = 1 AND b = 0 AND c = 1.25 AND d = 'bytes' AND e = NULL",
		},
		{
			name:  "time literal",
			query: "SELECT * FROM t WHERE created_at = ?",
			args: []driver.NamedValue{
				{Ordinal: 1, Value: time.Date(2026, 5, 28, 10, 11, 12, 0, time.UTC)},
			},
			want: "SELECT * FROM t WHERE created_at = strptime('2026-05-28 10:11:12','%Y-%m-%d %H:%M:%S')",
		},
		{
			name:  "bracketed and quoted placeholders ignored",
			query: `SELECT * FROM [what?] WHERE a = '?' AND b = "?" AND c = ?`,
			args:  []driver.NamedValue{{Ordinal: 1, Value: int64(3)}},
			want:  `SELECT * FROM [what?] WHERE a = '?' AND b = "?" AND c = 3`,
		},
		{
			name:    "not enough args",
			query:   "SELECT * FROM t WHERE a = ?",
			wantErr: "not enough",
		},
		{
			name:    "too many args",
			query:   "SELECT * FROM t",
			args:    []driver.NamedValue{{Ordinal: 1, Value: int64(1)}},
			wantErr: "too many",
		},
		{
			name:    "unsupported arg",
			query:   "SELECT * FROM t WHERE a = ?",
			args:    []driver.NamedValue{{Ordinal: 1, Value: struct{}{}}},
			wantErr: "unsupported",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := interpolateQuery(tt.query, tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("interpolateQuery error = %v, want contains %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("query = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPreparedQueryReuse(t *testing.T) {
	db, err := sql.Open(DriverName, "../../testdata/people.mdb")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	stmt, err := db.Prepare("SELECT id, name FROM people WHERE id = ?")
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()

	run := func() []string {
		rows, err := stmt.Query(1)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var id int
			var name string
			if err := rows.Scan(&id, &name); err != nil {
				t.Fatal(err)
			}
			out = append(out, name)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}

	first := run()
	second := run()
	if len(first) == 0 || len(first) != len(second) {
		t.Fatalf("prepared results differ: %v vs %v", first, second)
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("prepared row %d differs: %q vs %q", i, first[i], second[i])
		}
	}
}

func TestCountPlaceholders(t *testing.T) {
	query := `SELECT * FROM [what?] WHERE a = '?' AND b = "?" AND c = ? AND d = ?`
	if got := countPlaceholders(query); got != 2 {
		t.Fatalf("countPlaceholders = %d, want 2", got)
	}
}

func TestStmtBasics(t *testing.T) {
	conn := &Conn{path: "missing.mdb"}
	stmt, err := conn.Prepare("SELECT * FROM people WHERE id = ?")
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()

	if got := stmt.NumInput(); got != 1 {
		t.Fatalf("NumInput = %d, want 1", got)
	}
	if _, err := stmt.Exec([]driver.Value{int64(1)}); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("Stmt Exec error = %v, want read-only", err)
	}
	if _, err := stmt.(driver.StmtExecContext).ExecContext(context.Background(), []driver.NamedValue{{Ordinal: 1, Value: int64(1)}}); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("Stmt ExecContext error = %v, want read-only", err)
	}
}

func TestRowsWithoutHandle(t *testing.T) {
	rows := &Rows{closed: true}
	if err := rows.Next(make([]driver.Value, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("closed Rows Next = %v, want EOF", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("closed Rows Close = %v", err)
	}
}

func TestColumnTypeHelpers(t *testing.T) {
	rows := &Rows{info: []backend.Column{
		{Name: "id", DatabaseType: "INTEGER", Type: backend.TypeLongInt},
		{Name: "name", DatabaseType: "Text", Type: backend.TypeText, Size: 40},
		{Name: "created", DatabaseType: "DateTime", Type: backend.TypeDateTime},
		{Name: "payload", DatabaseType: "Binary", Type: backend.TypeBinary, Size: 8},
	}}
	if got := rows.ColumnTypeDatabaseTypeName(1); got != "Text" {
		t.Fatalf("ColumnTypeDatabaseTypeName = %q, want Text", got)
	}
	if got, ok := rows.ColumnTypeLength(1); !ok || got != 40 {
		t.Fatalf("ColumnTypeLength text = %d,%v, want 40,true", got, ok)
	}
	if _, ok := rows.ColumnTypeLength(0); ok {
		t.Fatalf("ColumnTypeLength integer ok = true, want false")
	}
	if got := rows.ColumnTypeScanType(0); got != reflect.TypeOf(int64(0)) {
		t.Fatalf("scan type id = %v", got)
	}
	if got := rows.ColumnTypeScanType(2); got != reflect.TypeOf(time.Time{}) {
		t.Fatalf("scan type datetime = %v", got)
	}
	if got := rows.ColumnTypeScanType(3); got != reflect.TypeOf([]byte{}) {
		t.Fatalf("scan type binary = %v", got)
	}
	if nullable, ok := rows.ColumnTypeNullable(0); !nullable || ok {
		t.Fatalf("nullable = %v,%v, want true,false", nullable, ok)
	}
}
