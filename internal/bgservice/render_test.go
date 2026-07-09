package bgservice

import (
	"strings"
	"testing"
)

const testExec = "/Applications/CashPilot Desktop.app/Contents/MacOS/CashPilot-Desktop"

// TestPlistLaunchAgent asserts the launchd plist carries the label, the exec path, the
// --daemon arg, and the keep-alive/restart directives the OS persistence relies on.
func TestPlistLaunchAgent(t *testing.T) {
	out := plistLaunchAgent(DefaultLabel, testExec, []string{"--daemon"},
		"/logs/helper.out.log", "/logs/helper.err.log")

	wants := []string{
		`<string>cloud.cashpilot.desktop.helper</string>`,
		`<key>ProgramArguments</key>`,
		`<string>--daemon</string>`,
		`<key>RunAtLoad</key>` + "\n\t<true/>",
		`<key>KeepAlive</key>` + "\n\t<true/>",
		`<key>ThrottleInterval</key>` + "\n\t<integer>30</integer>",
		`<key>ProcessType</key>` + "\n\t<string>Background</string>",
		`<string>/logs/helper.out.log</string>`,
		`<string>/logs/helper.err.log</string>`,
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("plist missing %q\n---\n%s", w, out)
		}
	}
	// The exec path contains a space and an escapable char is absent, but it must be
	// present verbatim as its own <string> element (a single ProgramArguments entry).
	if !strings.Contains(out, "<string>"+testExec+"</string>") {
		t.Errorf("plist missing verbatim exec path element\n%s", out)
	}
	if !strings.HasPrefix(out, `<?xml version="1.0" encoding="UTF-8"?>`) {
		t.Errorf("plist missing xml prolog\n%s", out)
	}
}

// TestPlistLaunchAgentEscapes proves XML-special characters in the exec path are escaped
// so the plist stays well-formed.
func TestPlistLaunchAgentEscapes(t *testing.T) {
	out := plistLaunchAgent("lbl", `/opt/a&b/c<d>/cash`, []string{"--daemon"}, "/o", "/e")
	if strings.Contains(out, "a&b") || strings.Contains(out, "c<d>") {
		t.Errorf("exec path not XML-escaped:\n%s", out)
	}
	if !strings.Contains(out, "a&amp;b") || !strings.Contains(out, "c&lt;d&gt;") {
		t.Errorf("expected escaped entities in plist:\n%s", out)
	}
}

// TestSystemdUserUnit asserts the systemd unit runs "<exec> --daemon", restarts always,
// and disables the start-rate limiter so a crash loop is not permanently latched failed.
func TestSystemdUserUnit(t *testing.T) {
	out := systemdUserUnit("/usr/bin/cashpilot", []string{"--daemon"}, helperDescription)

	wants := []string{
		"[Unit]",
		"Description=" + helperDescription,
		"StartLimitIntervalSec=0",
		"[Service]",
		"Type=simple",
		"ExecStart=/usr/bin/cashpilot --daemon",
		"Restart=always",
		"RestartSec=5",
		"[Install]",
		"WantedBy=default.target",
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("unit missing %q\n---\n%s", w, out)
		}
	}
}

// TestSystemdUserUnitQuotesSpaces proves an exec path with spaces is double-quoted so
// systemd parses it as a single token.
func TestSystemdUserUnitQuotesSpaces(t *testing.T) {
	out := systemdUserUnit("/opt/Cash Pilot/cashpilot", []string{"--daemon"}, "d")
	if !strings.Contains(out, `ExecStart="/opt/Cash Pilot/cashpilot" --daemon`) {
		t.Errorf("expected quoted ExecStart for spaced path:\n%s", out)
	}
}

// TestScheduledTaskXML asserts the Windows task carries the logon trigger, interactive
// least-privilege principal, restart-on-failure (3× a minute apart), no execution-time
// limit, battery-safe settings, and the exec/arguments.
func TestScheduledTaskXML(t *testing.T) {
	out := scheduledTaskXML(`C:\Program Files\CashPilot\cashpilot.exe`, []string{"--daemon"}, `MACHINE\sergio`, helperDescription)

	wants := []string{
		`<?xml version="1.0" encoding="UTF-16"?>`,
		`<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">`,
		"<LogonTrigger>",
		"<UserId>MACHINE\\sergio</UserId>",
		`<Principal id="Author">`,
		"<LogonType>InteractiveToken</LogonType>",
		"<RunLevel>LeastPrivilege</RunLevel>",
		"<RestartOnFailure>",
		"<Interval>PT1M</Interval>",
		"<Count>3</Count>",
		"<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>",
		"<StartWhenAvailable>true</StartWhenAvailable>",
		"<DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>",
		`<Actions Context="Author">`,
		"<Command>C:\\Program Files\\CashPilot\\cashpilot.exe</Command>",
		"<Arguments>--daemon</Arguments>",
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("task xml missing %q\n---\n%s", w, out)
		}
	}
}

// TestScheduledTaskXMLNoUser proves the UserId elements are omitted when no user is known
// (so an empty <UserId></UserId> never reaches schtasks).
func TestScheduledTaskXMLNoUser(t *testing.T) {
	out := scheduledTaskXML(`C:\cashpilot.exe`, []string{"--daemon"}, "", "d")
	if strings.Contains(out, "<UserId>") {
		t.Errorf("did not expect a UserId element with empty user:\n%s", out)
	}
}

// TestUTF16LEWithBOM proves the schtasks encoding helper prepends the UTF-16LE BOM and
// little-endian-encodes the content.
func TestUTF16LEWithBOM(t *testing.T) {
	b := utf16LEWithBOM("AB")
	want := []byte{0xFF, 0xFE, 'A', 0x00, 'B', 0x00}
	if len(b) != len(want) {
		t.Fatalf("len = %d, want %d (%v)", len(b), len(want), b)
	}
	for i := range want {
		if b[i] != want[i] {
			t.Fatalf("byte %d = %#x, want %#x (%v)", i, b[i], want[i], b)
		}
	}
}
