$ErrorActionPreference = 'Stop'
$path = ".\people.mdb"
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