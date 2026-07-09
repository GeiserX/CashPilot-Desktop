package bgservice

import (
	"bytes"
	"encoding/xml"
	"strings"
	"unicode/utf16"
)

// xmlEscape escapes s for safe inclusion in an XML/plist text node (paths and arguments
// may contain &, <, > or quotes).
func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// plistLaunchAgent renders a launchd LaunchAgent property list that runs execPath with
// args at login and keeps it alive (KeepAlive + RunAtLoad), throttled so a crash-loop
// cannot busy-spin, running as a Background process, with stdout/stderr captured to the
// given log files.
func plistLaunchAgent(label, execPath string, args []string, stdoutPath, stderrPath string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n")
	b.WriteString("<dict>\n")
	b.WriteString("\t<key>Label</key>\n\t<string>" + xmlEscape(label) + "</string>\n")
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	b.WriteString("\t\t<string>" + xmlEscape(execPath) + "</string>\n")
	for _, arg := range args {
		b.WriteString("\t\t<string>" + xmlEscape(arg) + "</string>\n")
	}
	b.WriteString("\t</array>\n")
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	b.WriteString("\t<key>KeepAlive</key>\n\t<true/>\n")
	b.WriteString("\t<key>ThrottleInterval</key>\n\t<integer>30</integer>\n")
	b.WriteString("\t<key>ProcessType</key>\n\t<string>Background</string>\n")
	b.WriteString("\t<key>StandardOutPath</key>\n\t<string>" + xmlEscape(stdoutPath) + "</string>\n")
	b.WriteString("\t<key>StandardErrorPath</key>\n\t<string>" + xmlEscape(stderrPath) + "</string>\n")
	b.WriteString("</dict>\n")
	b.WriteString("</plist>\n")
	return b.String()
}

// systemdUserUnit renders a systemd --user service unit that runs execPath with args and
// is restarted forever on exit.
//
// StartLimitIntervalSec is placed in [Unit] (its correct home in modern systemd) and set
// to 0 to disable the start-rate limiter — otherwise a crash-looping helper latches into
// a permanent "failed" state instead of being retried, which would defeat the whole
// keep-alive purpose.
func systemdUserUnit(execPath string, args []string, description string) string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=" + description + "\n")
	b.WriteString("StartLimitIntervalSec=0\n")
	b.WriteString("\n[Service]\n")
	b.WriteString("Type=simple\n")
	b.WriteString("ExecStart=" + execLine(execPath, args) + "\n")
	b.WriteString("Restart=always\n")
	b.WriteString("RestartSec=5\n")
	b.WriteString("\n[Install]\n")
	b.WriteString("WantedBy=default.target\n")
	return b.String()
}

// execLine builds a systemd ExecStart command line, double-quoting any token that
// contains whitespace or a quote/backslash (systemd's own quoting rules) so a binary
// installed under a path with spaces still parses as a single argument.
func execLine(execPath string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, systemdQuote(execPath))
	for _, a := range args {
		parts = append(parts, systemdQuote(a))
	}
	return strings.Join(parts, " ")
}

func systemdQuote(s string) string {
	if s == "" || strings.ContainsAny(s, " \t\"\\") {
		r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
		return `"` + r.Replace(s) + `"`
	}
	return s
}

// scheduledTaskXML renders a Windows Task Scheduler task definition that runs execPath
// with args at the current user's logon under an interactive token (no admin), restarting
// up to 3 times a minute apart on failure and with no execution-time limit (PT0S) so the
// long-lived helper is never force-stopped. The <Settings> element order follows the
// order the Task Scheduler UI itself exports, which schtasks accepts.
func scheduledTaskXML(execPath string, args []string, userID, description string) string {
	command := xmlEscape(execPath)
	arguments := xmlEscape(strings.Join(args, " "))
	desc := xmlEscape(description)
	uid := xmlEscape(userID)

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-16"?>` + "\n")
	b.WriteString(`<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">` + "\n")
	b.WriteString("  <RegistrationInfo>\n")
	b.WriteString("    <Description>" + desc + "</Description>\n")
	b.WriteString("  </RegistrationInfo>\n")
	b.WriteString("  <Triggers>\n")
	b.WriteString("    <LogonTrigger>\n")
	b.WriteString("      <Enabled>true</Enabled>\n")
	if userID != "" {
		b.WriteString("      <UserId>" + uid + "</UserId>\n")
	}
	b.WriteString("    </LogonTrigger>\n")
	b.WriteString("  </Triggers>\n")
	b.WriteString("  <Principals>\n")
	b.WriteString(`    <Principal id="Author">` + "\n")
	if userID != "" {
		b.WriteString("      <UserId>" + uid + "</UserId>\n")
	}
	b.WriteString("      <LogonType>InteractiveToken</LogonType>\n")
	b.WriteString("      <RunLevel>LeastPrivilege</RunLevel>\n")
	b.WriteString("    </Principal>\n")
	b.WriteString("  </Principals>\n")
	b.WriteString("  <Settings>\n")
	b.WriteString("    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>\n")
	b.WriteString("    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>\n")
	b.WriteString("    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>\n")
	b.WriteString("    <AllowHardTerminate>true</AllowHardTerminate>\n")
	b.WriteString("    <StartWhenAvailable>true</StartWhenAvailable>\n")
	b.WriteString("    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>\n")
	b.WriteString("    <IdleSettings>\n")
	b.WriteString("      <StopOnIdleEnd>false</StopOnIdleEnd>\n")
	b.WriteString("      <RestartOnIdle>false</RestartOnIdle>\n")
	b.WriteString("    </IdleSettings>\n")
	b.WriteString("    <AllowStartOnDemand>true</AllowStartOnDemand>\n")
	b.WriteString("    <Enabled>true</Enabled>\n")
	b.WriteString("    <Hidden>false</Hidden>\n")
	b.WriteString("    <RunOnlyIfIdle>false</RunOnlyIfIdle>\n")
	b.WriteString("    <WakeToRun>false</WakeToRun>\n")
	b.WriteString("    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>\n")
	b.WriteString("    <Priority>7</Priority>\n")
	b.WriteString("    <RestartOnFailure>\n")
	b.WriteString("      <Interval>PT1M</Interval>\n")
	b.WriteString("      <Count>3</Count>\n")
	b.WriteString("    </RestartOnFailure>\n")
	b.WriteString("  </Settings>\n")
	b.WriteString(`  <Actions Context="Author">` + "\n")
	b.WriteString("    <Exec>\n")
	b.WriteString("      <Command>" + command + "</Command>\n")
	if arguments != "" {
		b.WriteString("      <Arguments>" + arguments + "</Arguments>\n")
	}
	b.WriteString("    </Exec>\n")
	b.WriteString("  </Actions>\n")
	b.WriteString("</Task>\n")
	return b.String()
}

// utf16LEWithBOM encodes s as little-endian UTF-16 with a leading byte-order mark, the
// on-disk form schtasks.exe expects for a task XML whose declaration says
// encoding="UTF-16".
func utf16LEWithBOM(s string) []byte {
	u := utf16.Encode([]rune(s))
	buf := make([]byte, 0, 2+len(u)*2)
	buf = append(buf, 0xFF, 0xFE) // UTF-16LE BOM
	for _, r := range u {
		buf = append(buf, byte(r), byte(r>>8))
	}
	return buf
}
