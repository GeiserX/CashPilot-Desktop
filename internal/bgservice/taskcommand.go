package bgservice

import "strings"

// taskCommandScript is the PowerShell that prints, as UTF-8, the executable the
// scheduled task taskName (folder\name) starts, and prints nothing when there is
// no such task. It asks Task Scheduler's own COM API, which a user may read for
// their own tasks without admin. A missing folder or task is HRESULT 0x80070002
// or 0x80070003 there, a code and not a sentence, so it reads the same on a
// Windows in any language. Any other error fails the script.
func taskCommandScript(taskName string) string {
	folder, name := `\`, taskName
	if i := strings.LastIndex(taskName, `\`); i >= 0 {
		folder, name = `\`+strings.Trim(taskName[:i], `\`), taskName[i+1:]
	}
	return `$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding $false
$s = New-Object -ComObject Schedule.Service
$s.Connect()
try {
  $task = $s.GetFolder(` + psLiteral(folder) + `).GetTask(` + psLiteral(name) + `)
} catch {
  # .NET turns 0x80070002 into a FileNotFoundException, inside PowerShell's own
  # wrapper, so every exception in the chain is checked for the code.
  for ($e = $_.Exception; $e; $e = $e.InnerException) {
    if ($e.HResult -eq -2147024894 -or $e.HResult -eq -2147024893) { exit 0 }
  }
  throw
}
foreach ($a in $task.Definition.Actions) { if ($a.Type -eq 0 -and $a.Path) { [Console]::Out.Write($a.Path); break } }
exit 0
`
}

// taskStateScript is the PowerShell that prints the State number of the scheduled
// task taskName (folder\name), and prints nothing when there is no such task.
// Task Scheduler's states are numbers (4 is running), the same in every Windows
// language.
func taskStateScript(taskName string) string {
	return strings.Replace(taskCommandScript(taskName),
		"foreach ($a in $task.Definition.Actions) { if ($a.Type -eq 0 -and $a.Path) { [Console]::Out.Write($a.Path); break } }",
		"[Console]::Out.Write([string]$task.State)", 1)
}

// taskStateRunning reports whether taskStateScript printed the running state, 4.
func taskStateRunning(out string) bool {
	return strings.TrimSpace(strings.TrimPrefix(out, "\ufeff")) == "4"
}

// parseTaskCommand turns what taskCommandScript printed into a path: trimmed,
// and without the quotes a task made by hand may put around it.
func parseTaskCommand(out string) string {
	return strings.Trim(strings.TrimSpace(strings.TrimPrefix(out, "\ufeff")), `"`)
}

// psLiteral makes a PowerShell single-quoted literal: inside one, only ' is
// special and it is escaped by doubling.
func psLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
