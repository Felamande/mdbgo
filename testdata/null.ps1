$ErrorActionPreference = 'Stop'
$path = "nulltest.mdb"
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