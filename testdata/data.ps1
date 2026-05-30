$ErrorActionPreference = 'Stop'
$path = ".\typed.mdb"
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