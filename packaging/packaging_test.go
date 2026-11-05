// Package packaging checks that the store packages in this directory point at assets
// the release workflow really publishes, all name the same version, and carry
// well-formed checksums. packaging/render.sh writes them; this keeps a hand edit or a
// renamed release asset from shipping a package that cannot download.
package packaging

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	workflowPath = "../.github/workflows/desktop-release.yml"
	caskPath     = "homebrew/cashpilot-desktop.rb"
	aurDir       = "aur/cashpilot-desktop-bin"
	wingetRoot   = "winget/GeiserX.CashPilotDesktop"
)

var (
	sha256Re  = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
	assetRe   = regexp.MustCompile(`releases/download/[^/"'\s]+/(CashPilot-Desktop-[^"'\s]+)`)
	distRe    = regexp.MustCompile(`dist[/\\]CashPilot-Desktop-\$\{\{ matrix\.platform \}\}\.([a-z.]+)`)
	runnerOS  = map[string]string{"macos": "macOS", "ubuntu": "Linux", "windows": "Windows"}
	runnerIf  = regexp.MustCompile(`runner\.os == '(\w+)'`)
	urlVerRe  = regexp.MustCompile(`releases/download/v([^/]+)/`)
	caskVerRe = regexp.MustCompile(`(?m)^  version "([^"]+)"$`)
	caskShaRe = regexp.MustCompile(`(?m)^  sha256 "([^"]+)"$`)
)

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// publishedAssets returns the asset file names desktop-release.yml uploads, built from
// its matrix and the dist/ names in its per-OS packaging steps.
func publishedAssets(t *testing.T, workflow string) []string {
	t.Helper()
	var wf struct {
		Jobs struct {
			Build struct {
				Strategy struct {
					Matrix struct {
						Include []struct{ Runner, Platform string }
					}
				}
				Steps []struct {
					If  string
					Run string
				}
			}
		}
	}
	if err := yaml.Unmarshal([]byte(workflow), &wf); err != nil {
		t.Fatal(err)
	}
	extByOS := map[string]string{}
	for _, s := range wf.Jobs.Build.Steps {
		m := distRe.FindStringSubmatch(s.Run)
		if m == nil {
			continue
		}
		osm := runnerIf.FindStringSubmatch(s.If)
		if osm == nil {
			t.Fatalf("packaging step writing %q has no runner.os condition", m[0])
		}
		extByOS[osm[1]] = m[1]
	}
	var out []string
	for _, inc := range wf.Jobs.Build.Strategy.Matrix.Include {
		osName := runnerOS[strings.SplitN(inc.Runner, "-", 2)[0]]
		ext, ok := extByOS[osName]
		if !ok {
			t.Fatalf("matrix platform %s (runner %s) has no packaging step", inc.Platform, inc.Runner)
		}
		out = append(out, "CashPilot-Desktop-"+inc.Platform+"."+ext)
	}
	if len(out) == 0 {
		t.Fatal("found no published assets in the release workflow")
	}
	sort.Strings(out)
	return out
}

func wingetDir(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir(wingetRoot)
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) != 1 {
		t.Fatalf("want exactly one version directory under %s, got %v", wingetRoot, dirs)
	}
	return filepath.Join(wingetRoot, dirs[0])
}

// srcinfoValues returns every value of `key = value` lines in a .SRCINFO.
func srcinfoValues(srcinfo, key string) []string {
	var out []string
	for _, line := range strings.Split(srcinfo, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), " = "); ok && k == key {
			out = append(out, v)
		}
	}
	return out
}

func TestPackagesPointAtPublishedAssets(t *testing.T) {
	want := publishedAssets(t, read(t, workflowPath))

	files := map[string]string{
		"homebrew cask":    caskPath,
		"AUR PKGBUILD":     filepath.Join(aurDir, "PKGBUILD"),
		"AUR .SRCINFO":     filepath.Join(aurDir, ".SRCINFO"),
		"winget installer": filepath.Join(wingetDir(t), "GeiserX.CashPilotDesktop.installer.yaml"),
	}
	seen := map[string]bool{}
	for name, path := range files {
		matches := assetRe.FindAllStringSubmatch(read(t, path), -1)
		if len(matches) != 1 {
			t.Errorf("%s: want exactly one release asset URL, got %d", name, len(matches))
			continue
		}
		asset := matches[0][1]
		if i := sort.SearchStrings(want, asset); i == len(want) || want[i] != asset {
			t.Errorf("%s: asset %q is not one the release publishes %v", name, asset, want)
		}
		seen[asset] = true
	}
	for _, a := range want {
		if !seen[a] {
			t.Errorf("no package installs the published asset %q", a)
		}
	}
}

