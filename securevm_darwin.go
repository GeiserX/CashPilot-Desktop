//go:build darwin

package main

import (
	"os"
	"runtime"

	"github.com/GeiserX/CashPilot-Desktop/internal/macvm"
	"github.com/GeiserX/CashPilot-Desktop/internal/securevm"
)

func goos() string { return runtime.GOOS }

func newSecureVMEngine(a *App) (securevm.Engine, error) {
	return macvm.NewEngine(AppVersion(), func() []macvm.AppSpec {
		var out []macvm.AppSpec
		for _, s := range a.secureVMApps() {
			out = append(out, macvm.AppSpec{Slug: s.Slug, Name: s.Name, Refused: s.Refused, MacOS: s.MacOS, OnHost: s.OnHost})
		}
		return out
	})
}

// runMacVMMain runs the Secure VM's root roles (--macvm daemon|install|remove).
func runMacVMMain(role string, args []string) int {
	return macvm.Main(role, args, AppVersion(), os.Stdout)
}
