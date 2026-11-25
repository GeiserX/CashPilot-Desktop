//go:build !windows

package main

import "testing"

// guardRealRunOnce has nothing to guard outside Windows: there is no RunOnce.
func guardRealRunOnce(t *testing.T) { t.Helper() }
