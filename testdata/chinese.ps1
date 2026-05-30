$ErrorActionPreference = 'Stop'
$path = ".\chinese.mdb"

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