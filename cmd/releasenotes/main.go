// Command releasenotes writes the body of a GitHub release for people who want to
// install the app, not for the people who wrote it.
//
// GitHub's generated notes were a list of commit subjects, with the download files
// far below it. A reader of v1.6.0 saw "docs(design): record why desktop-only
// earners get one fenced Hyper-V VM" and looked for a Secure VM the app did not
// offer yet, and found no word on the two warnings Windows shows for an unsigned
// file. So the body now has three parts, in this order:
//
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
//
// -prev defaults to the newest v*.*.* tag reachable from -to, other than -tag.
// The asset arguments are only read for their file names.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	Assets  []string // the file names the release publishes
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
		b.WriteString("2. Edge may say the file \"isn't commonly downloaded\". Click the arrow next to **Delete** and choose **Keep anyway**. Some Edge versions put it under **...** > **Keep**, then **Show more** > **Keep anyway**.\n")
		b.WriteString("3. Open the file. If Windows says \"Windows protected your PC\", click **More info**, then **Run anyway**.\n")
		switch {
		case installer != "":
			b.WriteString("4. Follow the installer. CashPilot Desktop is then in the Start menu, and you can remove it in Settings > Apps > Installed apps.\n")
		default:
			b.WriteString("4. The app opens. The file is the app, so keep it in a folder you will find again.\n")
		}
		b.WriteString("\nWhy the warnings: the Windows files are not code-signed yet. Windows warns about any unsigned file that few people have downloaded. ")
		b.WriteString("To check that your file is the one this release built, compare it with `SHA256SUMS`, or run ")
		fmt.Fprintf(b, "`gh attestation verify <file> -R %s`.\n", r.Repo)
	}
	if mac != "" {
		b.WriteString("\n### Install on macOS\n\n")
		b.WriteString("Open the zip file, then drag CashPilot Desktop to Applications. The app is signed and notarized by Apple, so macOS opens it without a warning.\n")
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

func subjects(prev, to string) ([]string, error) {
	rng := to
	if prev != "" {
		rng = prev + ".." + to
	}
	// First parent only: main is squash-merged, so each line is one pull request.
	out, err := git("log", "--first-parent", "--reverse", "--format=%s", rng)
	if err != nil {
		return nil, err
	}
	var list []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			list = append(list, line)
		}
	}
	return list, nil
}

func run(args []string) (string, error) {
	fs := flag.NewFlagSet("releasenotes", flag.ContinueOnError)
	tag := fs.String("tag", "", "the version being released, e.g. v1.6.0 (required)")
	prev := fs.String("prev", "", "the previous version (default: the newest earlier v*.*.* tag reachable from -to)")
	to := fs.String("to", "HEAD", "the commit the release is built from")
	repo := fs.String("repo", "GeiserX/CashPilot-Desktop", "owner/name on GitHub")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if *tag == "" {
		return "", fmt.Errorf("-tag is required")
	}
	r := Release{Repo: *repo, Tag: *tag, Prev: *prev}
	if r.Prev == "" {
		p, err := previousTag(r.Tag, *to)
		if err != nil {
			return "", err
		}
		r.Prev = p
	}
	commits, err := subjects(r.Prev, *to)
	if err != nil {
		return "", err
	}
	r.Commits = commits
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
