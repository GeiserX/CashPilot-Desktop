package main

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"
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

// A squash merge with an empty title leaves a subject like " (#838)". It used to
// drop out of the notes without a word; now it has no title and stops the release.
func TestHasTitle(t *testing.T) {
	for _, c := range []struct {
		subject string
		want    bool
	}{
		{"fix: keep the key off the page (#332)", true},
		{"feat(fleet)!: drop the old pairing (#9)", true},
		{"Merge branch 'main' into x", true},
		{"chore: v2 (#7)", true},
		{" (#838)", false},
		{"(#838)", false},
		{"feat: (#838)", false},
		{"fix(fleet)!: (#838) (#839)", false},
		{"feat:", false},
		{"", false},
		{"  ", false},
	} {
		if got := hasTitle(c.subject); got != c.want {
			t.Errorf("hasTitle(%q) = %v, want %v", c.subject, got, c.want)
		}
	}
}

func TestIsBreaking(t *testing.T) {
	for _, c := range []struct {
		c    commit
		want bool
	}{
		{commit{subject: "feat!: drop the old pairing (#9)"}, true},
		{commit{subject: "fix(fleet)!: refuse the old key (#10)"}, true},
		{commit{subject: "refactor(app)!: move the config (#11)"}, true},
		{commit{subject: "feat(app): move the config (#12)", body: "Longer text.\n\nBREAKING CHANGE: config.json moved\n"}, true},
		{commit{subject: "feat(fleet): drop the old pairing (#13)"}, false},
		{commit{subject: "fix: say BREAKING CHANGE: in the middle (#14)", body: "It mentions BREAKING CHANGE: but not as a footer.\n"}, false},
		{commit{subject: "docs: explain feat!: subjects (#15)"}, false},
		// auto-tag greps the raw %s line, so a leading space stops the match.
		{commit{subject: " feat!: drop the old pairing (#16)"}, false},
	} {
		if got := isBreaking(c.c); got != c.want {
			t.Errorf("isBreaking(%+v) = %v, want %v", c.c, got, c.want)
		}
	}
}

// auto-tag.yml decides on a major bump with this same test. If the two drift, a
// release can be major with no breaking change listed, or the other way round.
func TestBreakingLineIsTheOneAutoTagUses(t *testing.T) {
	workflow, err := os.ReadFile("../../.github/workflows/auto-tag.yml")
	if err != nil {
		t.Fatal(err)
	}
	want := "grep -qE '" + breakingLine.String() + "'"
	if !strings.Contains(string(workflow), want) {
		t.Fatalf("auto-tag.yml no longer runs %s; change breakingLine to match it", want)
	}
}

func TestAMajorReleaseOpensWithBeforeYouUpgrade(t *testing.T) {
	r := Release{
		Repo: "GeiserX/CashPilot-Desktop", Tag: "v2.0.0", Prev: "v1.7.0", Assets: allAssets,
		Commits:  []string{"feat(fleet)!: drop the old pairing (#9)", "fix: keep the key off the page (#332)", "feat(app): move the config (#12)"},
		Breaking: []string{"feat(fleet)!: drop the old pairing (#9)", "feat(app): move the config (#12)"},
	}
	body := Render(r)
	if !strings.HasPrefix(body, "## Before you upgrade\n") {
		t.Fatalf("a major release must open with Before you upgrade:\n%s", body)
	}
	before := section(t, body, "## Before you upgrade", "## Download")
	for _, want := range []string{
		"[the 2.0 section of UPGRADING.md](https://github.com/GeiserX/CashPilot-Desktop/blob/v2.0.0/UPGRADING.md#20)",
		"before you upgrade from v1.7.0",
		"- Drop the old pairing (#9)\n",
		"- Move the config (#12)\n",
	} {
		if !strings.Contains(before, want) {
			t.Errorf("Before you upgrade lacks %q:\n%s", want, before)
		}
	}
	if strings.Contains(before, "Keep the key off the page") {
		t.Errorf("Before you upgrade must list only the breaking changes:\n%s", before)
	}
}

func TestAMajorReleaseWithNoBreakingCommitStillLinksTheGuide(t *testing.T) {
	body := Render(Release{Repo: "GeiserX/CashPilot-Desktop", Tag: "v3.0.0", Prev: "v2.4.1", Commits: v160, Assets: allAssets})
	before := section(t, body, "## Before you upgrade", "## Download")
	if !strings.Contains(before, "UPGRADING.md#30)") || strings.Contains(before, "- ") {
		t.Errorf("want the guide link and no list:\n%s", before)
	}
}

