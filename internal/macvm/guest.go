package macvm

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/i18n"
	"github.com/GeiserX/CashPilot-Desktop/internal/securevm"
)

// The guest scripts. They run inside the guest as root, through the agent.
var (
	//go:embed scripts/provision.sh
	provisionScript string
	//go:embed scripts/install-app.sh
	installAppScript string
	//go:embed scripts/read-apps.sh
	readAppsScript string
	//go:embed scripts/remove-app.sh
	removeAppScript string
)

// Guest reaches the guest from the owner's side, with the daemon's Tart.
type Guest struct {
	Tart     string
	TartHome string
}

// ProductGuest is the guest through the installed Tart.
func ProductGuest(tartHome string) Guest {
	return Guest{Tart: TartExe(ProductPaths().Bin), TartHome: tartHome}
}

// Exec runs script in the guest as root with args, stdin on its standard input.
func (g Guest) Exec(ctx context.Context, stdin, script string, args ...string) (string, error) {
	a := append([]string{"exec", "-i", VMName, "/bin/sh", "-c", script, "cp"}, args...)
	cmd := exec.CommandContext(ctx, g.Tart, a...)
	cmd.Env = append(os.Environ(), "TART_HOME="+g.TartHome)
	cmd.Stdin = strings.NewReader(stdin)
	cmd.WaitDelay = 5 * time.Second
	var errb strings.Builder
	cmd.Stderr = &errb
	out, err := cmd.Output()
	if err != nil {
		msg := errLine(errb.String())
		if msg == "" {
			msg = errLine(string(out))
		}
		return string(out), fmt.Errorf("%w: %s", err, msg)
	}
	return string(out), nil
}

// errLine is the guest's failure as the page shows it: our scripts say why in
// their last line, and a tool's stderr can run to pages.
func errLine(s string) string {
	l := lastLine(s)
	if r := []rune(l); len(r) > 300 {
		l = string(r[:300]) + "…"
	}
	return l
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// Ready is whether the agent answers.
func (g Guest) Ready(ctx context.Context) bool {
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err := g.Exec(c, "", "/usr/bin/true")
	return err == nil
}

// kcPassword is the /etc/kcpassword bytes for pw: the password XORed with
// macOS's fixed autologin key, padded with zero bytes to a multiple of 12. It
// is obfuscation, not a cipher; the file is root's, mode 600.
func kcPassword(pw string) []byte {
	key := []byte{0x7D, 0x89, 0x52, 0x23, 0xD2, 0xBC, 0xDD, 0xEA, 0xA3, 0xB9, 0x1F}
	b := []byte(pw)
	b = append(b, make([]byte, 12-len(b)%12)...)
	for i := range b {
		b[i] ^= key[i%len(key)]
	}
	return b
}

// Provision turns the fresh guest into the earner's: a standard user
// `earner` that logs in by itself, public DNS, no sleep, no remote doors, no
// automatic macOS upgrade. It runs once; a second run only reports. The
// password is random, used here and never kept: root reaches the guest
// through the agent, and nothing else needs it.
func (g Guest) Provision(ctx context.Context) (bool, error) {
	pw := make([]byte, 16)
	if _, err := rand.Read(pw); err != nil {
		return false, err
	}
	p := hex.EncodeToString(pw)
	stdin := p + "\n" + hex.EncodeToString(kcPassword(p)) + "\n"
	out, err := g.Exec(ctx, stdin, provisionScript)
	if err != nil {
		return false, fmt.Errorf("%s: %w", i18n.T("go.macvm.noPrepare"), err)
	}
	return strings.Contains(out, "PROVISIONED new"), nil
}

// Rosetta installs Rosetta in the guest. Theta and UpRock are Intel-only.
func (g Guest) Rosetta(ctx context.Context) error {
	out, err := g.Exec(ctx, "", `/usr/bin/arch -x86_64 /usr/bin/true 2>/dev/null && { echo ROSETTA ok; exit 0; }
/usr/sbin/softwareupdate --install-rosetta --agree-to-license >/dev/null 2>&1
/usr/bin/arch -x86_64 /usr/bin/true 2>/dev/null && echo ROSETTA ok || echo ROSETTA missing`)
	if err != nil {
		return fmt.Errorf("%s: %w", i18n.T("go.macvm.noRosetta"), err)
	}
	if !strings.Contains(out, "ROSETTA ok") {
		return errors.New(i18n.T("go.macvm.rosettaMissing"))
	}
	return nil
}

// HasRosetta is whether Rosetta runs in the guest.
func (g Guest) HasRosetta(ctx context.Context) securevm.Tri {
	out, err := g.Exec(ctx, "", `/usr/bin/arch -x86_64 /usr/bin/true 2>/dev/null && echo yes || echo no`)
	switch {
	case err != nil:
		return securevm.Unknown
	case strings.Contains(out, "yes"):
		return securevm.Yes
	}
	return securevm.No
}

// InstallApp downloads, checks and installs one earner in the guest, then
// starts it in the earner's session. The guest refuses it unless the SHA-256
// matches (when pinned), the Team ID matches and Gatekeeper accepts it.
func (g Guest) InstallApp(ctx context.Context, slug string, m catalog.GuestMacOS) error {
	sha := m.SHA256
	if sha == "" {
		sha = "-"
	}
	name, err := stagedName(m.Download)
	if err != nil {
		return fmt.Errorf("%s: %w", i18n.T("go.macvm.noInstallApp", "app", m.App), err)
	}
	out, err := g.Exec(ctx, "", installAppScript, m.Download, sha, m.TeamID, name, m.App, slug)
	return installResult(out, err, m.App)
}

// stagedName is the file name the guest saves a download under: the last
// segment of the URL path, without the query or the fragment, because its
// extension tells the installer the type.
func stagedName(download string) (string, error) {
	u, err := url.Parse(download)
	if err != nil {
		return "", err
	}
	name := path.Base(u.Path)
	if name == "." || name == "/" {
		return "", errors.New("the download URL names no file")
	}
	return name, nil
}

// installResult reads install-app.sh's RESULT line. "ok" counts only when
// Tart also returned without an error, or the end was never confirmed.
func installResult(out string, err error, app string) error {
	result := ""
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "RESULT ") {
			result = strings.TrimPrefix(l, "RESULT ")
		}
	}
	switch {
	case result == "ok" && err == nil:
		return nil
	case strings.HasPrefix(result, "refused: "):
		return errors.New(i18n.T("go.macvm.appRefused", "app", app, "why", strings.TrimPrefix(result, "refused: ")))
	case err != nil:
		return fmt.Errorf("%s: %w", i18n.T("go.macvm.noInstallApp", "app", app), err)
	}
	return errors.New(i18n.T("go.macvm.noInstallApp", "app", app))
}

