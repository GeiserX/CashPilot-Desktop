//go:build windows

package runtime

import (
	"os"
	"testing"
)

// TestFirewallBlockLive drives the real Windows Firewall. It changes the machine, so
// it runs only when asked for:
//
//	CASHPILOT_FIREWALL_LIVE=ensure|check|remove
//	CASHPILOT_FIREWALL_LIVE_PROGRAM=C:\path\to\program.exe
//
// ensure creates the rule (an admin prompt unless the test runs elevated), check
// reads it the way an unelevated app does, remove deletes it.
func TestFirewallBlockLive(t *testing.T) {
	step := os.Getenv("CASHPILOT_FIREWALL_LIVE")
	if step == "" {
		t.Skip("set CASHPILOT_FIREWALL_LIVE to drive the real Windows Firewall")
	}
	const slug = "cashpilot-live-test"
	program := os.Getenv("CASHPILOT_FIREWALL_LIVE_PROGRAM")
	nets := []string{"100.64.0.0/10", "169.254.0.0/16"}
	switch step {
	case "ensure":
		if err := ensureFirewallBlock(slug, program, nets); err != nil {
			t.Fatal(err)
		}
		fallthrough
	case "check":
		st, err := readFirewallRule(firewallRuleName(slug, program))
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("rule %q: %+v", firewallRuleName(slug, program), st)
		if !st.matches(program, nets) {
			t.Fatal("the rule on this machine is not the one we asked for")
		}
		if why := st.notEnforced(); why != "" {
			t.Fatalf("the rule matches but would do nothing: %s", why)
		}
	case "remove":
		if err := removeFirewallBlock(slug, program); err != nil {
			t.Fatal(err)
		}
		st, err := readFirewallRule(firewallRuleName(slug, program))
		if err != nil {
			t.Fatal(err)
		}
		if st.Found {
			t.Fatal("the rule is still there")
		}
	default:
		t.Fatalf("unknown step %q", step)
	}
}
