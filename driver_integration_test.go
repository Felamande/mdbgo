package mdbtool

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestDriverReadsCreatedMDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "people.mdb")
	if err := createTestMDB(path); err != nil {
		if errors.Is(err, errMDBProviderUnavailable) {
			t.Skip(err)
		}
		t.Fatal(err)
	}

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

func createTestMDB(path string) error {
	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$path = %s
$providers = @(
  @{ Name = 'Microsoft.ACE.OLEDB.16.0'; Engine = 5 },
  @{ Name = 'Microsoft.ACE.OLEDB.12.0'; Engine = 5 },
  @{ Name = 'Microsoft.Jet.OLEDB.4.0'; Engine = 5 }
)

$lastError = $null
foreach ($provider in $providers) {
  try {
    $catalog = New-Object -ComObject ADOX.Catalog
    $createConnection = 'Provider=' + $provider.Name + ';Data Source=' + $path + ';Jet OLEDB:Engine Type=' + $provider.Engine
    $catalog.Create($createConnection)

    $connection = New-Object -ComObject ADODB.Connection
    $connection.Open('Provider=' + $provider.Name + ';Data Source=' + $path)
    $connection.Execute('CREATE TABLE people (id INTEGER, name TEXT(40), active BIT, nickname TEXT(40), created_at DATETIME)')
    $connection.Execute("INSERT INTO people (id, name, active, nickname, created_at) VALUES (1, 'Ada', TRUE, 'Countess', #2026-05-28#)")
    $connection.Execute("INSERT INTO people (id, name, active, nickname, created_at) VALUES (2, 'Grace', FALSE, NULL, #2026-05-28#)")
    $connection.Close()
    exit 0
  } catch {
    $lastError = $_.Exception.Message
    if (Test-Path -LiteralPath $path) {
      Remove-Item -LiteralPath $path -Force
    }
  }
}

[Console]::Error.WriteLine('No usable Access OLE DB provider found: ' + $lastError)
exit 42
`, psSingleQuote(path))

	cmd := exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", script)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if exitErr := (&exec.ExitError{}); errors.As(err, &exitErr) && exitErr.ExitCode() == 42 {
		return fmt.Errorf("%w: %s", errMDBProviderUnavailable, strings.TrimSpace(string(out)))
	}
	return fmt.Errorf("create mdb: %w: %s", err, strings.TrimSpace(string(out)))
}

func psSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
