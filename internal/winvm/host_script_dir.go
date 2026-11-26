package winvm

import (
	"fmt"
	"path/filepath"
)

// Host scripts are independent of the VM instance directory. Setup creates and
// records the identity of that directory separately.
func hostScriptDirectory(programData string) (string, error) {
	if !filepath.IsAbs(programData) {
		return "", fmt.Errorf("expected an absolute ProgramData path, got %q", programData)
	}
	return filepath.Join(programData, "CashPilot-HostScripts"), nil
}
