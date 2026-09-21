package preflight

import (
	"os"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// The assessment is checked against the REAL vendored catalog rather than
// hand-written fixtures. Every finding it makes is a claim about a specific
// provider, so a fixture that drifts from services/*.yml would prove nothing
// about what a user is actually told before deploying.
func realCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	cat, err := catalog.LoadEmbedded(os.DirFS("../.."))
	if err != nil {
		t.Fatalf("loading the vendored catalog: %v", err)
	}
	return cat
}

func service(t *testing.T, slug string) catalog.Service {
	t.Helper()
	svc, ok := realCatalog(t).Get(slug)
	if !ok {
		t.Fatalf("%s is not in the vendored catalog", slug)
	}
	return svc
}

func findingWith(report Report, substring string) (Finding, bool) {
	for _, finding := range report.Findings {
		if strings.Contains(finding.Message, substring) {
			return finding, true
		}
	}
	return Finding{}, false
}

func TestAssessRealCatalogEntries(t *testing.T) {
	cases := []struct {
		name string
		slug string
		arch string
		// fleet is the other machines in the user's fleet.
		fleet []Device
		// verdict is the whole report's verdict.
		verdict string
		// says is a phrase that must appear in one of the findings. Empty means the
		// report must carry no findings at all.
		says string
	}{
		{
			name:    "no published build for this CPU is the strongest verdict short of a ban",
			slug:    "proxylite",
			arch:    "arm64",
			verdict: EarnsNothing,
			says:    "publishes no build for it, only x86-64",
		},
		{
			name:    "the same entry on the CPU it does publish for is clean",
			slug:    "proxylite",
			arch:    "x86_64",
			verdict: LooksFine,
		},
		{
			name:    "a service published for both CPUs, needing nothing, says so",
			slug:    "bitping",
			arch:    "arm64",
			verdict: LooksFine,
		},
		{
			name:    "a provider that forbids containers is disclosed before the deploy",
			slug:    "earnapp",
			arch:    "x86_64",
			verdict: EarnsNothing,
			says:    "forbids running its software in containers",
		},
		{
			name:    "a service needing a home connection says CashPilot cannot check it",
			slug:    "earnapp",
			arch:    "x86_64",
			verdict: EarnsNothing,
			says:    "CashPilot cannot check what kind of connection you have",
		},
		{
			name:    "a service needing a graphics card warns it will sit idle without one",
			slug:    "salad",
			arch:    "x86_64",
			verdict: CheckYourself,
			says:    "graphics card passed through to the container",
		},
		{
			name:    "a storage node states the disk it needs for months",
			slug:    "storj",
			arch:    "x86_64",
			verdict: CheckYourself,
			says:    "Needs at least 550GB of disk",
		},
		{
			name:    "and the catalog author's own note is passed through verbatim",
			slug:    "storj",
			arch:    "x86_64",
			verdict: CheckYourself,
			says:    "same /24 subnet share data allocation",
		},
		{
			name:    "a bandwidth floor is stated as earning less, not as a failure",
			slug:    "storj",
			arch:    "x86_64",
			verdict: CheckYourself,
			says:    "Wants at least 5 Mbps upload",
		},
		{
			name:    "a one-device-per-connection service already on another machine is flagged",
			slug:    "honeygain",
			arch:    "x86_64",
			fleet:   []Device{{Name: "attic-pi", Services: []string{"honeygain"}}},
			verdict: CheckYourself,
			says:    "already runs on attic-pi, and it allows only one device",
		},
		{
			name:    "a build that exists only under another tag is not a pass",
			slug:    "traffmonetizer",
			arch:    "arm64",
			verdict: EarnsNothing,
			says:    "separate image, and CashPilot deploys the x86-64 one",
		},
		{
			name:    "the same entry on x86-64 gets the image it was pinned to",
			slug:    "traffmonetizer",
			arch:    "x86_64",
			verdict: CheckYourself,
			says:    "VPS nodes are accepted in practice",
		},
		{
			name:    "a service that accepts a rented server is not told it needs a home line",
			slug:    "earnfm",
			arch:    "x86_64",
			verdict: LooksFine,
		},
		{
			name:    "a fleet machine running something else changes nothing",
			slug:    "bitping",
			arch:    "x86_64",
			fleet:   []Device{{Name: "attic-pi", Services: []string{"honeygain"}}},
			verdict: LooksFine,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := Assess(Input{Service: service(t, tc.slug), Fleet: tc.fleet, DaemonArch: tc.arch})
			if report.Verdict != tc.verdict {
				t.Fatalf("verdict for %s on %s = %q, want %q (findings: %+v)",
					tc.slug, tc.arch, report.Verdict, tc.verdict, report.Findings)
			}
			if tc.says == "" {
				if len(report.Findings) != 0 {
					t.Fatalf("expected no findings for %s, got %+v", tc.slug, report.Findings)
				}
				return
			}
			if _, ok := findingWith(report, tc.says); !ok {
				t.Fatalf("no finding for %s mentions %q; got %+v", tc.slug, tc.says, report.Findings)
			}
		})
	}
}

