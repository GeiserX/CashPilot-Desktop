//go:build !windows

package runtime

import (
	"context"
	"errors"
	"fmt"
)

// ensureFirewallBlock refuses outside Windows: firewall_block is a Windows Firewall
// rule, and a binary that asks for one must not start without it.
func ensureFirewallBlock(slug, program string, nets []string) error {
	_ = program
	return fmt.Errorf("%s was not started: firewall_block %v is only implemented on Windows", slug, nets)
}

// ensureFirewallBlockWith refuses outside Windows, as ensureFirewallBlock does.
func ensureFirewallBlockWith(slug, program string, nets []string, mayElevate bool) error {
	_ = mayElevate
	return ensureFirewallBlock(slug, program, nets)
}

// firewallReady: outside Windows there is no rule to read, and no prompt either.
func firewallReady(slug, program string, nets []string) (bool, error) {
	_, _, _ = slug, program, nets
	return false, nil
}

// removeFirewallBlock has nothing to remove outside Windows.
func removeFirewallBlock(slug, program string) error {
	_, _ = slug, program
	return nil
}

// RunElevated runs admin scripts only on Windows.
func RunElevated(script string) (ElevatedResult, error) {
	_ = script
	return ElevatedResult{}, errors.New("admin scripts run only on Windows")
}

// RunElevatedContext runs admin scripts only on Windows.
func RunElevatedContext(ctx context.Context, script string) (ElevatedResult, error) {
	_, _ = ctx, script
	return ElevatedResult{}, errors.New("admin scripts run only on Windows")
}