func TestPackagesAgreeOnVersion(t *testing.T) {
	cask := read(t, caskPath)
	pkgbuild := read(t, filepath.Join(aurDir, "PKGBUILD"))
	srcinfo := read(t, filepath.Join(aurDir, ".SRCINFO"))
	wdir := wingetDir(t)

	// A URL pinned to a literal tag would install an old release under a new version line.
	if !strings.Contains(cask, "releases/download/v#{version}/") {
		t.Error("homebrew cask URL does not use the version line")
	}
	if !strings.Contains(pkgbuild, "releases/download/v${pkgver}/") {
		t.Error("PKGBUILD source URL does not use pkgver")
	}

	versions := map[string]string{"winget directory": filepath.Base(wdir)}
	if m := caskVerRe.FindStringSubmatch(cask); m != nil {
		versions["homebrew cask"] = m[1]
	} else {
		t.Error("homebrew cask has no version line")
	}
	if m := regexp.MustCompile(`(?m)^pkgver=(\S+)$`).FindStringSubmatch(pkgbuild); m != nil {
		versions["AUR PKGBUILD"] = m[1]
	} else {
		t.Error("PKGBUILD has no pkgver")
	}
	if v := srcinfoValues(srcinfo, "pkgver"); len(v) == 1 {
		versions["AUR .SRCINFO"] = v[0]
	} else {
		t.Errorf(".SRCINFO: want one pkgver, got %v", v)
	}
	for i, src := range srcinfoValues(srcinfo, "source") {
		if m := urlVerRe.FindStringSubmatch(src); m != nil {
			versions[fmt.Sprintf(".SRCINFO source %d", i+1)] = m[1]
		}
	}
	for _, f := range []string{"GeiserX.CashPilotDesktop.yaml", "GeiserX.CashPilotDesktop.installer.yaml", "GeiserX.CashPilotDesktop.locale.en-US.yaml"} {
		body := read(t, filepath.Join(wdir, f))
		var doc struct {
			PackageVersion string `yaml:"PackageVersion"`
		}
		if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
			t.Fatal(err)
		}
		versions["winget "+f] = doc.PackageVersion
		if m := urlVerRe.FindStringSubmatch(body); m != nil {
			versions["winget "+f+" URL"] = m[1]
		}
	}

	ref := versions["winget directory"]
	for name, v := range versions {
		if v != ref {
			t.Errorf("%s says version %q, winget directory says %q", name, v, ref)
		}
	}
}

func TestChecksumsAreWellFormed(t *testing.T) {
	var sums []string
	if m := caskShaRe.FindStringSubmatch(read(t, caskPath)); m != nil {
		sums = append(sums, m[1])
	} else {
		t.Error("homebrew cask has no sha256 line")
	}

	var installer struct {
		Installers []struct {
			InstallerSha256 string `yaml:"InstallerSha256"`
		} `yaml:"Installers"`
	}
	if err := yaml.Unmarshal([]byte(read(t, filepath.Join(wingetDir(t), "GeiserX.CashPilotDesktop.installer.yaml"))), &installer); err != nil {
		t.Fatal(err)
	}
	for _, in := range installer.Installers {
		sums = append(sums, in.InstallerSha256)
	}

	pkgbuild := read(t, filepath.Join(aurDir, "PKGBUILD"))
	m := regexp.MustCompile(`(?s)\nsha256sums=\((.*?)\)`).FindStringSubmatch(pkgbuild)
	if m == nil {
		t.Fatal("PKGBUILD has no sha256sums array")
	}
	pkgSums := strings.Fields(strings.NewReplacer("'", " ", `"`, " ").Replace(m[1]))
	srcSums := srcinfoValues(read(t, filepath.Join(aurDir, ".SRCINFO")), "sha256sums")
	if strings.Join(pkgSums, ",") != strings.Join(srcSums, ",") {
		t.Errorf("PKGBUILD sha256sums %v differ from .SRCINFO %v; rerun packaging/render.sh", pkgSums, srcSums)
	}
	sums = append(sums, pkgSums...)

	if len(sums) != 5 {
		t.Errorf("want 5 checksums (cask, winget, 3 AUR sources), got %d", len(sums))
	}
	for _, s := range sums {
		if !sha256Re.MatchString(s) {
			t.Errorf("%q is not a sha256", s)
		}
	}

	// The .desktop file ships from this directory, so its checksum must match the file.
	desktop, err := os.ReadFile(filepath.Join(aurDir, "cashpilot-desktop.desktop"))
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(desktop)
	if got := hex.EncodeToString(h[:]); len(pkgSums) != 3 || pkgSums[2] != got {
		t.Errorf("PKGBUILD lists %v for cashpilot-desktop.desktop, file hashes to %s; rerun packaging/render.sh", pkgSums, got)
	}
}

// TestSrcinfoMatchesPKGBUILD checks every array .SRCINFO copies from the PKGBUILD,
// so a hand edit to one file that render.sh did not regenerate into the other fails.
func TestSrcinfoMatchesPKGBUILD(t *testing.T) {
	pkgbuild := read(t, filepath.Join(aurDir, "PKGBUILD"))
	srcinfo := read(t, filepath.Join(aurDir, ".SRCINFO"))
	m := regexp.MustCompile(`(?m)^pkgver=(\S+)$`).FindStringSubmatch(pkgbuild)
	if m == nil {
		t.Fatal("PKGBUILD has no pkgver")
	}
	expand := strings.NewReplacer("${pkgver}", m[1], "$pkgver", m[1])
	quoted := regexp.MustCompile(`'([^']*)'|"([^"]*)"`)
	for _, key := range []string{"depends", "optdepends", "provides", "conflicts", "options", "source"} {
		arr := regexp.MustCompile(`(?ms)^` + key + `=\((.*?)\)`).FindStringSubmatch(pkgbuild)
		if arr == nil {
			t.Errorf("PKGBUILD has no %s array", key)
			continue
		}
		var want []string
		for _, q := range quoted.FindAllStringSubmatch(arr[1], -1) {
			want = append(want, expand.Replace(q[1]+q[2]))
		}
		got := srcinfoValues(srcinfo, key)
		if len(want) == 0 || strings.Join(want, "|") != strings.Join(got, "|") {
			t.Errorf("PKGBUILD %s %q differ from .SRCINFO %q; rerun packaging/render.sh", key, want, got)
		}
	}
}
