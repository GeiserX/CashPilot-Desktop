//go:build !winvmlab

package winvm_test

import (
	"context"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/e2e/harness/hvfake"
	"github.com/GeiserX/CashPilot-Desktop/internal/winvm"
)

// TestProductBuildRefusesLabSettings: a product build never lowers the RAM
// floor, keeps Enhanced Session on, or builds another instance, whatever the
// params file says. It refuses before it reads the host.
func TestProductBuildRefusesLabSettings(t *testing.T) {
	sp := setupParams()
	sp.Lab = &winvm.LabOverrides{Instance: 1, MinHostRAM: 4 << 30, KeepEnhancedSession: true}
	if _, _, _, err := winvm.SetupNames(sp); err == nil {
		t.Fatal("SetupNames took lab settings in a product build")
	}
	f := hvfake.New()
	if _, err := winvm.Build(context.Background(), buildDeps(t, f, t.TempDir()), sp); err == nil {
		t.Fatal("Build took lab settings in a product build")
	}
	if c := f.Calls(); len(c) != 0 {
		t.Fatalf("the refused build called %v", c)
	}
}