// The worst verdict has to be the first thing read. A user who stops after one
// sentence must have read the decisive one.
func TestWorstFindingIsShownFirst(t *testing.T) {
	// PacketShare on 64-bit ARM: it needs a home connection (check this) and it
	// publishes no build for this CPU (earns nothing). The second is found later than
	// the first, so an unsorted list would open with the milder one.
	report := Assess(Input{Service: service(t, "packetshare"), DaemonArch: "arm64"})
	if len(report.Findings) < 2 {
		t.Fatalf("expected at least two findings, got %+v", report.Findings)
	}
	if report.Findings[0].Verdict != EarnsNothing {
		t.Fatalf("first finding is %q, want %q: %+v",
			report.Findings[0].Verdict, EarnsNothing, report.Findings)
	}
}

// The summary for a provider that bans containers must not read like a bad month.
// The outcome is a closed account with the pending balance cancelled.
func TestProhibitedContainerSummarySaysTheAccountIsAtRisk(t *testing.T) {
	report := Assess(Input{Service: service(t, "earnapp"), DaemonArch: "x86_64"})
	if !strings.Contains(report.Summary, "closed account") {
		t.Fatalf("summary does not name the real outcome: %q", report.Summary)
	}
	if strings.Contains(report.Summary, "earn nothing here") {
		t.Fatalf("summary reads like low earnings rather than a ban: %q", report.Summary)
	}
}

// Nothing here may ever stop a deploy, whatever the verdict.
func TestNothingBlocksTheDeploy(t *testing.T) {
	for _, svc := range realCatalog(t).List() {
		for _, arch := range []string{"", "x86_64", "arm64", "armv6l"} {
			report := Assess(Input{Service: svc, DaemonArch: arch})
			if report.Blocking {
				t.Fatalf("%s on %q reports blocking", svc.Slug, arch)
			}
		}
	}
}

func TestNotChecked(t *testing.T) {
	proxylite := service(t, "proxylite")

	// What Desktop genuinely cannot see is listed every time, on every service.
	always := Assess(Input{Service: proxylite, DaemonArch: "x86_64"}).NotChecked
	for _, want := range []string{"internet connection", "connection speed", "disk"} {
		if !containsSubstring(always, want) {
			t.Fatalf("not-checked list %q omits %q", always, want)
		}
	}

	// An unreadable runtime architecture means the CPU question was not answered.
	// Saying nothing would let a clean report imply a check nobody ran.
	unknown := Assess(Input{Service: proxylite, DaemonArch: ""})
	if !containsSubstring(unknown.NotChecked, "build for your CPU") {
		t.Fatalf("unknown architecture is not disclosed: %q", unknown.NotChecked)
	}
	if unknown.Verdict != LooksFine {
		t.Fatalf("an unknown CPU must not become a verdict: %q", unknown.Verdict)
	}
	if len(unknown.Findings) != 0 {
		t.Fatalf("an unknown CPU must not produce findings: %+v", unknown.Findings)
	}

	// A CPU that WAS checked must not appear in the list: that would teach the user
	// to ignore it.
	checked := Assess(Input{Service: proxylite, DaemonArch: "x86_64"})
	if containsSubstring(checked.NotChecked, "build for your CPU") {
		t.Fatalf("a checked CPU is listed as unchecked: %q", checked.NotChecked)
	}

	// Whether the other machines share this connection is the one fact the per-IP
	// finding rests on, and Desktop cannot see it.
	withPeer := Assess(Input{
		Service:    service(t, "honeygain"),
		Fleet:      []Device{{Name: "attic-pi", Services: []string{"honeygain"}}},
		DaemonArch: "x86_64",
	})
	if !containsSubstring(withPeer.NotChecked, "same internet connection as this one") {
		t.Fatalf("the shared-connection assumption is not disclosed: %q", withPeer.NotChecked)
	}
}

