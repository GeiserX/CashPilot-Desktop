// Command releasenotes writes the body of a GitHub release for people who want to
// install the app, not for the people who wrote it.
//
// GitHub's generated notes were a list of commit subjects, with the download files
// far below it. A reader of v1.6.0 saw "docs(design): record why desktop-only
// earners get one fenced Hyper-V VM" and looked for a Secure VM the app did not
// offer yet, and found no word on the two warnings Windows shows for an unsigned
// file. So the body now has three parts, in this order:
//
//  0. Before you upgrade, on a major release only: a link to the major's
//     section of UPGRADING.md and the breaking changes, so nobody upgrades
//     into one blind.
//  1. Download: which file to take for each system, the steps to run it, and the
//     two Windows warnings with what to click and why they appear.
//  2. What changed for you: only feat and fix commits, minus the scopes that are
//     not something a user can reach (see hiddenScopes).
//  3. Every commit subject, for developers, in a collapsed <details> block.
//
// Usage (the release workflow runs it after the builds, on the files it publishes):
//
//	go run ./cmd/releasenotes -tag v1.6.0 artifacts/desktop-*/*
//	go run ./cmd/releasenotes -tag v1.6.0 -prev v1.5.0 -to v1.6.0 a.exe b.zip
//	go run ./cmd/releasenotes -tag v1.7.0 -windows-signed=true artifacts/desktop-*/*
//
// -prev defaults to the newest v*.*.* tag reachable from -to, other than -tag.
// The asset arguments are only read for their file names.
//
// The command fails, and so stops the release, on a commit with no title text
// (a squash merge with an empty title leaves only " (#838)"). Main's history is
// never rewritten, so the way past such a commit is a line in titles.txt that
// gives it the subject it should have had.
package main

import (
	_ "embed"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Release is everything the notes are written from.
type Release struct {
	Repo    string   // owner/name on GitHub
	Tag     string   // the version being released, e.g. v1.6.0
	Prev    string   // the version before it, "" for the first release
	Commits []string // commit subjects, oldest first

	// Breaking holds the subjects of the commits that auto-tag counts as breaking
	// (see isBreaking), oldest first, merged branches included. On a major bump they are listed under
	// "Before you upgrade".
	Breaking []string
	Assets   []string // the file names the release publishes

	// WindowsSigned says the Windows files carry a code signature. The release
	// workflow sets it: the build signs them exactly when WINDOWS_SIGN_CERT is set.
	WindowsSigned bool
}

// hiddenScopes keeps a feat or fix out of "What changed for you".
//
// The first group is work on how the app is built, tested or released, which a
// user never sees. The second is a feature that exists in the code but that the
// app does not offer yet: listing it sends a reader looking for something that is
// not there. Remove a scope from the second group in the release that makes it
// reachable from the app.
var hiddenScopes = map[string]bool{
	"build": true, "ci": true, "deps": true, "docs": true, "e2e": true,
	"release": true, "test": true, "tests": true,

	// The Secure VM (Windows 11 Pro): the guard, the fence and the build steps
	// are in the code, but no screen offers the VM yet.
	"winvm": true,
}

var conventional = regexp.MustCompile(`^(feat|fix)(?:\(([^)]*)\))?!?:\s*(.+)$`)

// breakingLine is the test auto-tag.yml runs on every line of every commit's
// subject and body to decide on a major bump. Keep the two the same: a test
// reads auto-tag.yml and fails when they drift apart. auto-tag reads every
// commit, not only the first parents, and the raw subject, so isBreaking does too.
var breakingLine = regexp.MustCompile(`^BREAKING CHANGE:|^[a-z]+(\([^)]*\))?!:`)

// anyType is the "type(scope)!:" prefix of a conventional subject, any type.
var anyType = regexp.MustCompile(`^[a-z]+(?:\([^)]*\))?!?:`)

// prRef is a trailing pull request number, as a squash merge adds it.
var prRef = regexp.MustCompile(`\s*\(#\d+\)\s*$`)

// version reads the major number of a v1.2.3 tag.
var version = regexp.MustCompile(`^v(\d+)\.\d+\.\d+`)

// commit is one commit of the release. subject is git's %s as it is, leading
// space included, because that is the line auto-tag tests.
type commit struct {
	hash, subject, body string
}

// titlesFile gives a subject to a commit whose own subject has no title text.
// See the file for the format.
//
//go:embed titles.txt
var titlesFile string

// hashLine is a full commit hash.
var hashLine = regexp.MustCompile(`^[0-9a-f]{40}$`)

// parseTitles reads titlesFile into a map from full commit hash to subject.
func parseTitles(text string) (map[string]string, error) {
	titles := map[string]string{}
	for n, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		hash, subject, _ := strings.Cut(line, " ")
		subject = strings.TrimSpace(subject)
		if !hashLine.MatchString(hash) || !hasTitle(subject) {
			return nil, fmt.Errorf("titles.txt line %d: want a full 40-character commit hash, a space, then a subject with title text; got %q", n+1, line)
		}
		titles[hash] = subject
	}
	return titles, nil
}

