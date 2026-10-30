package e2e

import (
	"regexp"
	"testing"
	"time"
)

// The windowed health check reads hours of the node's logs from the daemon and keeps
// only the NAT-traversal lines, through the same Docker client a user's install uses.
// The signal's ${UDP_PORTS} is filled from what was saved for the service.
func TestMysteriumsWindowReadFiltersTheDaemonsLogs(t *testing.T) {
	e := newEnv(t)
	if _, err := e.Manager.Deploy(e.ctx(), "mysterium", map[string]string{"UDP_PORTS": "41000:41050"}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	e.Docker.SetLogs("a Method:manual Success:false\nb Method:holepunching Success:true\nc Method:manual Success:true\n")
	match := regexp.MustCompile("(?i)(?:Method:manual Success:false)|(?:Method:manual Success:true)")
	got, err := e.Manager.LogsMatching(e.ctx(), "mysterium", 6*time.Hour, match, 1000)
	if err != nil {
		t.Fatalf("LogsMatching: %v", err)
	}
	if want := "a Method:manual Success:false\nc Method:manual Success:true"; got != want {
		t.Fatalf("LogsMatching = %q, want %q", got, want)
	}

	if err := e.Store.SaveCredentials("mysterium", map[string]string{"UDP_PORTS": "41000:41050"}); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	vars := e.Manager.SignalVars("mysterium")
	if vars["UDP_PORTS"] != "41000:41050" || vars["UI_ADDRESS"] != "127.0.0.1" {
		t.Fatalf("SignalVars = %v, want the saved range over the catalog defaults", vars)
	}
}
