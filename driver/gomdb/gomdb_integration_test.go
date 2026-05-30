package gomdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestDriverReadsCreatedMDB(t *testing.T) {
	path := "../../testdata/people.mdb"

	db, err := sql.Open(DriverName, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}

	rows, err := db.QueryContext(context.Background(), `SELECT id, name, active, created_at FROM people WHERE id > ?`, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	colTypes, err := rows.ColumnTypes()
	if err != nil {
		t.Fatal(err)
	}
	if len(colTypes) != 4 {
		t.Fatalf("ColumnTypes len = %d, want 4", len(colTypes))
	}
	if colTypes[0].DatabaseTypeName() == "" {
		t.Fatalf("first column database type is empty")
	}
	if got := colTypes[0].ScanType(); got != reflect.TypeOf(int64(0)) {
		t.Fatalf("id ScanType = %v, want int64", got)
	}
	if got := colTypes[2].ScanType(); got != reflect.TypeOf(false) {
		t.Fatalf("active ScanType = %v, want bool", got)
	}
	if got := colTypes[3].ScanType(); got != reflect.TypeOf(time.Time{}) {
		t.Fatalf("created_at ScanType = %v, want time.Time", got)
	}

	type person struct {
		id        int64
		name      string
		active    bool
		createdAt time.Time
	}
	var got []person
	for rows.Next() {
		var p person
		if err := rows.Scan(&p.id, &p.name, &p.active, &p.createdAt); err != nil {
			t.Fatal(err)
		}
		got = append(got, p)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	sort.Slice(got, func(i, j int) bool { return got[i].id < got[j].id })
	wantCreated := time.Date(2026, 5, 28, 0, 0, 0, 0, got[0].createdAt.Location())
	want := []person{
		{id: 1, name: "Ada", active: true, createdAt: wantCreated},
		{id: 2, name: "Grace", active: false, createdAt: wantCreated},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("rows = %#v, want %#v", got, want)
	}

	stmt, err := db.PrepareContext(context.Background(), `SELECT name FROM people WHERE id = ?`)
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	var preparedName string
	if err := stmt.QueryRowContext(context.Background(), 1).Scan(&preparedName); err != nil {
		t.Fatal(err)
	}
	if preparedName != "Ada" {
		t.Fatalf("preparedName = %q, want Ada", preparedName)
	}

	var nickname sql.NullString
	if err := db.QueryRowContext(context.Background(), `SELECT nickname FROM people WHERE id = ?`, 2).Scan(&nickname); err != nil {
		t.Fatal(err)
	}
	if nickname.Valid {
		t.Fatalf("nickname = %#v, want NULL", nickname)
	}
}

var errMDBProviderUnavailable = errors.New("mdb creation provider unavailable")