// devices_per_ip is a pointer because an omitted value and 0 are different answers,
// and the user is told different things. Omitted means nobody documented a limit;
// 0 is a checked statement that there is none.
func TestDevicesPerIPUndocumentedIsNotUnlimited(t *testing.T) {
	limit := func(n int) *int { return &n }
	peer := []Device{{Name: "attic-pi", Services: []string{"honeygain"}}}

	cases := []struct {
		name  string
		limit *int
		fleet []Device
		says  string
	}{
		{
			name:  "undocumented sends the user to the provider's terms",
			limit: nil,
			fleet: peer,
			says:  "Nobody has documented how many devices",
		},
		{
			name:  "one device per connection is the documented conflict",
			limit: limit(1),
			fleet: peer,
			says:  "allows only one device per internet connection",
		},
		{
			name:  "a documented no-limit is a shared line, not a lost device",
			limit: limit(0),
			fleet: peer,
			says:  "sets no limit per internet connection",
		},
		{
			name:  "a limit this deploy would exceed names the number",
			limit: limit(1),
			fleet: []Device{{Name: "attic-pi", Services: []string{"honeygain"}}, {Name: "shed-nuc", Services: []string{"honeygain"}}},
			says:  "allows only one device per internet connection",
		},
		{
			name:  "a limit with room left says nothing at all",
			limit: limit(4),
			fleet: peer,
			says:  "",
		},
		{
			name:  "and no other machine runs it, so there is nothing to say",
			limit: limit(1),
			fleet: []Device{{Name: "attic-pi", Services: []string{"bitping"}}},
			says:  "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := service(t, "honeygain")
			svc.Requirements.DevicesPerIP = tc.limit
			// Isolate the fleet half from honeygain's own home-connection requirement.
			svc.Requirements.ResidentialIP = false
			report := Assess(Input{Service: svc, Fleet: tc.fleet, DaemonArch: "x86_64"})
			if tc.says == "" {
				if len(report.Findings) != 0 {
					t.Fatalf("expected no findings, got %+v", report.Findings)
				}
				return
			}
			finding, ok := findingWith(report, tc.says)
			if !ok {
				t.Fatalf("no finding mentions %q; got %+v", tc.says, report.Findings)
			}
			// Desktop cannot see the other machines' connection, so none of these may be
			// stated as a fact.
			if finding.Verdict != CheckYourself {
				t.Fatalf("verdict %q claims a certainty Desktop does not have", finding.Verdict)
			}
		})
	}
}

// A limit of 3 with three machines already on it is over the line, and the message
// has to say which number this would be.
func TestDevicesPerIPCountsThisDeploy(t *testing.T) {
	three := 3
	svc := service(t, "honeygain")
	svc.Requirements.DevicesPerIP = &three
	svc.Requirements.ResidentialIP = false
	report := Assess(Input{
		Service: svc,
		Fleet: []Device{
			{Name: "attic-pi", Services: []string{"honeygain"}},
			{Name: "shed-nuc", Services: []string{"honeygain"}},
			{Name: "loft-mini", Services: []string{"honeygain"}},
		},
		DaemonArch: "x86_64",
	})
	if _, ok := findingWith(report, "allows 3 devices per internet connection and this would be number 4"); !ok {
		t.Fatalf("the count does not include this deploy: %+v", report.Findings)
	}
}

// Two machines can share a display name (the default is the hostname). Naming one
// twice in the sentence would read as a bug; counting one twice would be one.
func TestPeersWithTheSameNameAreNamedOnce(t *testing.T) {
	svc := service(t, "honeygain")
	svc.Requirements.ResidentialIP = false
	report := Assess(Input{
		Service: svc,
		Fleet: []Device{
			{Name: "raspberrypi", Services: []string{"honeygain"}},
			{Name: "raspberrypi", Services: []string{"honeygain"}},
		},
		DaemonArch: "x86_64",
	})
	if len(report.Findings) != 1 {
		t.Fatalf("expected one finding, got %+v", report.Findings)
	}
	if strings.Count(report.Findings[0].Message, "raspberrypi") != 1 {
		t.Fatalf("a duplicate name is repeated: %q", report.Findings[0].Message)
	}
}

// A service with no image (tracked manually, or run as a native process) has no
// image to check, so the CPU question must not be raised at all.
func TestServiceWithoutAnImageRaisesNoCPUQuestion(t *testing.T) {
	svc := catalog.Service{Name: "Handset Only", Slug: "handset-only"}
	report := Assess(Input{Service: svc, DaemonArch: ""})
	if containsSubstring(report.NotChecked, "build for your CPU") {
		t.Fatalf("a service with no image asks about builds: %q", report.NotChecked)
	}
}

func containsSubstring(list []string, want string) bool {
	for _, item := range list {
		if strings.Contains(item, want) {
			return true
		}
	}
	return false
}

