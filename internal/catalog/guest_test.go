package catalog

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
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
		w := svc.Guest.Windows
		if w == nil {
			problems = append(problems, fmt.Sprintf("%s: guest block has no windows entry", svc.Slug))
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
