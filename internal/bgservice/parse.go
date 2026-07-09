package bgservice

import "strings"

// launchctlPrintRunning reports whether `launchctl print gui/<uid>/<label>` output
// describes a live agent. launchd prints a "state = running" line for an active service;
// as a fallback a "pid = <n>" line also means the process is alive.
func launchctlPrintRunning(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "state = "); ok {
			// Match the exact state value: "running" is alive, but "not running",
			// "waiting", "exited" etc. are not (a substring check would wrongly treat
			// "not running" as running).
			return strings.TrimSpace(v) == "running"
		}
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "pid = ") {
			return true
		}
	}
	return false
}

// systemdActive reports whether `systemctl --user is-active <unit>` output means active.
// is-active prints exactly "active" (with a trailing newline) and exits 0 when running.
func systemdActive(out string) bool {
	return strings.TrimSpace(out) == "active"
}

// systemdEnabled reports whether `systemctl --user is-enabled <unit>` output means the
// unit is registered to start (enabled or a static/linked variant), so a raced file-stat
// still resolves to Installed.
func systemdEnabled(out string) bool {
	switch strings.TrimSpace(out) {
	case "enabled", "enabled-runtime", "static", "linked", "linked-runtime", "alias":
		return true
	default:
		return false
	}
}

// schtasksRunning reports whether `schtasks /query /tn <name> /fo list` output shows the
// task currently Running (its "Status:" line). Ready/Disabled/other states are not
// running.
func schtasksRunning(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Status:") {
			state := strings.TrimSpace(strings.TrimPrefix(line, "Status:"))
			return strings.EqualFold(state, "Running")
		}
	}
	return false
}
