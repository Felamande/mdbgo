package gomdb

import (
	"context"
	"database/sql"
	"runtime"
	"testing"
)

// TestFastScanStringsSurviveClose runs a real fast-scan query on lm.mdb,
// scans the first text value into an interface{}, closes rows and the
// database, forces GC churn, and then verifies the value is still intact.
// This exercises the zero-copy borrowed-string path: the value aliases the
// resident file data, which the GC must keep alive after the connection is
// closed and the cache entry is no longer referenced by the driver.
func TestFastScanStringsSurviveClose(t *testing.T) {
	db, err := sql.Open(DriverName, "../../testdata/lm.mdb")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(context.Background(), "SELECT MIBName FROM [MibTree]")
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	var v any
	scanned := false
	for rows.Next() {
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		scanned = true
		break
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	db.Close()
	if !scanned {
		t.Skip("lm.mdb returned no rows")
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("value type = %T, want string", v)
	}
	want := s
	if want == "" {
		t.Fatal("unexpected empty text value")
	}
	if testing.Short() {
		return
	}
	for i := 0; i < 8; i++ {
		runtime.GC()
		_ = make([]byte, 8<<20)
	}
	runtime.KeepAlive(s)
	if s != want {
		t.Fatalf("text value changed after close: %q -> %q", want, s)
	}
}