// title is the subject the notes show for a commit: the one titles.txt gives
// it, or its own.
func title(c commit, titles map[string]string) string {
	if t, ok := titles[c.hash]; ok {
		return t
	}
	return strings.TrimSpace(c.subject)
}

// isBreaking says whether auto-tag would count this commit as breaking: a line
// of its subject or body is a "type!:" subject or a "BREAKING CHANGE:" footer.
func isBreaking(c commit) bool {
	for _, line := range strings.Split(c.subject+"\n"+c.body, "\n") {
		if breakingLine.MatchString(line) {
			return true
		}
	}
	return false
}

// hasTitle says whether a subject says anything once the "type(scope):" prefix
// and the trailing pull request numbers are gone. " (#838)" says nothing.
func hasTitle(subject string) bool {
	text := anyType.ReplaceAllString(strings.TrimSpace(subject), "")
	for {
		trimmed := prRef.ReplaceAllString(text, "")
		if trimmed == text {
			break
		}
		text = trimmed
	}
	return strings.IndexFunc(text, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0
}

// majorOf returns the major number of a v1.2.3 tag.
func majorOf(tag string) (int, bool) {
	m := version.FindStringSubmatch(tag)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

// majorBump returns the new major number when tag starts a major over prev. The
// first release (no prev) is not a bump: nobody upgrades into it.
func majorBump(tag, prev string) (int, bool) {
	to, ok1 := majorOf(tag)
	from, ok2 := majorOf(prev)
	if !ok1 || !ok2 || to <= from {
		return 0, false
	}
	return to, true
}

// checkTitles refuses a commit with no title text that titles.txt does not
// name. The error says how to get past it without rewriting main's history.
func checkTitles(commits []commit, titles map[string]string) error {
	for _, c := range commits {
		if _, ok := titles[c.hash]; !ok && !hasTitle(c.subject) {
			return fmt.Errorf("commit %s has no title text, only %q: a squash merge lost its title. "+
				"Do not rewrite main. In a pull request, add the line \"%s <the subject it should have had>\" to cmd/releasenotes/titles.txt, merge it, then run the release again", c.hash, c.subject, c.hash)
		}
	}
	return nil
}

// readable turns a subject into the words a user reads: no "type(scope)!:"
// prefix, first letter upper case.
func readable(subject string) string {
	return capitalize(strings.TrimSpace(anyType.ReplaceAllString(strings.TrimSpace(subject), "")))
}

// forPeople says whether a commit belongs in "What changed for you", under which
// heading, and in what words.
func forPeople(subject string) (heading, text string, ok bool) {
	m := conventional.FindStringSubmatch(strings.TrimSpace(subject))
	if m == nil {
		return "", "", false
	}
	for _, scope := range strings.Split(m[2], ",") {
		if hiddenScopes[strings.ToLower(strings.TrimSpace(scope))] {
			return "", "", false
		}
	}
	heading = "New"
	if m[1] == "fix" {
		heading = "Fixed"
	}
	return heading, capitalize(m[3]), true
}

func capitalize(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}

// Render writes the release body.
func Render(r Release) string {
	var b strings.Builder
	writeBeforeYouUpgrade(&b, r)
	writeDownloads(&b, r)
	writeChanges(&b, r)
	writeDeveloperList(&b, r)
	return b.String()
}

func (r Release) url(asset string) string {
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", r.Repo, r.Tag, asset)
}

func (r Release) link(asset string) string {
	return fmt.Sprintf("[%s](%s)", asset, r.url(asset))
}

// findAsset returns the first asset whose name ends with suffix.
func findAsset(assets []string, suffix string) string {
	for _, a := range assets {
		if strings.HasSuffix(a, suffix) {
			return a
		}
	}
	return ""
}

// upgradingLink is the path, with its anchor, of a major's section in
// UPGRADING.md. Each major has the heading "## <major>.0" there, and GitHub
// drops the dot from the anchor, so "## 2.0" is "#20".
func upgradingLink(major int) string {
	return fmt.Sprintf("UPGRADING.md#%d0", major)
}

// writeBeforeYouUpgrade opens the notes of a major release: what to read first,
// and every breaking change, so a "feat!:" does not hide under "New".
func writeBeforeYouUpgrade(b *strings.Builder, r Release) {
	major, ok := majorBump(r.Tag, r.Prev)
	if !ok {
		return
	}
	doc := upgradingLink(major)
	b.WriteString("## Before you upgrade\n\n")
	fmt.Fprintf(b, "This is a major release. Some changes need a step from you. Read [the %d.0 section of UPGRADING.md](https://github.com/%s/blob/%s/%s) before you upgrade from %s.\n", major, r.Repo, r.Tag, doc, r.Prev)
	if len(r.Breaking) > 0 {
		b.WriteString("\nThe changes that can break how you use the app today:\n\n")
		for _, subject := range r.Breaking {
			fmt.Fprintf(b, "- %s\n", readable(subject))
		}
	}
	b.WriteString("\n")
}

func writeDownloads(b *strings.Builder, r Release) {
	installer := findAsset(r.Assets, "-windows-amd64-setup.exe")
	portable := findAsset(r.Assets, "-windows-amd64.exe")
	mac := findAsset(r.Assets, "-darwin-arm64.zip")
	linux := findAsset(r.Assets, "-linux-amd64.tar.gz")

	b.WriteString("## Download\n\n")
	b.WriteString("Pick the file for your computer. The same files are under **Assets** at the bottom of this page.\n\n")
	switch {
	case installer != "" && portable != "":
		fmt.Fprintf(b, "- **Windows (64-bit):** %s is the installer. Use this one. %s is the portable app: it runs from any folder and installs nothing.\n", r.link(installer), r.link(portable))
	case installer != "":
		fmt.Fprintf(b, "- **Windows (64-bit):** %s, the installer.\n", r.link(installer))
	case portable != "":
		fmt.Fprintf(b, "- **Windows (64-bit):** %s. It runs from any folder and installs nothing.\n", r.link(portable))
	}
	if mac != "" {
		fmt.Fprintf(b, "- **macOS (Apple silicon):** %s\n", r.link(mac))
	}
	if linux != "" {
		fmt.Fprintf(b, "- **Linux (64-bit):** %s\n", r.link(linux))
	}

	if installer != "" || portable != "" {
		b.WriteString("\n### Install on Windows\n\n")
		b.WriteString("1. Download the file.\n")
		b.WriteString("2. Edge may say the file \"isn't commonly downloaded\". Open the **...** menu on that warning and choose **Keep**. Edge then shows a box that starts \"Make sure you trust\". Open the arrow next to **Delete** and choose **Keep anyway**.\n")
		b.WriteString("3. Open the file. The first time, Windows may say \"Windows protected your PC\". Click **More info**, then **Run anyway**.\n")
		switch {
		case installer != "":
			b.WriteString("4. Follow the installer. CashPilot Desktop is then in the Start menu, and you can remove it in Settings > Apps > Installed apps.\n")
		default:
			b.WriteString("4. The app opens. The file is the app, so keep it in a folder you will find again.\n")
		}
		if r.WindowsSigned {
			b.WriteString("\nWhy the warnings: Windows can warn about any new file that few people have downloaded yet, even a signed one. ")
		} else {
			b.WriteString("\nWhy the warnings: the Windows files are not code-signed yet. Windows warns about any unsigned file that few people have downloaded. ")
		}
		b.WriteString("To check that your file is the one this release built, compare it with `SHA256SUMS`, or run ")
		fmt.Fprintf(b, "`gh attestation verify <file> -R %s`.\n", r.Repo)
	}
	if mac != "" {
		b.WriteString("\n### Install on macOS\n\n")
		b.WriteString("1. Open the zip file, then drag CashPilot Desktop to Applications.\n")
		b.WriteString("2. Open CashPilot Desktop. The first time, macOS asks: \"CashPilot Desktop\" is an app downloaded from the Internet. Are you sure you want to open it? Click **Open**.\n")
		b.WriteString("\nThe app is signed and notarized by Apple. macOS still asks once for every app downloaded from the Internet.\n")
	}
	if linux != "" {
		b.WriteString("\n### Install on Linux\n\n")
		fmt.Fprintf(b, "Unpack it with `tar -xzf %s`, then run `./\"CashPilot Desktop\"`. It needs GTK 3 and WebKitGTK 4.1.\n", linux)
	}
}

func writeChanges(b *strings.Builder, r Release) {
	groups := map[string][]string{}
	for _, subject := range r.Commits {
		if heading, text, ok := forPeople(subject); ok {
			groups[heading] = append(groups[heading], text)
		}
	}
	b.WriteString("\n## What changed for you\n\n")
	if len(groups) == 0 {
		b.WriteString("Nothing you will notice. This release changes how the app is built, tested or documented.\n")
		return
	}
	first := true
	for _, heading := range []string{"New", "Fixed"} {
		if len(groups[heading]) == 0 {
			continue
		}
		if !first {
			b.WriteString("\n")
		}
		first = false
		fmt.Fprintf(b, "### %s\n\n", heading)
		for _, text := range groups[heading] {
			fmt.Fprintf(b, "- %s\n", text)
		}
	}
}

func writeDeveloperList(b *strings.Builder, r Release) {
	b.WriteString("\n<details>\n<summary>Every change in this release, for developers</summary>\n\n")
	for _, subject := range r.Commits {
		fmt.Fprintf(b, "- %s\n", subject)
	}
	if r.Prev != "" {
		fmt.Fprintf(b, "\n**Full Changelog**: https://github.com/%s/compare/%s...%s\n", r.Repo, r.Prev, r.Tag)
	}
	b.WriteString("\n</details>\n")
}

func git(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// previousTag is the newest version tag reachable from to, other than tag itself
// (a tag pushed by hand already exists when the notes are written).
func previousTag(tag, to string) (string, error) {
	out, err := git("tag", "-l", "v*.*.*", "--merged", to, "--sort=-v:refname")
	if err != nil {
		return "", err
	}
	for _, t := range strings.Fields(out) {
		if t != tag {
			return t, nil
		}
	}
	return "", nil
}

// commits reads the release's commits, oldest first. An empty subject stays in
// the list, so checkTitles can refuse it. firstParent reads one commit per pull
// request, as main is squash-merged; without it the list also holds the commits
// of a merged branch, which auto-tag reads too.
func commits(prev, to string, firstParent bool) ([]commit, error) {
	rng := to
	if prev != "" {
		rng = prev + ".." + to
	}
	args := []string{"log", "--reverse", "--format=%H%x1f%s%x1f%b%x1e"}
	if firstParent {
		args = append(args, "--first-parent")
	}
	out, err := git(append(args, rng)...)
	if err != nil {
		return nil, err
	}
	var list []commit
	for _, record := range strings.Split(out, "\x1e") {
		record = strings.TrimLeft(record, "\n")
		if record == "" {
			continue
		}
		fields := strings.SplitN(record, "\x1f", 3)
		if len(fields) != 3 {
			return nil, fmt.Errorf("git log: unexpected record %q", record)
		}
		list = append(list, commit{hash: fields[0], subject: fields[1], body: fields[2]})
	}
	return list, nil
}

func run(args []string) (string, error) {
	fs := flag.NewFlagSet("releasenotes", flag.ContinueOnError)
	tag := fs.String("tag", "", "the version being released, e.g. v1.6.0 (required)")
	prev := fs.String("prev", "", "the previous version (default: the newest earlier v*.*.* tag reachable from -to)")
	to := fs.String("to", "HEAD", "the commit the release is built from")
	repo := fs.String("repo", "GeiserX/CashPilot-Desktop", "owner/name on GitHub")
	windowsSigned := fs.Bool("windows-signed", false, "the Windows files are code-signed")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if *tag == "" {
		return "", fmt.Errorf("-tag is required")
	}
	r := Release{Repo: *repo, Tag: *tag, Prev: *prev, WindowsSigned: *windowsSigned}
	if r.Prev == "" {
		p, err := previousTag(r.Tag, *to)
		if err != nil {
			return "", err
		}
		r.Prev = p
	}
	titles, err := parseTitles(titlesFile)
	if err != nil {
		return "", err
	}
	list, err := commits(r.Prev, *to, true)
	if err != nil {
		return "", err
	}
	if err := checkTitles(list, titles); err != nil {
		return "", err
	}
	for _, c := range list {
		r.Commits = append(r.Commits, title(c, titles))
	}
	all, err := commits(r.Prev, *to, false)
	if err != nil {
		return "", err
	}
	for _, c := range all {
		if isBreaking(c) {
			r.Breaking = append(r.Breaking, title(c, titles))
		}
	}
	for _, a := range fs.Args() {
		r.Assets = append(r.Assets, filepath.Base(a))
	}
	return Render(r), nil
}

func main() {
	out, err := run(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "releasenotes:", err)
		os.Exit(1)
	}
	fmt.Print(out)
}
