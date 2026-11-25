package main

import (
	"strings"
	"testing"
)

// The v1.5.0..v1.6.0 subjects, as they are on main, plus the kinds of commit a
// user never sees.
var v160 = []string{
	"fix(fence): keep the fleet's exits when a reply drops the list, so a rolled-back server cannot open the other lines (#323)",
	"feat(first-run): ask whether this computer runs everything or joins your CashPilot, as the docs promise (#326)",
	"docs(design): record why desktop-only earners get one fenced Hyper-V VM, and which apps may use it (#328)",
	"feat(winvm): decide the Secure VM's fence in pure Go so every rule is tested on every OS (#331)",
	"ci: run the render checks on every pull request (#900)",
	"fix(ci): stop the release job reading the wrong tag (#901)",
	"chore(deps): bump wails (#902)",
	"fix: keep the fleet key off the Fleet page (#332)",
}

var allAssets = []string{
	"CashPilot-Desktop-darwin-arm64.zip",
	"CashPilot-Desktop-linux-amd64.tar.gz",
	"CashPilot-Desktop-windows-amd64-setup.exe",
	"CashPilot-Desktop-windows-amd64.exe",
	"SHA256SUMS",
}

func render(commits, assets []string) string {
	return Render(Release{Repo: "GeiserX/CashPilot-Desktop", Tag: "v1.6.0", Prev: "v1.5.0", Commits: commits, Assets: assets})
}

// section returns the text between start and the first of the ends after it.
func section(t *testing.T, body, start string, ends ...string) string {
	t.Helper()
	i := strings.Index(body, start)
	if i < 0 {
		t.Fatalf("no %q in:\n%s", start, body)
	}
	rest := body[i+len(start):]
	for _, end := range ends {
		if j := strings.Index(rest, end); j >= 0 {
			rest = rest[:j]
		}
	}
	return rest
}

func TestThePeoplePartComesFirstAndTheDeveloperListLast(t *testing.T) {
	body := render(v160, allAssets)
	download := strings.Index(body, "## Download")
	changes := strings.Index(body, "## What changed for you")
	details := strings.Index(body, "<details>")
	if download != 0 {
		t.Fatalf("the body must open with the download part, got:\n%s", body)
	}
	if !(download < changes && changes < details) {
		t.Fatalf("want Download, then What changed for you, then <details>; got %d, %d, %d:\n%s", download, changes, details, body)
	}
	if !strings.HasSuffix(strings.TrimSpace(body), "</details>") {
		t.Fatalf("the developer list must close the body:\n%s", body)
	}
}

func TestWhatChangedForYouListsOnlyWhatAUserCanReach(t *testing.T) {
	body := render(v160, allAssets)
	forYou := section(t, body, "## What changed for you", "<details>")
	for _, want := range []string{
		"### New",
		"- Ask whether this computer runs everything or joins your CashPilot, as the docs promise (#326)",
		"### Fixed",
		"- Keep the fleet's exits when a reply drops the list, so a rolled-back server cannot open the other lines (#323)",
		"- Keep the fleet key off the Fleet page (#332)",
	} {
		if !strings.Contains(forYou, want) {
			t.Errorf("What changed for you lacks %q:\n%s", want, forYou)
		}
	}
	for _, gone := range []string{"#328", "Hyper-V", "#331", "Secure VM", "#900", "#901", "#902", "docs(", "feat(", "fix("} {
		if strings.Contains(forYou, gone) {
			t.Errorf("What changed for you must not show %q:\n%s", gone, forYou)
		}
	}
	devs := section(t, body, "<details>", "</details>")
	for _, subject := range v160 {
		if !strings.Contains(devs, "- "+subject+"\n") {
			t.Errorf("the developer list lacks %q:\n%s", subject, devs)
		}
	}
	if !strings.Contains(devs, "https://github.com/GeiserX/CashPilot-Desktop/compare/v1.5.0...v1.6.0") {
		t.Errorf("the developer list lacks the compare link:\n%s", devs)
	}
}

func TestEveryFileIsLinkedAndTheInstallerIsTheOneToUse(t *testing.T) {
	body := render(v160, allAssets)
	download := section(t, body, "## Download", "## What changed for you")
	for _, a := range allAssets[:4] {
		if !strings.Contains(download, "https://github.com/GeiserX/CashPilot-Desktop/releases/download/v1.6.0/"+a) {
			t.Errorf("no download link for %s:\n%s", a, download)
		}
	}
	if !strings.Contains(download, "-setup.exe) is the installer. Use this one.") {
		t.Errorf("the installer is not named as the file to use:\n%s", download)
	}
	if !strings.Contains(download, "is the portable app") {
		t.Errorf("the portable file is not explained:\n%s", download)
	}
	for _, want := range []string{
		"Open the **...** menu on that warning and choose **Keep**",
		"\"Make sure you trust\". Open the arrow next to **Delete** and choose **Keep anyway**",
		"Click **More info**, then **Run anyway**",
		"not code-signed yet",
		"Settings > Apps > Installed apps",
	} {
		if !strings.Contains(download, want) {
			t.Errorf("the Windows steps lack %q:\n%s", want, download)
		}
	}
}