func TestOnlyAMajorBumpGetsBeforeYouUpgrade(t *testing.T) {
	for _, c := range []struct{ tag, prev string }{
		{"v1.6.0", "v1.5.0"},
		{"v1.6.1", "v1.6.0"},
		{"v1.0.0", ""},
		{"v9.9.9", "HEAD"},
	} {
		body := Render(Release{Repo: "GeiserX/CashPilot-Desktop", Tag: c.tag, Prev: c.prev, Commits: v160, Breaking: v160[:1], Assets: allAssets})
		if strings.Contains(body, "Before you upgrade") {
			t.Errorf("%s after %q is not a major bump, but the notes say Before you upgrade:\n%s", c.tag, c.prev, body)
		}
	}
}

// gitRepo makes a repository in a temporary folder with a v1.7.0 tag, adds one
// empty commit per message, and moves the test into it. It returns a function
// that runs git there and returns its output. The user's own git config stays
// out, so a global hook or template cannot change what the test sees.
func gitRepo(t *testing.T, messages ...string) func(args ...string) string {
	t.Helper()
	t.Chdir(t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	g := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	g("init", "-q")
	g("commit", "-q", "--allow-empty", "-m", "feat: the first release (#1)")
	g("tag", "v1.7.0")
	for _, m := range messages {
		g("commit", "-q", "--allow-empty", "--allow-empty-message", "--cleanup=verbatim", "-m", m)
	}
	return g
}

// withTitles stands in text for the shipped titles.txt for one test.
func withTitles(t *testing.T, text string) {
	t.Helper()
	old := titlesFile
	titlesFile = text
	t.Cleanup(func() { titlesFile = old })
}

func TestRunListsTheBreakingCommitsOfAMajorRelease(t *testing.T) {
	gitRepo(t,
		"feat(fleet)!: drop the old pairing (#9)",
		"fix: keep the key off the page (#332)",
		"feat(app): move the config (#12)\n\nBREAKING CHANGE: config.json moved to the app folder",
		" feat!: a subject auto-tag does not match (#13)",
	)
	out, err := run([]string{"-tag", "v2.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	before := section(t, out, "## Before you upgrade", "## Download")
	for _, want := range []string{"UPGRADING.md#20)", "from v1.7.0", "- Drop the old pairing (#9)\n", "- Move the config (#12)\n"} {
		if !strings.Contains(before, want) {
			t.Errorf("Before you upgrade lacks %q:\n%s", want, before)
		}
	}
	for _, gone := range []string{"Keep the key", "auto-tag does not match"} {
		if strings.Contains(before, gone) {
			t.Errorf("only what auto-tag counts as breaking belongs here, not %q:\n%s", gone, before)
		}
	}
}

// auto-tag reads every commit, so a breaking commit inside a pull request merged
// with a merge commit makes the release major. The notes must list it too.
func TestRunListsABreakingCommitInsideAMergedBranch(t *testing.T) {
	g := gitRepo(t)
	g("checkout", "-q", "-b", "pairing")
	g("commit", "-q", "--allow-empty", "-m", "feat(fleet)!: drop the old pairing")
	g("checkout", "-q", "main")
	g("merge", "-q", "--no-ff", "-m", "Merge pull request #5 from x/pairing", "pairing")
	out, err := run([]string{"-tag", "v2.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	before := section(t, out, "## Before you upgrade", "## Download")
	if !strings.Contains(before, "- Drop the old pairing\n") {
		t.Errorf("Before you upgrade lacks the merged branch's breaking commit:\n%s", before)
	}
	devs := section(t, out, "<details>", "</details>")
	if strings.Contains(devs, "drop the old pairing") {
		t.Errorf("the developer list holds one line per pull request, not the branch's commits:\n%s", devs)
	}
}

func TestRunFailsOnACommitWithNoTitle(t *testing.T) {
	for _, subject := range []string{" (#838)", "feat: (#838)", ""} {
		t.Run(subject, func(t *testing.T) {
			g := gitRepo(t, "fix: keep the key off the page (#332)", subject)
			hash := g("rev-parse", "HEAD")
			out, err := run([]string{"-tag", "v1.7.1"})
			if err == nil {
				t.Fatalf("want a no-title error, got none:\n%s", out)
			}
			// The error names the full hash to copy, the raw subject to search
			// for, and the way out that leaves main's history alone.
			for _, want := range []string{"has no title text", hash, fmt.Sprintf("%q", subject), "cmd/releasenotes/titles.txt"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the error lacks %q: %v", want, err)
				}
			}
		})
	}
}

// A commit with no title can never be renamed on main. A line in titles.txt
// gives it the subject to show, so the release goes on; another commit with no
// title that the file does not name still stops it.
func TestRunTakesTheTitleOfAnUntitledCommitFromTitlesTxt(t *testing.T) {
	g := gitRepo(t, " (#838)", "fix: a later fix (#839)")
	untitled := g("rev-parse", "HEAD~1")
	withTitles(t, "# a comment\n\n"+untitled+" fix(fleet): keep the exits when a reply drops the list (#838)\n")
	out, err := run([]string{"-tag", "v1.7.1"})
	if err != nil {
		t.Fatal(err)
	}
	fixed := section(t, out, "### Fixed", "<details>")
	for _, want := range []string{"- Keep the exits when a reply drops the list (#838)\n", "- A later fix (#839)\n"} {
		if !strings.Contains(fixed, want) {
			t.Errorf("What changed for you lacks %q:\n%s", want, fixed)
		}
	}
	devs := section(t, out, "<details>", "</details>")
	if !strings.Contains(devs, "- fix(fleet): keep the exits when a reply drops the list (#838)\n") || strings.Contains(devs, "- (#838)") {
		t.Errorf("the developer list must show the title from titles.txt:\n%s", devs)
	}

	g("commit", "-q", "--allow-empty", "--cleanup=verbatim", "-m", " (#840)")
	if _, err := run([]string{"-tag", "v1.7.1"}); err == nil || !strings.Contains(err.Error(), g("rev-parse", "HEAD")) {
		t.Fatalf("an untitled commit titles.txt does not name must still fail, got %v", err)
	}
}

func TestParseTitles(t *testing.T) {
	hash := strings.Repeat("ab", 20)
	got, err := parseTitles("# comment\n\n  " + hash + "   fix: the real title (#838)  \n")
	if err != nil || len(got) != 1 || got[hash] != "fix: the real title (#838)" {
		t.Fatalf("parseTitles = %v, %v", got, err)
	}
	for _, bad := range []string{
		"abc1234 fix: a short hash (#1)",
		hash,
		hash + " (#838)",
		strings.ToUpper(hash) + " fix: upper case (#1)",
	} {
		if _, err := parseTitles(bad); err == nil {
			t.Errorf("parseTitles(%q) must fail", bad)
		}
	}
}

// The release runs the tool with the titles.txt it embeds; a bad line there
// would stop every release.
func TestTheShippedTitlesFileParses(t *testing.T) {
	if _, err := parseTitles(titlesFile); err != nil {
		t.Fatal(err)
	}
}

// githubAnchor is the anchor GitHub gives a Markdown heading: lower case, spaces
// to hyphens, and every other character but letters, digits, hyphens and
// underscores dropped. A repeated heading gets "-1", "-2" and so on.
func githubAnchor(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(heading)) {
		switch {
		case r == ' ':
			b.WriteRune('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

// upgradingAnchors lists the anchors of every heading in UPGRADING.md.
func upgradingAnchors(t *testing.T) map[string]bool {
	t.Helper()
	text, err := os.ReadFile("../../UPGRADING.md")
	if err != nil {
		t.Fatal(err)
	}
	anchors := map[string]bool{}
	seen := map[string]int{}
	inFence := false
	for _, line := range strings.Split(string(text), "\n") {
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
		}
		if inFence || !strings.HasPrefix(line, "#") {
			continue
		}
		heading := strings.TrimLeft(line, "#")
		if !strings.HasPrefix(heading, " ") {
			continue
		}
		a := githubAnchor(heading)
		if n := seen[a]; n > 0 {
			anchors[fmt.Sprintf("%s-%d", a, n)] = true
		} else {
			anchors[a] = true
		}
		seen[a]++
	}
	return anchors
}

// currentMajor reads the major version from package.json, the version file the
// release bumps.
func currentMajor(t *testing.T) int {
	t.Helper()
	text, err := os.ReadFile("../../package.json")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`"version":\s*"(\d+)\.`).FindSubmatch(text)
	if m == nil {
		t.Fatal("package.json has no version")
	}
	major, err := strconv.Atoi(string(m[1]))
	if err != nil {
		t.Fatal(err)
	}
	return major
}

// The release notes of a major link its section of UPGRADING.md. A missing
// section would send every reader to the top of the page, so it fails here.
func TestUpgradingHasASectionForEveryMajorTheNotesLink(t *testing.T) {
	anchors := upgradingAnchors(t)
	majors := map[int]bool{1: true, 2: true, currentMajor(t): true}
	for major := range majors {
		link := upgradingLink(major)
		anchor := link[strings.Index(link, "#")+1:]
		if !anchors[anchor] {
			t.Errorf("the notes of v%d.0.0 link %s, but UPGRADING.md has no heading \"## %d.0\"", major, link, major)
		}
	}
}

func TestGithubAnchorDropsTheDotOfAVersion(t *testing.T) {
	for heading, want := range map[string]string{
		" 2.0":                              "20",
		" Before you upgrade to 2.0":        "before-you-upgrade-to-20",
		" Earners can't reach your network": "earners-cant-reach-your-network",
	} {
		if got := githubAnchor(heading); got != want {
			t.Errorf("githubAnchor(%q) = %q, want %q", heading, got, want)
		}
	}
}
