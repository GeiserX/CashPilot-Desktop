package catalog

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// guestProblems lists every way a guest: block breaks the Secure VM's rules
// (docs/design/secure-windows-vm.md). The worst one is a guest block on a service
// whose provider forbids VMs: offering it in the VM is the violation.
func guestProblems(services []Service) []string {
	var problems []string
	for _, svc := range services {
		if svc.Guest == nil {
			continue
		}
		if svc.Requirements.ContainerProhibited {
			problems = append(problems, fmt.Sprintf("%s: container_prohibited forbids VMs, so it must not have a guest block", svc.Slug))
		}
		if r := svc.Guest.Refused; r != "" {
			if r != "terms" && r != "unsigned" {
				problems = append(problems, fmt.Sprintf("%s: guest.refused %q is not terms or unsigned", svc.Slug, r))
			}
			if svc.Guest.Windows != nil || svc.Guest.MacOS != nil {
				problems = append(problems, fmt.Sprintf("%s: a refused guest block must name no OS", svc.Slug))
			}
			continue
		}
		if svc.Guest.Windows == nil && svc.Guest.MacOS == nil {
			problems = append(problems, fmt.Sprintf("%s: guest block has no windows or macos entry", svc.Slug))
			continue
		}
		if m := svc.Guest.MacOS; m != nil {
			problems = append(problems, macGuestProblems(svc.Slug, m)...)
		}
		w := svc.Guest.Windows
		if w == nil {
			continue
		}
		if strings.TrimSpace(w.Process) == "" {
			problems = append(problems, fmt.Sprintf("%s: guest.windows.process is empty", svc.Slug))
		}
		if strings.TrimSpace(w.Publisher) == "" {
			problems = append(problems, fmt.Sprintf("%s: guest.windows.publisher is empty", svc.Slug))
		}
		if u, err := url.Parse(w.DownloadPage); err != nil || u.Scheme != "https" || u.Host == "" {
			problems = append(problems, fmt.Sprintf("%s: guest.windows.download_page %q is not an HTTPS URL", svc.Slug, w.DownloadPage))
		}
		if w.Signin != "console" && w.Signin != "none" {
			problems = append(problems, fmt.Sprintf("%s: guest.windows.signin %q is not console or none", svc.Slug, w.Signin))
		}
	}
	return problems
}

var teamID = regexp.MustCompile(`^[A-Z0-9]{10}$`)
var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

func macGuestProblems(slug string, m *GuestMacOS) []string {
	var p []string
	if !strings.HasSuffix(m.App, ".app") || strings.Contains(m.App, "/") {
		p = append(p, fmt.Sprintf("%s: guest.macos.app %q is not a bundle name", slug, m.App))
	}
	if strings.TrimSpace(m.Process) == "" {
		p = append(p, fmt.Sprintf("%s: guest.macos.process is empty", slug))
	}
	if !teamID.MatchString(m.TeamID) {
		p = append(p, fmt.Sprintf("%s: guest.macos.team_id %q is not a Team ID", slug, m.TeamID))
	}
	if u, err := url.Parse(m.Download); err != nil || u.Scheme != "https" || u.Host == "" {
		p = append(p, fmt.Sprintf("%s: guest.macos.download %q is not an HTTPS URL", slug, m.Download))
	}
	if m.SHA256 != "" && !sha256Hex.MatchString(m.SHA256) {
		p = append(p, fmt.Sprintf("%s: guest.macos.sha256 is not 64 hex digits", slug))
	}
	if m.Signin != "console" && m.Signin != "none" {
		p = append(p, fmt.Sprintf("%s: guest.macos.signin %q is not console or none", slug, m.Signin))
	}
	mk := m.SignedIn
	switch {
	case mk.Process && (mk.File != "" || mk.Key != "" || mk.LogWords != ""):
		p = append(p, fmt.Sprintf("%s: guest.macos.signed_in mixes process with a file", slug))
	case !mk.Process && mk.File == "":
		p = append(p, fmt.Sprintf("%s: guest.macos.signed_in names no marker", slug))
	case mk.Key != "" && mk.LogWords != "":
		p = append(p, fmt.Sprintf("%s: guest.macos.signed_in has both a key and log words", slug))
	case strings.Contains(mk.File, "..") || strings.HasPrefix(mk.File, "/"):
		p = append(p, fmt.Sprintf("%s: guest.macos.signed_in.file must stay under Application Support", slug))
	}
	for _, v := range []string{m.App, m.Process, mk.File, mk.Key, mk.LogWords} {
		if strings.ContainsAny(v, "'\"`$\n") {
			p = append(p, fmt.Sprintf("%s: guest.macos value %q holds a quote, backslash or $", slug, v))
		}
	}
	return p
}

