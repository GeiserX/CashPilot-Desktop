//go:build !windows

package runtime

import "fmt"

// ensureFirewallBlock refuses outside Windows: firewall_block is a Windows Firewall
// rule, and a binary that asks for one must not start without it.
func ensureFirewallBlock(slug, program string, nets []string) error {
	_ = program
	return fmt.Errorf("%s was not started: firewall_block %v is only implemented on Windows", slug, nets)
}

// removeFirewallBlock has nothing to remove outside Windows.
func removeFirewallBlock(slug, program string) error {
	_, _ = slug, program
	return nil
}