// An entry with image_by_arch names a build Desktop's deploy never reaches for: it
// runs docker.image on every CPU. Counting the override as a pass told a user on a
// Raspberry Pi that Traffmonetizer works there, while the container they got was the
// x86-64 one, which dies on the first start and earns nothing.
func TestAnImageOnlyAnotherTagProvidesIsNotAPass(t *testing.T) {
	traffmonetizer := service(t, "traffmonetizer")
	if len(traffmonetizer.Docker.ImageByArch) == 0 {
		t.Fatal("this test needs an entry with per-architecture images")
	}
	for _, arch := range []string{"arm64", "aarch64", "armv7l"} {
		report := Assess(Input{Service: traffmonetizer, DaemonArch: arch})
		if report.Verdict != EarnsNothing {
			t.Fatalf("on %s the verdict is %q, want %q: %+v",
				arch, report.Verdict, EarnsNothing, report.Findings)
		}
		if containsSubstring(report.NotChecked, "build for your CPU") {
			t.Fatalf("on %s the CPU is both answered and listed as unchecked: %q",
				arch, report.NotChecked)
		}
	}

	// The image it IS pinned to runs on x86-64, so nothing about the CPU is said
	// there. A finding fired at every user is a finding everybody learns to skip.
	onIntel := Assess(Input{Service: traffmonetizer, DaemonArch: "x86_64"})
	if _, ok := findingWith(onIntel, "exec format error"); ok {
		t.Fatalf("x86-64 is warned about the image it actually gets: %+v", onIntel.Findings)
	}
}

// The summary is the one sentence a user is guaranteed to read. When the point
// below it says the deploy may earn nothing, the summary may not say it should
// work: they cannot both be the takeaway.
func TestSummaryDoesNotPromiseWorkOverAFindingThatDescribesALoss(t *testing.T) {
	report := Assess(Input{
		Service:    service(t, "earnfm"),
		Fleet:      []Device{{Name: "attic-pi", Services: []string{"earnfm"}}},
		DaemonArch: "x86_64",
	})
	if report.Verdict != CheckYourself {
		t.Fatalf("verdict %q: Desktop cannot see the other machine's connection", report.Verdict)
	}
	if strings.Contains(report.Summary, "should work") {
		t.Fatalf("the summary contradicts its own finding: %q / %+v", report.Summary, report.Findings)
	}
	if !strings.Contains(report.Summary, "may earn nothing") {
		t.Fatalf("the summary does not carry the loss: %q", report.Summary)
	}

	// The same service with nobody else running it keeps the ordinary summary: the
	// loss wording belongs to the conflict, not to every "check this" report.
	alone := Assess(Input{Service: service(t, "honeygain"), DaemonArch: "x86_64"})
	if !strings.Contains(alone.Summary, "should work") {
		t.Fatalf("a plain check-this report lost its summary: %q", alone.Summary)
	}
}

// Two machines can answer to one name: the name defaults to the hostname, and a
// house with two Raspberry Pis has two rows reading "raspberrypi". Counting the
// names instead of the machines merges them and under-warns, which is the failure
// this whole check exists to remove.
func TestTwoMachinesSharingOneNameCountAsTwo(t *testing.T) {
	two := 2
	svc := service(t, "honeygain")
	svc.Requirements.DevicesPerIP = &two
	svc.Requirements.ResidentialIP = false
	report := Assess(Input{
		Service: svc,
		Fleet: []Device{
			{Name: "raspberrypi", Services: []string{"honeygain"}},
			{Name: "raspberrypi", Services: []string{"honeygain"}},
		},
		DaemonArch: "x86_64",
	})
	if _, ok := findingWith(report, "allows 2 devices per internet connection and this would be number 3"); !ok {
		t.Fatalf("two machines with one name counted as one: %+v", report.Findings)
	}
	// Still named once: the count is not the sentence.
	if strings.Count(report.Findings[0].Message, "raspberrypi") != 1 {
		t.Fatalf("a duplicate name is repeated: %q", report.Findings[0].Message)
	}
}

// A service that documents both residential and VPS connections as acceptable must
// not be told it needs a home line. That finding is for the services where a rented
// server gets the account banned, and firing it everywhere would make it noise.
func TestAServiceThatAcceptsARentedServerIsNotToldItNeedsAHomeLine(t *testing.T) {
	earnfm := service(t, "earnfm")
	if !earnfm.Requirements.ResidentialIP || !earnfm.Requirements.VPSIP {
		t.Fatal("this test needs an entry that declares both residential and VPS")
	}
	report := Assess(Input{Service: earnfm, DaemonArch: "x86_64"})
	if _, ok := findingWith(report, "home internet connection"); ok {
		t.Fatalf("a VPS-friendly service demands a home line: %+v", report.Findings)
	}

	// The control: the same finding on an entry that really does need one.
	honeygain := service(t, "honeygain")
	if _, ok := findingWith(Assess(Input{Service: honeygain, DaemonArch: "x86_64"}), "home internet connection"); !ok {
		t.Fatal("the home-connection finding no longer fires for a residential-only service")
	}
}