// RemoveApp stops an earner and takes it out of the guest. Its own data (the
// sign-in) stays in the guest until the VM is removed.
func (g Guest) RemoveApp(ctx context.Context, slug string, m catalog.GuestMacOS) error {
	_, err := g.Exec(ctx, "", removeAppScript, m.App, m.Process, slug)
	return err
}

// Front opens the earner's app in the guest's session, in front, for a sign-in.
func (g Guest) Front(ctx context.Context, m catalog.GuestMacOS) error {
	_, err := g.Exec(ctx, "", `u=$(id -u earner) || exit 1; /bin/launchctl asuser "$u" /usr/bin/sudo -u earner /usr/bin/open -a "/Applications/$1"`, m.App)
	return err
}

// AppState is one earner as the guest read saw it.
type AppState struct {
	Installed securevm.Tri
	Running   securevm.Tri
	SignedIn  securevm.Tri
}

// ReadApps reads every earner's state in one call. It returns presence and
// yes/no only, never a file's content or a log line.
func (g Guest) ReadApps(ctx context.Context, apps map[string]catalog.GuestMacOS) (map[string]AppState, error) {
	var in strings.Builder
	for slug, m := range apps {
		kind, file, arg := "process", "", ""
		switch mk := m.SignedIn; {
		case mk.Process:
		case mk.Key != "":
			kind, file, arg = "key", mk.File, mk.Key
		case mk.LogWords != "":
			kind, file, arg = "log", mk.File, mk.LogWords
		default:
			kind, file = "file", mk.File
		}
		fmt.Fprintf(&in, "%s|%s|%s|%s|%s|%s\n", slug, m.App, m.Process, kind, file, arg)
	}
	out, err := g.Exec(ctx, in.String(), readAppsScript)
	if err != nil {
		return nil, err
	}
	tri := func(s string) securevm.Tri {
		switch s {
		case "y":
			return securevm.Yes
		case "n":
			return securevm.No
		}
		return securevm.Unknown
	}
	res := map[string]AppState{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimSpace(l), "|")
		if len(f) != 4 {
			continue
		}
		if _, ok := apps[f[0]]; ok {
			res[f[0]] = AppState{Installed: tri(f[1]), Running: tri(f[2]), SignedIn: tri(f[3])}
		}
	}
	return res, nil
}
