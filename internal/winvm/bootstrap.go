package winvm

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// The two bootstrap scripts the admin stub runs (DESKTOP-VM §4, "the elevated
// script is long"): short PowerShell that copies the exe into the admin-only
// guard folder, checks it, and runs it as --vm-setup or --vm-remove. All the
// work is Go behind the Builder seam.

//go:embed scripts/setup.ps1
var setupPS1 []byte

//go:embed scripts/remove.ps1
var removePS1 []byte

// Bootstrap is one embedded bootstrap script.
type Bootstrap struct {
	Name   string // the file name the stub gives the copy
	Text   []byte
	SHA256 string // uppercase hex, as Get-FileHash writes it
}

func bootstrap(name string, b []byte) Bootstrap {
	const marker = "$ErrorActionPreference = 'Stop'\n"
	b = bytes.Replace(b, []byte(marker), []byte(marker+runtime.SecureVMDirectoryPS+"\n"+runtime.SecureVMFilesPS), 1)
	s := sha256.Sum256(b)
	return Bootstrap{Name: name, Text: b, SHA256: strings.ToUpper(hex.EncodeToString(s[:]))}
}

// SetupBootstrap is setup.ps1.
func SetupBootstrap() Bootstrap { return bootstrap("setup.ps1", setupPS1) }

// RemoveBootstrap is remove.ps1.
func RemoveBootstrap() Bootstrap { return bootstrap("remove.ps1", removePS1) }

// GuardDir is the admin-only folder for the bootstraps and the exe copy the
// guard task runs. Names hold Windows paths, so it is joined with a
// backslash on every OS.
func (n Names) GuardDir() string { return n.DataDir + `\guard` }

// GuardExe is the exe copy the guard task runs, and setup and removal run as.
func (n Names) GuardExe() string { return n.GuardDir() + `\CashPilot-guard.exe` }
