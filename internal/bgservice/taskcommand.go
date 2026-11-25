package bgservice

import "strings"

// taskCommandScript is the PowerShell that prints, as UTF-8, the executable the
// scheduled task taskName (folder\name) starts, and prints nothing when there is
// no such task. Get-ScheduledTask reads a task of this user without admin. A
// missing task is a CIM NotFound error, which a SilentlyContinue would still turn
// into exit code 1, so it is caught by its code.
func taskCommandScript(taskName string) string {
	folder, name := `\`, taskName
	if i := strings.LastIndex(taskName, `\`); i >= 0 {
		folder, name = `\`+strings.Trim(taskName[:i], `\`)+`\`, taskName[i+1:]
	}
	return `$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding $false
try {
  $t = Get-ScheduledTask -TaskPath ` + psLiteral(folder) + ` -TaskName ` + psLiteral(name) + `
} catch [Microsoft.Management.Infrastructure.CimException] {
  # No such task, or no such folder: nothing is registered. Any other error fails.
  if ($_.Exception.NativeErrorCode -eq 'NotFound') { exit 0 }
  throw
}
foreach ($a in @($t.Actions)) { if ($a.Execute) { [Console]::Out.Write($a.Execute); break } }
exit 0
`
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