// v1.6.0 shipped one Windows file, the portable exe. The notes must not point to
// an installer that is not there.
func TestOnlyThePortableExe(t *testing.T) {
	body := render(v160, []string{"CashPilot-Desktop-windows-amd64.exe", "SHA256SUMS"})
	download := section(t, body, "## Download", "## What changed for you")
	for _, gone := range []string{"installer", "setup.exe", "Installed apps", "macOS", "Linux"} {
		if strings.Contains(download, gone) {
			t.Errorf("a release without that file must not mention %q:\n%s", gone, download)
		}
	}
	for _, want := range []string{"CashPilot-Desktop-windows-amd64.exe", "installs nothing", "**Keep anyway**", "**Run anyway**", "keep it in a folder"} {
		if !strings.Contains(download, want) {
			t.Errorf("the portable-only notes lack %q:\n%s", want, download)
		}
	}
}

func TestAReleaseWithNoUserChangeSaysSo(t *testing.T) {
	body := render([]string{"ci: pin an action (#1)", "docs(design): write down a decision (#2)", "feat(winvm): add a fence rule (#3)"}, allAssets)
	forYou := section(t, body, "## What changed for you", "<details>")
	if !strings.Contains(forYou, "Nothing you will notice.") || strings.Contains(forYou, "###") {
		t.Fatalf("want a plain 'nothing' line and no headings:\n%s", forYou)
	}
}

func TestForPeople(t *testing.T) {
	cases := []struct {
		subject, heading, text string
		ok                     bool
	}{
		{"feat(alerts): catch a stalled copy (#330)", "New", "Catch a stalled copy (#330)", true},
		{"fix: keep the key off the page (#332)", "Fixed", "Keep the key off the page (#332)", true},
		{"feat(fleet)!: drop the old pairing (#9)", "New", "Drop the old pairing (#9)", true},
		{"feat(ui,winvm): show the VM card (#10)", "", "", false},
		{"fix(CI): flaky test (#11)", "", "", false},
		{"refactor(app): split a file (#12)", "", "", false},
		{"Merge branch 'main' into x", "", "", false},
	}
	for _, c := range cases {
		heading, text, ok := forPeople(c.subject)
		if heading != c.heading || text != c.text || ok != c.ok {
			t.Errorf("forPeople(%q) = %q, %q, %v; want %q, %q, %v", c.subject, heading, text, ok, c.heading, c.text, c.ok)
		}
	}
}

// The build signs the Windows files when the signing secret is set. A signed
// release must not tell people the files are unsigned, and an unsigned one must.
func TestTheUnsignedLineFollowsTheSignature(t *testing.T) {
	r := Release{Repo: "GeiserX/CashPilot-Desktop", Tag: "v1.7.0", Prev: "v1.6.0", Commits: v160, Assets: allAssets}
	unsigned := section(t, Render(r), "## Download", "## What changed for you")
	if !strings.Contains(unsigned, "the Windows files are not code-signed yet") {
		t.Errorf("an unsigned release must say so:\n%s", unsigned)
	}
	r.WindowsSigned = true
	signed := section(t, Render(r), "## Download", "## What changed for you")
	if strings.Contains(signed, "not code-signed") || strings.Contains(signed, "unsigned") {
		t.Errorf("a signed release must not say its files are unsigned:\n%s", signed)
	}
	for _, want := range []string{"even a signed one", "**Keep anyway**", "**Run anyway**"} {
		if !strings.Contains(signed, want) {
			t.Errorf("the signed notes lack %q:\n%s", want, signed)
		}
	}
}

// A notarized app still gets Gatekeeper's first-open question. The notes must
// say so and name the button, not promise that no warning appears.
func TestMacOSNamesTheFirstOpenQuestion(t *testing.T) {
	mac := section(t, render(v160, allAssets), "### Install on macOS", "### Install on Linux")
	for _, want := range []string{"is an app downloaded from the Internet. Are you sure you want to open it?", "Click **Open**", "notarized"} {
		if !strings.Contains(mac, want) {
			t.Errorf("the macOS steps lack %q:\n%s", want, mac)
		}
	}
	if strings.Contains(mac, "without a warning") {
		t.Errorf("the macOS steps must not promise no warning:\n%s", mac)
	}
}

// The workflow passes -windows-signed; the flag must reach the notes.
func TestTheSignedFlagReachesTheNotes(t *testing.T) {
	for _, c := range []struct {
		flag     string
		unsigned bool
	}{{"-windows-signed=true", false}, {"-windows-signed=false", true}} {
		out, err := run([]string{"-tag", "v9.9.9", "-prev", "HEAD", "-to", "HEAD", c.flag, "x/CashPilot-Desktop-windows-amd64.exe"})
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(out, "not code-signed yet"); got != c.unsigned {
			t.Errorf("%s: says unsigned = %v, want %v:\n%s", c.flag, got, c.unsigned, out)
		}
	}
}
