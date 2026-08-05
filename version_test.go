package main

import (
	"os"
	"strings"
	"testing"
)

// The version is injected at link time, so these tests cover the two halves
// that can independently break: what the accessor does with the value, and
// whether the release build still passes one.
//
// Verified empirically that the injection itself works, because a config change
// that does nothing is the easy failure here:
//
//	go build -ldflags "-X main.appVersion=v9.9.9" -o /tmp/x .
//	strings /tmp/x | grep -c 9.9.9   -> 3
//
// Note the FORM matters and differs between a binary and a test binary: a real
// build of package main takes `-X main.appVersion`, while a `go test` binary
// needs the full import path (`-X github.com/GeiserX/CashPilot-Desktop.appVersion`).
// My first probe used the `main.` form under `go test`, got an empty string, and
// looked exactly like a broken feature.

func TestAppVersionStripsTheTagPrefix(t *testing.T) {
	// The tag is "v1.2.3" while wails.json productVersion and package.json both
	// store the bare number. Sending both spellings for one release would show
	// what looks like two different versions on the fleet page.
	old := appVersion
	t.Cleanup(func() { appVersion = old })

	appVersion = "v1.2.3"
	if got := AppVersion(); got != "1.2.3" {
		t.Fatalf("AppVersion() = %q, want %q", got, "1.2.3")
	}
}

func TestAppVersionAcceptsABareNumber(t *testing.T) {
	old := appVersion
	t.Cleanup(func() { appVersion = old })

	appVersion = "1.2.3"
	if got := AppVersion(); got != "1.2.3" {
		t.Fatalf("AppVersion() = %q", got)
	}
}

func TestAppVersionTrimsWhitespace(t *testing.T) {
	old := appVersion
	t.Cleanup(func() { appVersion = old })

	appVersion = "  v1.2.3\n"
	if got := AppVersion(); got != "1.2.3" {
		t.Fatalf("AppVersion() = %q", got)
	}
}

func TestAnUnstampedBuildReportsUnknownRatherThanAVersion(t *testing.T) {
	// THE RULE. Empty means this build does not know, and the server reads an
	// absent version as unknown. Substituting a placeholder here -- "dev",
	// "0.0.0", the last known release -- would read as a MATCH and hide the
	// stale install this exists to reveal.
	old := appVersion
	t.Cleanup(func() { appVersion = old })

	appVersion = ""
	if got := AppVersion(); got != "" {
		t.Fatalf("AppVersion() = %q, want empty for an unstamped build", got)
	}
}

func TestTheDefaultIsUnstamped(t *testing.T) {
	// A hardcoded default is a SECOND source of truth that goes stale silently,
	// and a stale version is worse than none: it reads as a match and hides the
	// old install this exists to reveal.
	//
	// Asserted against the SOURCE DECLARATION, not the runtime value. The first
	// version of this test only t.Logf'd, so it could never fail -- and a
	// negative control that changed the default to "0.0.0" sailed through it.
	// The runtime value is also the wrong thing to check, because a test binary
	// built with -ldflags legitimately has a non-empty one.
	raw, err := os.ReadFile("version.go")
	if err != nil {
		t.Fatalf("read version.go: %v", err)
	}
	if !strings.Contains(string(raw), `var appVersion = ""`) {
		t.Error(`version.go no longer declares: var appVersion = "" -- an unstamped build would claim a version it does not have`)
	}
}

func TestThePayloadCarriesTheInjectedVersion(t *testing.T) {
	// Closes the loop: the accessor is correct AND the heartbeat actually sends
	// it. Previously the field went out empty by design, and this test is the
	// inversion of the one that asserted that.
	old := appVersion
	t.Cleanup(func() { appVersion = old })
	appVersion = "v4.5.6"

	p := newPayloadTestApp(t, nil).upstreamPayload()
	if p.SystemInfo.Version != "4.5.6" {
		t.Fatalf("system_info.version = %q, want the injected version", p.SystemInfo.Version)
	}
}

func TestThePayloadOmitsAnUnstampedVersion(t *testing.T) {
	old := appVersion
	t.Cleanup(func() { appVersion = old })
	appVersion = ""

	p := newPayloadTestApp(t, nil).upstreamPayload()
	if p.SystemInfo.Version != "" {
		t.Fatalf("system_info.version = %q, want empty so the server reads unknown", p.SystemInfo.Version)
	}
}

func TestTheReleaseBuildStillInjectsAVersion(t *testing.T) {
	// The accessor is worthless if the build stops passing a value, and that
	// regression is invisible: the app keeps working and merely reports nothing.
	//
	// Checked as text rather than parsed YAML because the value lives inside a
	// shell `run:` block, which YAML gives back as one opaque string anyway.
	raw, err := os.ReadFile(".github/workflows/desktop-release.yml")
	if err != nil {
		t.Fatalf("read workflow: %v", err)
	}
	wf := string(raw)

	if !strings.Contains(wf, "-X main.appVersion=$VERSION") {
		t.Error("the release workflow no longer injects main.appVersion")
	}
	// EVERY platform build must carry it. Injecting on one OS and not the others
	// is the likely half-fix, and it would leave Windows or macOS reporting
	// unknown while Linux looked correct.
	builds := strings.Count(wf, "cmd/wails@v2.12.0 build")
	stamped := strings.Count(wf, `-ldflags "$CASHPILOT_LDFLAGS"`)
	if builds == 0 {
		t.Fatal("no wails build step found; this test is checking nothing")
	}
	if stamped != builds {
		t.Errorf("%d wails build steps but only %d carry -ldflags: a platform would report no version", builds, stamped)
	}
	// The version must come from the same place wails.json gets it -- the tag --
	// or the two can disagree about what a release is.
	if !strings.Contains(wf, `VERSION="${VERSION#v}"`) {
		t.Error("the tag is no longer the source of the version")
	}
}