func TestGuestBlocksFollowTheSecureVMRules(t *testing.T) {
	services := realCatalog(t).List()
	var withGuest int
	for _, svc := range services {
		if svc.Guest != nil {
			withGuest++
		}
	}
	// Zero guest blocks would pass every rule below while checking nothing.
	if withGuest < 2 {
		t.Fatalf("only %d services carry a guest block; wipter and passiveapp should, so the overlay did not load", withGuest)
	}
	for _, p := range guestProblems(services) {
		t.Error(p)
	}
}

// The control for the test above: a guest block on EarnApp, the one service whose
// provider forbids VMs, must be reported. If this passes with no report, the
// eligibility rule can never fail.
func TestGuestRulesCatchAGuestBlockOnAProhibitedService(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "services", "bandwidth", "earnapp.yml"))
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte(`
guest:
  windows:
    process: "earnapp.exe"
    publisher: "CN=Example"
    download_page: "https://earnapp.com"
    signin: console
`)...)
	var svc Service
	if err := yaml.Unmarshal(raw, &svc); err != nil {
		t.Fatal(err)
	}
	if !svc.Requirements.ContainerProhibited {
		t.Fatal("earnapp no longer sets container_prohibited; pick another prohibited service for this control")
	}
	problems := guestProblems([]Service{svc})
	if len(problems) != 1 || !strings.Contains(problems[0], "container_prohibited") {
		t.Fatalf("a guest block on a prohibited service must be reported, got %v", problems)
	}
}

func TestGuestRulesCatchBadFields(t *testing.T) {
	svc := Service{Slug: "x", Guest: &GuestConfig{Windows: &GuestWindows{
		Process: "x.exe", Publisher: " ", DownloadPage: "http://example.com", Signin: "auto",
	}}}
	problems := strings.Join(guestProblems([]Service{svc}), "\n")
	for _, want := range []string{"publisher is empty", "not an HTTPS URL", "not console or none"} {
		if !strings.Contains(problems, want) {
			t.Errorf("expected a report containing %q, got:\n%s", want, problems)
		}
	}
}

// A misspelled key inside guest.windows must fail the strict decode that
// TestEveryVendoredKeyIsReadByTheLoader runs, or a typo would drop the value at load.
func TestStrictDecodeRefusesAMisspelledGuestKey(t *testing.T) {
	decode := func(doc string) (Service, error) {
		dec := yaml.NewDecoder(bytes.NewReader([]byte(doc)))
		dec.KnownFields(true)
		var svc Service
		err := dec.Decode(&svc)
		return svc, err
	}
	good := "slug: x\nguest:\n  windows:\n    process: x.exe\n    publisher: CN=X\n    download_page: https://x.example\n    signin: console\n"
	svc, err := decode(good)
	if err != nil {
		t.Fatalf("the correct keys must decode: %v", err)
	}
	if svc.Guest == nil || svc.Guest.Windows == nil || svc.Guest.Windows.DownloadPage != "https://x.example" ||
		svc.Guest.Windows.Process != "x.exe" || svc.Guest.Windows.Publisher != "CN=X" || svc.Guest.Windows.Signin != "console" {
		t.Fatalf("guest.windows did not decode into the struct: %+v", svc.Guest)
	}
	bad := strings.Replace(good, "download_page", "downlaod_page", 1)
	if _, err := decode(bad); err == nil {
		t.Fatal("a misspelled key inside guest.windows decoded without an error")
	}
}

// The macOS guest blocks: every earner the macOS lab proved, and the refusals.
func TestMacOSGuestSetMatchesTheLab(t *testing.T) {
	want := map[string]bool{"wipter": true, "grass": true, "teneo": true, "theta-edge": true, "uprock": true}
	refused := map[string]string{"honeygain": "terms", "spide": "unsigned", "noderr": "unsigned"}
	for _, svc := range realCatalog(t).List() {
		if svc.Guest == nil {
			continue
		}
		if svc.Guest.MacOS != nil && !want[svc.Slug] {
			t.Errorf("%s has a guest.macos block the lab never proved", svc.Slug)
		}
		if svc.Guest.MacOS != nil {
			delete(want, svc.Slug)
		}
		if r, ok := refused[svc.Slug]; ok && svc.Guest.Refused == r {
			delete(refused, svc.Slug)
		}
	}
	for slug := range want {
		t.Errorf("%s has no guest.macos block", slug)
	}
	for slug, r := range refused {
		t.Errorf("%s is not refused (%s)", slug, r)
	}
}

func TestMacGuestRulesCatchBadFields(t *testing.T) {
	m := &GuestMacOS{App: "x", Process: "", TeamID: "abc", Download: "http://x", Signin: "auto", SHA256: "zz", SignedIn: GuestMarker{File: "../x"}}
	got := strings.Join(macGuestProblems("x", m), "\n")
	for _, want := range []string{"not a bundle name", "process is empty", "not a Team ID", "not an HTTPS URL", "not 64 hex", "not console or none", "under Application Support"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in:\n%s", want, got)
		}
	}
}
