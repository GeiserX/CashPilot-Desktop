package winvm

import (
	"fmt"
	"regexp"
)

// Names holds every host object the Secure VM owns. The product uses one fixed
// set. The lab uses cpd-svm-<n>, so a lab run never touches a real install or any
// other VM on the PC, and its cleanup is exact.
//
// After creation every action uses the VM ID, the switch ID and the host vNIC's
// ifIndex, never a name. A name is only for display and for the first lookup, so
// a VM that someone renames to CashPilot-VM is never touched.
type Names struct {
	VM            string `json:"vm"`
	Switch        string `json:"switch"`
	NAT           string `json:"nat"`
	FirewallGroup string `json:"firewallGroup"`
	GuardTask     string `json:"guardTask"`
	DataDir       string `json:"dataDir"`
}

const (
	productName = "CashPilot-VM"
	guardTask   = `\CashPilot\VMGuard`
	dataDir     = `C:\ProgramData\CashPilot`
	// MaxLabInstance is the highest lab instance number.
	MaxLabInstance = 9
)

// objectName is the only shape a host object name may have. Names reach
// PowerShell later, so nothing else is let through.
var objectName = regexp.MustCompile(`^[A-Za-z0-9-]{1,32}$`)

// ProductNames is the one set every real install uses.
func ProductNames() Names {
	return Names{
		VM:            productName,
		Switch:        productName,
		NAT:           productName,
		FirewallGroup: "CashPilotVM",
		GuardTask:     guardTask,
		DataDir:       dataDir,
	}
}

// LabNames is lab instance n, for n in 1..MaxLabInstance.
func LabNames(n int) (Names, error) {
	if n < 1 || n > MaxLabInstance {
		return Names{}, fmt.Errorf("lab instance %d is outside 1..%d", n, MaxLabInstance)
	}
	s := fmt.Sprintf("cpd-svm-%d", n)
	return Names{
		VM:            s,
		Switch:        s,
		NAT:           s,
		FirewallGroup: s,
		GuardTask:     guardTask + "-" + s,
		DataDir:       dataDir + `\lab\` + s,
	}, nil
}

// Validate accepts only the product set or one of the lab sets, so a plan file
// can never point the guard task or the data folder anywhere else.
func (n Names) Validate() error {
	for _, s := range []string{n.VM, n.Switch, n.NAT, n.FirewallGroup} {
		if !objectName.MatchString(s) {
			return fmt.Errorf("host object name %q is not allowed", s)
		}
	}
	if n == ProductNames() {
		return nil
	}
	for i := 1; i <= MaxLabInstance; i++ {
		if l, _ := LabNames(i); n == l {
			return nil
		}
	}
	return fmt.Errorf("names %q are neither the product set nor a lab set", n.VM)
}
