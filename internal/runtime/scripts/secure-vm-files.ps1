function Write-CPDFileRecord($journal, [string]$path, [string]$id) {
  if ([CpdDirectory]::FileID($journal.Root) -cne $journal.RootId -or
      [CpdDirectory]::FileID($journal.Path) -cne $journal.Id) { throw 'The file journal changed.' }
  $line = [ordered]@{root = $journal.RootId; path = $path; id = $id} | ConvertTo-Json -Compress
  $file = [IO.File]::Open($journal.Path, [IO.FileMode]::Append, [IO.FileAccess]::Write, [IO.FileShare]::ReadWrite)
  try {
    $bytes = [Text.Encoding]::UTF8.GetBytes($line + [Environment]::NewLine)
    $file.Write($bytes, 0, $bytes.Length)
    $file.Flush($true)
  } finally { $file.Dispose() }
}

function Open-CPDFileJournal([string]$root) {
  $name = '.cashpilot-files.jsonl'
  $path = Join-Path $root $name
  $made = -not (Test-Path -LiteralPath $path)
  if ($made) { [IO.File]::Open($path, [IO.FileMode]::CreateNew).Dispose() }
  $journal = @{ Root = $root; RootId = [CpdDirectory]::FileID($root); Path = $path; Id = [CpdDirectory]::FileID($path) }
  if ($made) { Write-CPDFileRecord $journal $name $journal.Id }
  $first = Get-Content -LiteralPath $path -Encoding UTF8 -TotalCount 1 | ConvertFrom-Json
  if ($first.root -cne $journal.RootId -or $first.path -cne $name -or $first.id -cne $journal.Id) { throw 'The file journal was not created by this install.' }
  return $journal
}

function Get-CPDFileID($journal, [string]$rel) {
  $path = Join-Path $journal.Root $rel
  if (-not (Test-Path -LiteralPath $path)) { return $null }
  $id = [CpdDirectory]::FileID($path)
  $found = Get-Content -LiteralPath $journal.Path -Encoding UTF8 | ForEach-Object { $_ | ConvertFrom-Json } |
    Where-Object { $_.path -ieq $rel -and $_.root -ceq $journal.RootId -and $_.id -ceq $id } | Select-Object -First 1
  if (-not $found) { throw ('The file identity was not recorded: ' + $path) }
  return $id
}

function Copy-CPDFile($journal, [string]$rel, [string]$source) {
  $path = Join-Path $journal.Root $rel
  Write-CPDFileRecord $journal $rel ''
  $inputFile = [IO.File]::OpenRead($source)
  try {
    $outFile = [IO.File]::Open($path, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write)
    try { $inputFile.CopyTo($outFile); $outFile.Flush($true) }
    finally { $outFile.Dispose() }
  } finally { $inputFile.Dispose() }
  $id = [CpdDirectory]::FileID($path)
  Write-CPDFileRecord $journal $rel $id
  return $id
}

function Remove-CPDFile($journal, [string]$rel, [string]$id) {
  $path = Join-Path $journal.Root $rel
  if ($id -and (Test-Path -LiteralPath $path)) {
    if ([CpdDirectory]::FileID($journal.Root) -cne $journal.RootId -or -not [CpdDirectory]::DeleteOwned($path, $id)) {
      Write-Output ('left: ' + $path + ' (the file identity changed)')
    }
  }
}


function Write-CPDNewFile($journal, [string]$rel, [byte[]]$bytes) {
  $path = Join-Path $journal.Root $rel
  Write-CPDFileRecord $journal $rel ''
  $file = [IO.File]::Open($path, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write)
  try { $file.Write($bytes, 0, $bytes.Length); $file.Flush($true) }
  finally { $file.Dispose() }
  $id = [CpdDirectory]::FileID($path)
  Write-CPDFileRecord $journal $rel $id
  return $id
}
