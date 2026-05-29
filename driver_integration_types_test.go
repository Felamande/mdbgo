package mdbtool

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Felamande/mdbgo/internal/cmdb"
)

// createTypedTestMDB creates an MDB with a table covering all Access column types.
// Access DDL type → mdbtools DatabaseType mapping:
//
//	BYTE       → "Byte"          (unsigned 0-255)
//	SHORT      → "Integer"       (signed 16-bit)
//	INTEGER    → "Long Integer"  (signed 32-bit)
//	LONG       → "Long Integer"  (signed 32-bit)
//	SINGLE     → "Single"        (float32)
//	DOUBLE     → "Double"        (float64)
//	CURRENCY   → "Currency"      (fixed-point 4 decimals)
//	BIT        → "Boolean"       (0/1, no NULL support)
//	DATETIME   → "DateTime"
//	TEXT(n)    → "Text"          (Unicode, length reported as 2*n bytes)
//	MEMO       → "Memo/Hyperlink"
func createTypedTestMDB(path string) error {
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
    $connection.Execute('CREATE TABLE typed (id LONG, flag BIT, val_byte BYTE, val_short SHORT, val_int INTEGER, val_long LONG, val_single SINGLE, val_double DOUBLE, val_currency CURRENCY, val_datetime DATETIME, val_text TEXT(50), val_memo MEMO)')
    $connection.Execute("INSERT INTO typed (id, flag, val_byte, val_short, val_int, val_long, val_single, val_double, val_currency, val_datetime, val_text, val_memo) VALUES (1, TRUE, 127, 32000, 1000000, 1000000, 1.5, 3.14159265358979, 1234.5678, #2026-01-15 08:30:00#, 'hello world', 'memo content here')")
    $connection.Execute("INSERT INTO typed (id, flag, val_byte, val_short, val_int, val_long, val_single, val_double, val_currency, val_datetime, val_text, val_memo) VALUES (2, FALSE, 255, -32000, -1000000, -1000000, -2.75, -0.001, -99.9900, #1999-12-31 23:59:59#, 'special chars !@#$%%^&*()', 'line1' & Chr(10) & 'line2')")
    $connection.Execute("INSERT INTO typed (id, flag, val_byte, val_short, val_int, val_long, val_single, val_double, val_currency, val_datetime, val_text, val_memo) VALUES (3, TRUE, 0, 0, 0, 0, 0, 0, 0, #1899-12-30 00:00:00#, '', '')")
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

// createChineseTestMDB creates an MDB with Chinese/multibyte characters using
// ADODB Recordset AddNew to preserve Unicode strings. The PowerShell script is
// written to a temp file to avoid Go string escaping issues with Unicode.
func createChineseTestMDB(path string) error {
	// Build the PowerShell script with Unicode char codes to avoid encoding issues.
	script := fmt.Sprintf(`$ErrorActionPreference = 'Stop'
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
    $catalog.Create('Provider=' + $provider.Name + ';Data Source=' + $path + ';Jet OLEDB:Engine Type=' + $provider.Engine)

    $conn = New-Object -ComObject ADODB.Connection
    $conn.Open('Provider=' + $provider.Name + ';Data Source=' + $path)
    $conn.Execute('CREATE TABLE chinese (id INTEGER, name TEXT(100), description MEMO, score DOUBLE)')

    $rs = New-Object -ComObject ADODB.Recordset
    $rs.Open('chinese', $conn, 3, 3, 2)

    $rs.AddNew()
    $rs.Fields.Item('id').Value = 1
    $rs.Fields.Item('name').Value = [string]::Concat([char]0x5F20, [char]0x4E09)
    $rs.Fields.Item('description').Value = [string]::Concat([char]0x8FD9, [char]0x662F, [char]0x4E00, [char]0x4E2A, [char]0x4E2D, [char]0x6587, [char]0x63CF, [char]0x8FF0)
    $rs.Fields.Item('score').Value = 95.5
    $rs.Update()

    $rs.AddNew()
    $rs.Fields.Item('id').Value = 2
    $rs.Fields.Item('name').Value = [string]::Concat([char]0x674E, [char]0x56DB)
    $rs.Fields.Item('description').Value = [string]::Concat([char]0x7B2C, [char]0x4E8C, [char]0x6761, [char]0x8BB0, [char]0x5F55, [char]0xFF0C, [char]0x5305, [char]0x542B, [char]0x6807, [char]0x70B9, [char]0x7B26, [char]0x53F7, [char]0xFF01, [char]0x0040, [char]0x0023)
    $rs.Fields.Item('score').Value = 88.0
    $rs.Update()

    $rs.AddNew()
    $rs.Fields.Item('id').Value = 3
    $rs.Fields.Item('name').Value = [string]::Concat([char]0x738B, [char]0x4E94)
    $rs.Fields.Item('description').Value = ''
    $rs.Fields.Item('score').Value = 0
    $rs.Update()

    $rs.AddNew()
    $rs.Fields.Item('id').Value = 4
    $rs.Fields.Item('name').Value = [string]::Concat([char]0x6DF7, [char]0x5408, 'Mixed', [char]0x4E2D, 'English', [char]0x6587)
    $rs.Fields.Item('description').Value = [string]::Concat([char]0x65E5, [char]0x672C, [char]0x8A9E, [char]0x30C6, [char]0x30B9, [char]0x30C8, ' ', [char]0xD55C, [char]0xAD6D, [char]0xC5B4, ' ', [char]0x0627, [char]0x0644, [char]0x0639, [char]0x0631, [char]0x0628, [char]0x064A, [char]0x0629)
    $rs.Fields.Item('score').Value = 77.7
    $rs.Update()

    $rs.Close()
    $conn.Close()
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

	// Write script to a temp file to avoid encoding issues with -Command.
	scriptFile, err := os.CreateTemp("", "mdbtest*.ps1")
	if err != nil {
		return fmt.Errorf("create temp script: %w", err)
	}
	scriptPath := scriptFile.Name()
	scriptFile.Write([]byte{0xEF, 0xBB, 0xBF}) // UTF-8 BOM
	scriptFile.WriteString(script)
	scriptFile.Close()
	defer os.Remove(scriptPath)

	cmd := exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", scriptPath)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if exitErr := (&exec.ExitError{}); errors.As(err, &exitErr) && exitErr.ExitCode() == 42 {
		return fmt.Errorf("%w: %s", errMDBProviderUnavailable, strings.TrimSpace(string(out)))
	}
	return fmt.Errorf("create mdb: %w: %s", err, strings.TrimSpace(string(out)))
}


// createNullTestMDB creates an MDB with nullable and non-nullable columns.
// Note: Access BIT fields do not support NULL — NULL inserts become FALSE.
func createNullTestMDB(path string) error {
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
    $connection.Execute('CREATE TABLE nulltest (id INTEGER, val_int INTEGER, val_text TEXT(50), val_dt DATETIME, val_double DOUBLE, val_bool BIT)')
    $connection.Execute("INSERT INTO nulltest (id, val_int, val_text, val_dt, val_double, val_bool) VALUES (1, NULL, NULL, NULL, NULL, FALSE)")
    $connection.Execute("INSERT INTO nulltest (id, val_int, val_text, val_dt, val_double, val_bool) VALUES (2, 42, 'not null', #2026-06-01#, 99.9, TRUE)")
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

func TestColumnTypeExactMatch(t *testing.T) {
	path := t.TempDir() + "/typed.mdb"
	if err := createTypedTestMDB(path); err != nil {
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

	rows, err := db.QueryContext(context.Background(),
		`SELECT id, flag, val_byte, val_short, val_int, val_long, val_single, val_double, val_currency, val_datetime, val_text, val_memo FROM typed WHERE id = 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	colTypes, err := rows.ColumnTypes()
	if err != nil {
		t.Fatal(err)
	}

	type colExpect struct {
		dbType   string
		scanType reflect.Type
	}
	want := []colExpect{
		{"Long Integer", reflect.TypeOf(int64(0))},    // id LONG → Long Integer
		{"Boolean", reflect.TypeOf(false)},             // flag BIT → Boolean
		{"Byte", reflect.TypeOf(int64(0))},             // val_byte BYTE → Byte
		{"Integer", reflect.TypeOf(int64(0))},          // val_short SHORT → Integer (16-bit)
		{"Long Integer", reflect.TypeOf(int64(0))},     // val_int INTEGER → Long Integer (32-bit)
		{"Long Integer", reflect.TypeOf(int64(0))},     // val_long LONG → Long Integer
		{"Single", reflect.TypeOf(float64(0))},         // val_single SINGLE → Single
		{"Double", reflect.TypeOf(float64(0))},         // val_double DOUBLE → Double
		{"Currency", reflect.TypeOf(float64(0))},       // val_currency CURRENCY → Currency
		{"DateTime", reflect.TypeOf(time.Time{})},      // val_datetime DATETIME → DateTime
		{"Text", reflect.TypeOf("")},                   // val_text TEXT → Text
		{"Memo/Hyperlink", reflect.TypeOf("")},         // val_memo MEMO → Memo/Hyperlink
	}

	if len(colTypes) != len(want) {
		t.Fatalf("ColumnTypes len = %d, want %d", len(colTypes), len(want))
	}

	for i, w := range want {
		gotDBType := colTypes[i].DatabaseTypeName()
		gotScanType := colTypes[i].ScanType()
		if gotDBType != w.dbType {
			t.Errorf("col[%d] DatabaseTypeName = %q, want %q", i, gotDBType, w.dbType)
		}
		if gotScanType != w.scanType {
			t.Errorf("col[%d] ScanType = %v, want %v", i, gotScanType, w.scanType)
		}
	}
}

func TestTypedValuesExactMatch(t *testing.T) {
	path := t.TempDir() + "/typed.mdb"
	if err := createTypedTestMDB(path); err != nil {
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

	tests := []struct {
		name    string
		query   string
		checkFn func(t *testing.T, rows *sql.Rows)
	}{
		{
			name:  "row 1 exact values",
			query: `SELECT id, flag, val_byte, val_short, val_int, val_long, val_single, val_double, val_currency, val_datetime, val_text, val_memo FROM typed WHERE id = 1`,
			checkFn: func(t *testing.T, rows *sql.Rows) {
				if !rows.Next() {
					t.Fatal("no rows returned")
				}
				var (
					id          int64
					flag        bool
					valByte     int64
					valShort    int64
					valInt      int64
					valLong     int64
					valSingle   float64
					valDouble   float64
					valCurrency float64
					valDatetime time.Time
					valText     string
					valMemo     string
				)
				if err := rows.Scan(&id, &flag, &valByte, &valShort, &valInt, &valLong, &valSingle, &valDouble, &valCurrency, &valDatetime, &valText, &valMemo); err != nil {
					t.Fatal(err)
				}
				if id != 1 {
					t.Errorf("id = %d, want 1", id)
				}
				if flag != true {
					t.Errorf("flag = %v, want true", flag)
				}
				if valByte != 127 {
					t.Errorf("val_byte = %d, want 127", valByte)
				}
				if valShort != 32000 {
					t.Errorf("val_short = %d, want 32000", valShort)
				}
				if valInt != 1000000 {
					t.Errorf("val_int = %d, want 1000000", valInt)
				}
				if valLong != 1000000 {
					t.Errorf("val_long = %d, want 1000000", valLong)
				}
				if valSingle != 1.5 {
					t.Errorf("val_single = %v, want 1.5", valSingle)
				}
				if diff := valDouble - 3.14159265358979; diff > 1e-10 || diff < -1e-10 {
					t.Errorf("val_double = %.15f, want 3.14159265358979", valDouble)
				}
				// CURRENCY is fixed-point with 4 decimal places
				if valCurrency < 1234.5677 || valCurrency > 1234.5679 {
					t.Errorf("val_currency = %v, want ~1234.5678", valCurrency)
				}
				wantDT := time.Date(2026, 1, 15, 8, 30, 0, 0, valDatetime.Location())
				if !valDatetime.Equal(wantDT) {
					t.Errorf("val_datetime = %v, want %v", valDatetime, wantDT)
				}
				if valText != "hello world" {
					t.Errorf("val_text = %q, want %q", valText, "hello world")
				}
				if valMemo != "memo content here" {
					t.Errorf("val_memo = %q, want %q", valMemo, "memo content here")
				}
				if rows.Next() {
					t.Error("unexpected additional row")
				}
			},
		},
		{
			name:  "row 2 negative and special values",
			query: `SELECT id, flag, val_byte, val_short, val_int, val_long, val_single, val_double, val_currency, val_datetime, val_text, val_memo FROM typed WHERE id = 2`,
			checkFn: func(t *testing.T, rows *sql.Rows) {
				if !rows.Next() {
					t.Fatal("no rows returned")
				}
				var (
					id          int64
					flag        bool
					valByte     int64
					valShort    int64
					valInt      int64
					valLong     int64
					valSingle   float64
					valDouble   float64
					valCurrency float64
					valDatetime time.Time
					valText     string
					valMemo     string
				)
				if err := rows.Scan(&id, &flag, &valByte, &valShort, &valInt, &valLong, &valSingle, &valDouble, &valCurrency, &valDatetime, &valText, &valMemo); err != nil {
					t.Fatal(err)
				}
				if id != 2 {
					t.Errorf("id = %d, want 2", id)
				}
				if flag != false {
					t.Errorf("flag = %v, want false", flag)
				}
				// BYTE is unsigned 0-255
				if valByte != 255 {
					t.Errorf("val_byte = %d, want 255", valByte)
				}
				if valShort != -32000 {
					t.Errorf("val_short = %d, want -32000", valShort)
				}
				if valInt != -1000000 {
					t.Errorf("val_int = %d, want -1000000", valInt)
				}
				if valLong != -1000000 {
					t.Errorf("val_long = %d, want -1000000", valLong)
				}
				if valSingle != -2.75 {
					t.Errorf("val_single = %v, want -2.75", valSingle)
				}
				if diff := valDouble - (-0.001); diff > 1e-10 || diff < -1e-10 {
					t.Errorf("val_double = %v, want -0.001", valDouble)
				}
				if valCurrency < -99.9901 || valCurrency > -99.9899 {
					t.Errorf("val_currency = %v, want ~-99.99", valCurrency)
				}
				wantDT := time.Date(1999, 12, 31, 23, 59, 59, 0, valDatetime.Location())
				if !valDatetime.Equal(wantDT) {
					t.Errorf("val_datetime = %v, want %v", valDatetime, wantDT)
				}
				if valText != "special chars !@#$%^&*()" {
					t.Errorf("val_text = %q, want %q", valText, "special chars !@#$%^&*()")
				}
				// memo contains embedded newline
				if !strings.Contains(valMemo, "line1") || !strings.Contains(valMemo, "line2") {
					t.Errorf("val_memo = %q, want lines containing 'line1' and 'line2'", valMemo)
				}
			},
		},
		{
			name:  "row 3 zero values",
			query: `SELECT id, flag, val_byte, val_short, val_int, val_long, val_single, val_double, val_currency, val_datetime, val_text, val_memo FROM typed WHERE id = 3`,
			checkFn: func(t *testing.T, rows *sql.Rows) {
				if !rows.Next() {
					t.Fatal("no rows returned")
				}
				var (
					id          int64
					flag        bool
					valByte     int64
					valShort    int64
					valInt      int64
					valLong     int64
					valSingle   float64
					valDouble   float64
					valCurrency float64
					valDatetime time.Time
					valText     string
					valMemo     string
				)
				if err := rows.Scan(&id, &flag, &valByte, &valShort, &valInt, &valLong, &valSingle, &valDouble, &valCurrency, &valDatetime, &valText, &valMemo); err != nil {
					t.Fatal(err)
				}
				if id != 3 {
					t.Errorf("id = %d, want 3", id)
				}
				if flag != true {
					t.Errorf("flag = %v, want true", flag)
				}
				if valByte != 0 {
					t.Errorf("val_byte = %d, want 0", valByte)
				}
				if valShort != 0 {
					t.Errorf("val_short = %d, want 0", valShort)
				}
				if valInt != 0 {
					t.Errorf("val_int = %d, want 0", valInt)
				}
				if valLong != 0 {
					t.Errorf("val_long = %d, want 0", valLong)
				}
				if valSingle != 0 {
					t.Errorf("val_single = %v, want 0", valSingle)
				}
				if valDouble != 0 {
					t.Errorf("val_double = %v, want 0", valDouble)
				}
				if valCurrency != 0 {
					t.Errorf("val_currency = %v, want 0", valCurrency)
				}
				if valText != "" {
					t.Errorf("val_text = %q, want empty", valText)
				}
				if valMemo != "" {
					t.Errorf("val_memo = %q, want empty", valMemo)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, err := db.QueryContext(context.Background(), tt.query)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			tt.checkFn(t, rows)
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNullValuesAllColumns(t *testing.T) {
	path := t.TempDir() + "/nulltest.mdb"
	if err := createNullTestMDB(path); err != nil {
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

	t.Run("all NULLs", func(t *testing.T) {
		rows, err := db.QueryContext(context.Background(),
			`SELECT val_int, val_text, val_dt, val_double FROM nulltest WHERE id = 1`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()

		if !rows.Next() {
			t.Fatal("no rows returned")
		}

		var valInt sql.NullInt64
		var valText sql.NullString
		var valDT sql.NullTime
		var valDouble sql.NullFloat64

		if err := rows.Scan(&valInt, &valText, &valDT, &valDouble); err != nil {
			t.Fatal(err)
		}

		if valInt.Valid {
			t.Errorf("val_int = %v, want NULL", valInt)
		}
		if valText.Valid {
			t.Errorf("val_text = %v, want NULL", valText)
		}
		if valDT.Valid {
			t.Errorf("val_dt = %v, want NULL", valDT)
		}
		if valDouble.Valid {
			t.Errorf("val_double = %v, want NULL", valDouble)
		}
	})

	t.Run("BIT column does not support NULL", func(t *testing.T) {
		// Access BIT fields do not support NULL; NULL inserts become FALSE.
		// This test documents that behavior.
		rows, err := db.QueryContext(context.Background(),
			`SELECT val_bool FROM nulltest WHERE id = 1`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()

		if !rows.Next() {
			t.Fatal("no rows returned")
		}
		var valBool bool
		if err := rows.Scan(&valBool); err != nil {
			t.Fatal(err)
		}
		if valBool != false {
			t.Errorf("val_bool = %v, want false (BIT NULL coerced to FALSE)", valBool)
		}
	})

	t.Run("all non-NULLs", func(t *testing.T) {
		rows, err := db.QueryContext(context.Background(),
			`SELECT val_int, val_text, val_dt, val_double, val_bool FROM nulltest WHERE id = 2`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()

		if !rows.Next() {
			t.Fatal("no rows returned")
		}

		var valInt sql.NullInt64
		var valText sql.NullString
		var valDT sql.NullTime
		var valDouble sql.NullFloat64
		var valBool bool

		if err := rows.Scan(&valInt, &valText, &valDT, &valDouble, &valBool); err != nil {
			t.Fatal(err)
		}

		if !valInt.Valid || valInt.Int64 != 42 {
			t.Errorf("val_int = %v, want 42", valInt)
		}
		if !valText.Valid || valText.String != "not null" {
			t.Errorf("val_text = %v, want 'not null'", valText)
		}
		if !valDT.Valid {
			t.Errorf("val_dt is NULL, want non-NULL")
		} else {
			wantDT := time.Date(2026, 6, 1, 0, 0, 0, 0, valDT.Time.Location())
			if !valDT.Time.Equal(wantDT) {
				t.Errorf("val_dt = %v, want %v", valDT.Time, wantDT)
			}
		}
		if !valDouble.Valid || valDouble.Float64 != 99.9 {
			t.Errorf("val_double = %v, want 99.9", valDouble)
		}
		if valBool != true {
			t.Errorf("val_bool = %v, want true", valBool)
		}
	})
}

func TestDatabaseTypeNamesAllTypes(t *testing.T) {
	path := t.TempDir() + "/typed.mdb"
	if err := createTypedTestMDB(path); err != nil {
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

	rows, err := db.QueryContext(context.Background(),
		`SELECT id, flag, val_byte, val_short, val_int, val_long, val_single, val_double, val_currency, val_datetime, val_text, val_memo FROM typed WHERE id = 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	colTypes, err := rows.ColumnTypes()
	if err != nil {
		t.Fatal(err)
	}

	wantTypes := map[string]struct {
		dbType   string
		scanType reflect.Type
	}{
		"id":           {"Long Integer", reflect.TypeOf(int64(0))},
		"flag":         {"Boolean", reflect.TypeOf(false)},
		"val_byte":     {"Byte", reflect.TypeOf(int64(0))},
		"val_short":    {"Integer", reflect.TypeOf(int64(0))},
		"val_int":      {"Long Integer", reflect.TypeOf(int64(0))},
		"val_long":     {"Long Integer", reflect.TypeOf(int64(0))},
		"val_single":   {"Single", reflect.TypeOf(float64(0))},
		"val_double":   {"Double", reflect.TypeOf(float64(0))},
		"val_currency": {"Currency", reflect.TypeOf(float64(0))},
		"val_datetime": {"DateTime", reflect.TypeOf(time.Time{})},
		"val_text":     {"Text", reflect.TypeOf("")},
		"val_memo":     {"Memo/Hyperlink", reflect.TypeOf("")},
	}

	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}

	for i, name := range cols {
		want, ok := wantTypes[name]
		if !ok {
			t.Errorf("unexpected column %q", name)
			continue
		}
		gotDB := colTypes[i].DatabaseTypeName()
		gotScan := colTypes[i].ScanType()
		if gotDB != want.dbType {
			t.Errorf("col %q DatabaseTypeName = %q, want %q", name, gotDB, want.dbType)
		}
		if gotScan != want.scanType {
			t.Errorf("col %q ScanType = %v, want %v", name, gotScan, want.scanType)
		}
	}
}

func TestColumnTypeLengthReported(t *testing.T) {
	path := t.TempDir() + "/typed.mdb"
	if err := createTypedTestMDB(path); err != nil {
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

	rows, err := db.QueryContext(context.Background(),
		`SELECT val_text, val_memo FROM typed WHERE id = 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	colTypes, err := rows.ColumnTypes()
	if err != nil {
		t.Fatal(err)
	}

	// TEXT(50) reports length as 100 (2× for Unicode encoding in mdbtools)
	length, ok := colTypes[0].Length()
	if !ok {
		t.Fatal("val_text Length ok = false, want true")
	}
	if length != 100 {
		t.Errorf("val_text Length = %d, want 100 (mdbtools reports 2× for Unicode TEXT(50))", length)
	}

	// MEMO does not report a length
	if _, ok := colTypes[1].Length(); ok {
		t.Error("val_memo Length ok = true, want false")
	}
}

func TestScanIntoInterfaceSlice(t *testing.T) {
	path := t.TempDir() + "/typed.mdb"
	if err := createTypedTestMDB(path); err != nil {
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

	rows, err := db.QueryContext(context.Background(),
		`SELECT id, flag, val_text FROM typed WHERE id = 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	if !rows.Next() {
		t.Fatal("no rows")
	}

	var vals [3]interface{}
	if err := rows.Scan(&vals[0], &vals[1], &vals[2]); err != nil {
		t.Fatal(err)
	}

	if id, ok := vals[0].(int64); !ok || id != 1 {
		t.Errorf("id = %v (%T), want int64(1)", vals[0], vals[0])
	}
	if flag, ok := vals[1].(bool); !ok || flag != true {
		t.Errorf("flag = %v (%T), want bool(true)", vals[1], vals[1])
	}
	if s, ok := vals[2].(string); !ok || s != "hello world" {
		t.Errorf("val_text = %v (%T), want string('hello world')", vals[2], vals[2])
	}
}

func TestChineseCharacters(t *testing.T) {
	path := t.TempDir() + "/chinese.mdb"
	if err := createChineseTestMDB(path); err != nil {
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

	type row struct {
		id          int64
		name        string
		description string
		score       float64
	}

	tests := []struct {
		name string
		id   int64
		want row
	}{
		{
			name: "simple Chinese",
			id:   1,
			want: row{id: 1, name: "张三", description: "这是一个中文描述", score: 95.5},
		},
		{
			name: "Chinese with punctuation",
			id:   2,
			want: row{id: 2, name: "李四", description: "第二条记录，包含标点符号！@#", score: 88.0},
		},
		{
			name: "empty strings",
			id:   3,
			want: row{id: 3, name: "王五", description: "", score: 0},
		},
		{
			name: "mixed CJK scripts",
			id:   4,
			want: row{id: 4, name: "混合Mixed中English文", description: "日本語テスト 한국어 العربية", score: 77.7},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r row
			err := db.QueryRowContext(context.Background(),
				`SELECT id, name, description, score FROM chinese WHERE id = ?`, tt.id,
			).Scan(&r.id, &r.name, &r.description, &r.score)
			if err != nil {
				t.Fatal(err)
			}

			if r.id != tt.want.id {
				t.Errorf("id = %d, want %d", r.id, tt.want.id)
			}
			// mdbtools may mangle non-ASCII via mdb_unicode2ascii().
			// These assertions verify the actual round-trip behavior.
			if r.name != tt.want.name {
				t.Errorf("name = %q, want %q", r.name, tt.want.name)
			}
			if r.description != tt.want.description {
				t.Errorf("description = %q, want %q", r.description, tt.want.description)
			}
			if diff := r.score - tt.want.score; diff > 0.01 || diff < -0.01 {
				t.Errorf("score = %v, want %v", r.score, tt.want.score)
			}
		})
	}
}

func TestChineseCharacterTypes(t *testing.T) {
	path := t.TempDir() + "/chinese.mdb"
	if err := createChineseTestMDB(path); err != nil {
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

	rows, err := db.QueryContext(context.Background(),
		`SELECT id, name, description, score FROM chinese WHERE id = 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	colTypes, err := rows.ColumnTypes()
	if err != nil {
		t.Fatal(err)
	}

	wantColTypes := []struct {
		dbType   string
		scanType reflect.Type
	}{
		{"Long Integer", reflect.TypeOf(int64(0))},
		{"Text", reflect.TypeOf("")},
		{"Memo/Hyperlink", reflect.TypeOf("")},
		{"Double", reflect.TypeOf(float64(0))},
	}

	if len(colTypes) != len(wantColTypes) {
		t.Fatalf("ColumnTypes len = %d, want %d", len(colTypes), len(wantColTypes))
	}
	for i, w := range wantColTypes {
		if got := colTypes[i].DatabaseTypeName(); got != w.dbType {
			t.Errorf("col[%d] DatabaseTypeName = %q, want %q", i, got, w.dbType)
		}
		if got := colTypes[i].ScanType(); got != w.scanType {
			t.Errorf("col[%d] ScanType = %v, want %v", i, got, w.scanType)
		}
	}

	if !rows.Next() {
		t.Fatal("no rows")
	}
	var id int64
	var name, desc string
	var score float64
	if err := rows.Scan(&id, &name, &desc, &score); err != nil {
		t.Fatal(err)
	}
	if reflect.TypeOf(id) != reflect.TypeOf(int64(0)) {
		t.Errorf("id Go type = %T, want int64", id)
	}
	if reflect.TypeOf(name) != reflect.TypeOf("") {
		t.Errorf("name Go type = %T, want string", name)
	}
	if reflect.TypeOf(desc) != reflect.TypeOf("") {
		t.Errorf("desc Go type = %T, want string", desc)
	}
	if reflect.TypeOf(score) != reflect.TypeOf(float64(0)) {
		t.Errorf("score Go type = %T, want float64", score)
	}
}

// TestColumnTypeScanTypeAllMappings verifies all cmdb.Type → reflect.Type mappings
// using synthetic Rows (no MDB file needed).
func TestColumnTypeScanTypeAllMappings(t *testing.T) {
	tests := []struct {
		name     string
		colType  int
		wantType reflect.Type
	}{
		{"TypeBool", cmdb.TypeBool, reflect.TypeOf(false)},
		{"TypeByte", cmdb.TypeByte, reflect.TypeOf(int64(0))},
		{"TypeInt", cmdb.TypeInt, reflect.TypeOf(int64(0))},
		{"TypeLongInt", cmdb.TypeLongInt, reflect.TypeOf(int64(0))},
		{"TypeComplex", cmdb.TypeComplex, reflect.TypeOf(int64(0))},
		{"TypeMoney", cmdb.TypeMoney, reflect.TypeOf(float64(0))},
		{"TypeFloat", cmdb.TypeFloat, reflect.TypeOf(float64(0))},
		{"TypeDouble", cmdb.TypeDouble, reflect.TypeOf(float64(0))},
		{"TypeDateTime", cmdb.TypeDateTime, reflect.TypeOf(time.Time{})},
		{"TypeBinary", cmdb.TypeBinary, reflect.TypeOf([]byte{})},
		{"TypeOLE", cmdb.TypeOLE, reflect.TypeOf([]byte{})},
		{"TypeText", cmdb.TypeText, reflect.TypeOf("")},
		{"TypeMemo", cmdb.TypeMemo, reflect.TypeOf("")},
		{"TypeRepID", cmdb.TypeRepID, reflect.TypeOf("")},
		{"TypeNumeric", cmdb.TypeNumeric, reflect.TypeOf("")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := &Rows{info: []cmdb.Column{
				{Name: "col", Type: tt.colType},
			}}
			got := rows.ColumnTypeScanType(0)
			if got != tt.wantType {
				t.Errorf("ColumnTypeScanType(%s) = %v, want %v", tt.name, got, tt.wantType)
			}
		})
	}
}

// TestColumnTypeDatabaseTypeNameAllTypes verifies DatabaseTypeName strings
// for all known column types using synthetic Rows (no MDB file needed).
func TestColumnTypeDatabaseTypeNameAllTypes(t *testing.T) {
	tests := []struct {
		name     string
		colType  int
		dbType   string
		wantName string
	}{
		{"TypeBool", cmdb.TypeBool, "Boolean", "Boolean"},
		{"TypeByte", cmdb.TypeByte, "Byte", "Byte"},
		{"TypeInt", cmdb.TypeInt, "Integer", "Integer"},
		{"TypeLongInt", cmdb.TypeLongInt, "Long Integer", "Long Integer"},
		{"TypeMoney", cmdb.TypeMoney, "Currency", "Currency"},
		{"TypeFloat", cmdb.TypeFloat, "Single", "Single"},
		{"TypeDouble", cmdb.TypeDouble, "Double", "Double"},
		{"TypeDateTime", cmdb.TypeDateTime, "DateTime", "DateTime"},
		{"TypeBinary", cmdb.TypeBinary, "Binary", "Binary"},
		{"TypeText", cmdb.TypeText, "Text", "Text"},
		{"TypeOLE", cmdb.TypeOLE, "OLE", "OLE"},
		{"TypeMemo", cmdb.TypeMemo, "Memo/Hyperlink", "Memo/Hyperlink"},
		{"TypeRepID", cmdb.TypeRepID, "Replication ID", "Replication ID"},
		{"TypeNumeric", cmdb.TypeNumeric, "Numeric", "Numeric"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := &Rows{info: []cmdb.Column{
				{Name: "col", Type: tt.colType, DatabaseType: tt.dbType},
			}}
			got := rows.ColumnTypeDatabaseTypeName(0)
			if got != tt.wantName {
				t.Errorf("ColumnTypeDatabaseTypeName(%s) = %q, want %q", tt.name, got, tt.wantName)
			}
		})
	}
}
