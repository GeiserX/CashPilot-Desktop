package winvm

import (
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// goodPC is a Windows 11 PC with everything Docker needs, Docker Desktop installed
// and opened before, but not running.
func goodPC() HostFacts {
	f := unknownFacts("windows")
	f.Supported = true
	f.Build, f.EditionID, f.Arch = 26100, "Professional", "x64"
	f.HypervisorPresent, f.Firmware, f.SLAT = Yes, No, No
	f.VMPlatformFeature, f.WSLFeature = Yes, Yes
	f.WSLInstalled, f.WSLVersion = Yes, "2.3.26.0"
	f.DockerDesktopInstalled, f.DockerDesktopRunning, f.DockerDesktopStarted = Yes, No, Yes
	f.DockerBackend = BackendWSL
	f.LanmanServer = "auto"
	f.OtherHypervisors = No
	f.Manufacturer, f.Model = "Dell Inc.", "OptiPlex 7090"
	derive(&f)
	return f
}

// hypervisorOff is goodPC with VirtualBox installed and Windows' hypervisor not
// started at boot, though the firmware and the feature are on.
func hypervisorOff(f *HostFacts) {
	f.OtherHypervisors, f.OtherHypervisorNames = Yes, []string{"VirtualBox"}
	f.HypervisorPresent, f.Firmware, f.SLAT = No, Yes, Yes
	f.RestartPending = No
}

func stepOf(t *testing.T, steps []Step, id string) Step {
	t.Helper()
	for _, s := range steps {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("no step %s in %+v", id, steps)
	return Step{}
}

func hasStep(steps []Step, id string) bool {
	for _, s := range steps {
		if s.ID == id {
			return true
		}
	}
	return false
}

// Every step, in each of its states, from fake facts. want is the state; blockedBy
// is set for a blocked step; kind is the action's kind ("" for none).
func TestDockerStepsGiveEachStepItsState(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(*HostFacts)
		reason    string
		step      string
		want      string
		blockedBy string
		kind      string
	}{
		// A1 Windows version.
		{"a1 done on Windows 11 24H2", nil, runtime.ReasonNotRunning, "a1", StepDone, "", ""},
		{"a1 done on Windows 10 22H2", func(f *HostFacts) { f.Build = 19045 }, runtime.ReasonNotRunning, "a1", StepDone, "", ""},
		{"a1 needs an update on Windows 11 22H2", func(f *HostFacts) { f.Build = 22621 }, runtime.ReasonNotRunning, "a1", StepNeedsYou, "", ActionWindowsUpdate},
		{"a1 needs an update on Windows 10 21H2", func(f *HostFacts) { f.Build = 19044 }, runtime.ReasonNotRunning, "a1", StepNeedsYou, "", ActionWindowsUpdate},
		{"a1 unknown with no build", func(f *HostFacts) { f.Build = 0 }, runtime.ReasonNotRunning, "a1", StepUnknown, "", ""},
		// A2 virtualization.
		{"a2 done with a hypervisor running", nil, runtime.ReasonNotRunning, "a2", StepDone, "", ""},
		{"a2 needs the BIOS when off", func(f *HostFacts) { f.HypervisorPresent, f.Firmware = No, No }, runtime.ReasonVirtualizationOff, "a2", StepNeedsYou, "", ActionFirmware},
		{"a2 needs you with no SLAT", func(f *HostFacts) { f.HypervisorPresent, f.Firmware, f.SLAT = No, Yes, No }, runtime.ReasonNotRunning, "a2", StepNeedsYou, "", ""},
		{"a2 unknown when unread", func(f *HostFacts) { f.HypervisorPresent, f.Firmware = Unknown, Unknown }, runtime.ReasonNotRunning, "a2", StepUnknown, "", ""},
		{"a2 unknown inside a VM", func(f *HostFacts) { f.Manufacturer, f.Model = "Microsoft Corporation", "Virtual Machine" }, runtime.ReasonNotRunning, "a2", StepUnknown, "", ""},
		// A3 WSL features.
		{"a3 done", nil, runtime.ReasonNotRunning, "a3", StepDone, "", ""},
		{"a3 needs WSL turned on", func(f *HostFacts) { f.VMPlatformFeature = No }, runtime.ReasonWSLMissing, "a3", StepNeedsYou, "", ActionElevated},
		{"a3 unknown when the feature read failed", func(f *HostFacts) { f.VMPlatformFeature = Unknown }, runtime.ReasonNotRunning, "a3", StepUnknown, "", ""},
		// A4 WSL version.
		{"a4 done", nil, runtime.ReasonNotRunning, "a4", StepDone, "", ""},
		{"a4 needs an update", func(f *HostFacts) { f.WSLVersion = "2.0.9.0" }, runtime.ReasonWSLOutdated, "a4", StepNeedsYou, "", ActionElevated},
		{"a4 unknown with no version", func(f *HostFacts) { f.WSLVersion = "" }, runtime.ReasonNotRunning, "a4", StepUnknown, "", ""},
		{"a4 waits for a3", func(f *HostFacts) { f.VMPlatformFeature = No }, runtime.ReasonWSLMissing, "a4", StepBlocked, "a3", ""},
		// A5 Docker installed.
		{"a5 done", nil, runtime.ReasonNotRunning, "a5", StepDone, "", ""},
		{"a5 offers the install", func(f *HostFacts) { f.DockerDesktopInstalled = No }, runtime.ReasonNotInstalled, "a5", StepNeedsYou, "", ActionRun},
		{"a5 unknown", func(f *HostFacts) { f.DockerDesktopInstalled = Unknown }, runtime.ReasonNotRunning, "a5", StepUnknown, "", ""},
		{"a5 waits for a1", func(f *HostFacts) { f.Build, f.DockerDesktopInstalled = 22000, No }, runtime.ReasonNotInstalled, "a5", StepBlocked, "a1", ""},
		// A6 Docker running.
		{"a6 done when the engine answers", func(f *HostFacts) { f.DockerDesktopRunning = Yes }, runtime.ReasonRunning, "a6", StepDone, "", ""},
		{"a6 needs a start", nil, runtime.ReasonNotRunning, "a6", StepNeedsYou, "", ActionStartDocker},
		{"a6 shows the window while the engine starts", func(f *HostFacts) { f.DockerDesktopRunning = Yes }, runtime.ReasonNotRunning, "a6", StepNeedsYou, "", ActionShowDocker},
		{"a6 unknown", func(f *HostFacts) { f.DockerDesktopRunning = Unknown }, runtime.ReasonNotRunning, "a6", StepUnknown, "", ""},
		{"a6 waits for the BIOS", func(f *HostFacts) { f.HypervisorPresent, f.Firmware = No, No }, runtime.ReasonVirtualizationOff, "a6", StepBlocked, "a2", ""},
		{"a6 waits for the install", func(f *HostFacts) { f.DockerDesktopInstalled = No }, runtime.ReasonNotInstalled, "a6", StepBlocked, "a5", ""},
		// A7 Docker's terms.
		{"a7 done when the engine answers", func(f *HostFacts) { f.DockerDesktopRunning = Yes }, runtime.ReasonRunning, "a7", StepDone, "", ""},
		{"a7 needs the terms on a first start", func(f *HostFacts) { f.DockerDesktopStarted = No }, runtime.ReasonFirstStart, "a7", StepNeedsYou, "", ActionStartDocker},
		{"a7 shows the window when Docker runs", func(f *HostFacts) { f.DockerDesktopStarted, f.DockerDesktopRunning = No, Yes }, runtime.ReasonNotRunning, "a7", StepNeedsYou, "", ActionShowDocker},
		{"a7 unknown once opened before", nil, runtime.ReasonNotRunning, "a7", StepUnknown, "", ""},
		{"a7 waits for the install", func(f *HostFacts) { f.DockerDesktopInstalled = No }, runtime.ReasonNotInstalled, "a7", StepBlocked, "a5", ""},
		// A8 docker-users, only on access denied.
		{"a8 needs the group", func(f *HostFacts) { f.DockerUsersListed, f.DockerUsersToken = No, No }, runtime.ReasonAccessDenied, "a8", StepNeedsYou, "", ActionElevated},
		{"a8 needs a sign-out when listed", func(f *HostFacts) { f.DockerUsersListed, f.DockerUsersToken = Yes, No }, runtime.ReasonAccessDenied, "a8", StepNeedsYou, "", ""},
		{"a8 offers no second add when the token carries the group", func(f *HostFacts) { f.DockerUsersListed, f.DockerUsersToken = Yes, Yes }, runtime.ReasonAccessDenied, "a8", StepNeedsYou, "", ""},
		// A9 Linux containers.
		{"a9 done", func(f *HostFacts) { f.DockerDesktopRunning = Yes }, runtime.ReasonRunning, "a9", StepDone, "", ""},
		{"a9 offers the switch", func(f *HostFacts) { f.DockerDesktopRunning = Yes }, runtime.ReasonWindowsContainers, "a9", StepNeedsYou, "", ActionRun},
		{"a9 unknown until the engine runs", func(f *HostFacts) { f.DockerDesktopRunning = Unknown }, runtime.ReasonNotRunning, "a9", StepUnknown, "", ""},
		{"a9 waits for a6", nil, runtime.ReasonNotRunning, "a9", StepBlocked, "a6", ""},
		// A10 other hypervisors.
		{"a10 done with none", nil, runtime.ReasonNotRunning, "a10", StepDone, "", ""},
		{"a10 notes VirtualBox, which nothing proves is the blocker", func(f *HostFacts) { f.OtherHypervisors, f.OtherHypervisorNames = Yes, []string{"VirtualBox"} }, runtime.ReasonNotRunning, "a10", StepNote, "", ""},
		{"a10 done when Docker runs beside it", func(f *HostFacts) {
			f.OtherHypervisors, f.OtherHypervisorNames, f.DockerDesktopRunning = Yes, []string{"VMware"}, Yes
		}, runtime.ReasonRunning, "a10", StepDone, "", ""},
		{"a10 unknown", func(f *HostFacts) { f.OtherHypervisors = Unknown }, runtime.ReasonNotRunning, "a10", StepUnknown, "", ""},
		{"a10 offers the hypervisor fix when it was turned off at boot", hypervisorOff, runtime.ReasonNotRunning, "a10", StepNeedsYou, "", ActionElevated},
		{"a10 is only a note when a restart is pending", func(f *HostFacts) { hypervisorOff(f); f.RestartPending = Yes }, runtime.ReasonNotRunning, "a10", StepNote, "", ""},
		{"a10 is only a note when the feature is off", func(f *HostFacts) { hypervisorOff(f); f.VMPlatformFeature = No }, runtime.ReasonWSLMissing, "a10", StepNote, "", ""},
		{"a10 is only a note when the BIOS is the cause", func(f *HostFacts) { hypervisorOff(f); f.Firmware = No }, runtime.ReasonVirtualizationOff, "a10", StepNote, "", ""},
		{"a10 is only a note when no other hypervisor is there", func(f *HostFacts) { hypervisorOff(f); f.OtherHypervisors, f.OtherHypervisorNames = No, nil }, runtime.ReasonNotRunning, "a10", StepDone, "", ""},
		// A11 this PC is a VM.
		{"a11 done on a physical PC", nil, runtime.ReasonNotRunning, "a11", StepDone, "", ""},
		{"a11 needs nesting in a VM", func(f *HostFacts) { f.Manufacturer, f.Model = "QEMU", "Standard PC (Q35 + ICH9, 2009)" }, runtime.ReasonNotRunning, "a11", StepNeedsYou, "", ""},
		{"a11 unknown when unread", func(f *HostFacts) { f.Manufacturer, f.Model = "", "" }, runtime.ReasonNotRunning, "a11", StepUnknown, "", ""},
		// A12 LanmanServer.
		{"a12 done", nil, runtime.ReasonNotRunning, "a12", StepDone, "", ""},
		{"a12 needs Automatic", func(f *HostFacts) { f.LanmanServer = "disabled" }, runtime.ReasonNotRunning, "a12", StepNeedsYou, "", ActionElevated},
		{"a12 manual needs Automatic", func(f *HostFacts) { f.LanmanServer = "manual" }, runtime.ReasonNotRunning, "a12", StepNeedsYou, "", ActionElevated},
		{"a12 unknown", func(f *HostFacts) { f.LanmanServer = "unknown" }, runtime.ReasonNotRunning, "a12", StepUnknown, "", ""},
		{"a12 manual is only a note while Docker runs", func(f *HostFacts) { f.LanmanServer, f.DockerDesktopRunning = "manual", Yes }, runtime.ReasonRunning, "a12", StepNote, "", ""},
		{"a12 off is only a note while Docker runs", func(f *HostFacts) { f.LanmanServer, f.DockerDesktopRunning = "disabled", Yes }, runtime.ReasonRunning, "a12", StepNote, "", ""},
		{"a12 unknown stays unknown while Docker runs", func(f *HostFacts) { f.LanmanServer, f.DockerDesktopRunning = "unknown", Yes }, runtime.ReasonRunning, "a12", StepUnknown, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := goodPC()
			if tc.mutate != nil {
				tc.mutate(&f)
				derive(&f)
			}
			s := stepOf(t, DockerSteps(f, tc.reason, true), tc.step)
			if s.State != tc.want || s.BlockedBy != tc.blockedBy || s.Action.Kind != tc.kind {
				t.Fatalf("%s: state %q blockedBy %q action %q, want %q %q %q\n%s", tc.step, s.State, s.BlockedBy, s.Action.Kind, tc.want, tc.blockedBy, tc.kind, s.Text)
			}
			if strings.TrimSpace(s.Text) == "" || s.Title == "" {
				t.Fatalf("%s has no title or text: %+v", tc.step, s)
			}
		})
	}
}

// THE RULE: a fact CashPilot could not read never makes a step done. With every
// fact unknown, no step claims done, whatever the engine's reason short of running.
func TestDockerStepsNeverClaimDoneOnUnknown(t *testing.T) {
	f := unknownFacts("windows")
	f.Supported = true
	derive(&f)
	for _, reason := range []string{runtime.ReasonNotRunning, runtime.ReasonNotInstalled, runtime.ReasonFirstStart, runtime.ReasonAccessDenied, ""} {
		for _, s := range DockerSteps(f, reason, true) {
			if s.State == StepDone {
				t.Errorf("reason %q: %s claims done with nothing known: %s", reason, s.ID, s.Text)
			}
		}
	}
}

// The engine answering proves the steps before it only when this PC's Docker
// Desktop is what answers; an engine DOCKER_HOST names elsewhere proves nothing
// about this PC.
func TestDockerStepsTrustTheEngineOnlyWhenItIsThisPCs(t *testing.T) {
	f := goodPC()
	f.HypervisorPresent, f.Firmware = Unknown, Unknown
	f.DockerDesktopRunning = Yes
	derive(&f)
	if s := stepOf(t, DockerSteps(f, runtime.ReasonRunning, true), "a2"); s.State != StepDone {
		t.Fatalf("Docker Desktop's engine runs here, but a2 is %s", s.State)
	}
	f.DockerDesktopRunning = No
	if s := stepOf(t, DockerSteps(f, runtime.ReasonRunning, true), "a2"); s.State != StepUnknown {
		t.Fatalf("a remote engine answered, but a2 is %s", s.State)
	}
}

// Docker Desktop runs here, but the engine that answered is one DOCKER_HOST or
// a Docker context names elsewhere: it proves nothing about this PC.
func TestDockerStepsTrustNoRemoteEngine(t *testing.T) {
	f := goodPC()
	f.HypervisorPresent, f.Firmware = Unknown, Unknown
	f.DockerDesktopRunning = Yes
	f.WSLInstalled, f.WSLVersion = No, ""
	derive(&f)
	steps := DockerSteps(f, runtime.ReasonRunning, false)
	if s := stepOf(t, steps, "a2"); s.State != StepUnknown {
		t.Errorf("a remote engine answered, but a2 is %s", s.State)
	}
	if s := stepOf(t, steps, "a4"); s.State == StepDone {
		t.Errorf("a remote engine answered, and a4 claims done with WSL missing: %s", s.Text)
	}
}

// Windows containers run without WSL, so an engine in that mode does not prove
// the WSL rows. It still proves the Windows version row.
func TestWindowsContainersDoNotProveWSL(t *testing.T) {
	f := goodPC()
	f.DockerDesktopRunning = Yes
	f.VMPlatformFeature, f.WSLFeature, f.WSLInstalled, f.WSLVersion = No, No, No, ""
	derive(&f)
	steps := DockerSteps(f, runtime.ReasonWindowsContainers, true)
	if s := stepOf(t, steps, "a3"); s.State != StepNeedsYou {
		t.Errorf("Windows containers with WSL missing: a3 is %s (%s)", s.State, s.Text)
	}
	if s := stepOf(t, steps, "a4"); s.State == StepDone {
		t.Errorf("Windows containers with WSL missing: a4 claims done (%s)", s.Text)
	}
	if s := stepOf(t, steps, "a5"); s.State != StepDone {
		t.Errorf("Windows containers run, but a5 is %s", s.State)
	}
	if s := stepOf(t, DockerSteps(f, runtime.ReasonRunning, true), "a3"); s.State != StepDone {
		t.Errorf("Linux containers run here, but a3 is %s", s.State)
	}
}

// Docker's Windows engine answers without virtualization, so an engine in
// Windows containers mode does not prove the virtualization row: a reading of
// No still asks for the BIOS change.
func TestWindowsContainersDoNotProveVirtualization(t *testing.T) {
	f := goodPC()
	f.DockerDesktopRunning = Yes
	f.HypervisorPresent, f.Firmware = No, No
	derive(&f)
	if f.Virtualization != No {
		t.Fatalf("the fixture reads virtualization as %v", f.Virtualization)
	}
	s := stepOf(t, DockerSteps(f, runtime.ReasonWindowsContainers, true), "a2")
	if s.State != StepNeedsYou || s.Action.Kind != ActionFirmware {
		t.Errorf("Windows containers answer with virtualization off, but a2 is %s (%s)", s.State, s.Text)
	}
	if s := stepOf(t, DockerSteps(f, runtime.ReasonRunning, true), "a2"); s.State != StepDone {
		t.Errorf("Linux containers run here, but a2 is %s", s.State)
	}
}

// The boot option needs admin rights to read, so the row never says the
// hypervisor was turned off at boot: it says it may have been, and that the
// change reads the option first.
func TestHypervisorRowNeverClaimsWhatItDidNotRead(t *testing.T) {
	f := goodPC()
	hypervisorOff(&f)
	derive(&f)
	s := stepOf(t, DockerSteps(f, runtime.ReasonNotRunning, true), "a10")
	if s.State != StepNeedsYou || s.Action.Op != OpHypervisorLaunch {
		t.Fatalf("a10 is %s with %+v", s.State, s.Action)
	}
	low := strings.ToLower(s.Text)
	for _, claim := range []string{"did not start at boot", "versions of virtualbox turn it off"} {
		if strings.Contains(low, claim) {
			t.Errorf("a10 claims %q without reading the boot option: %s", claim, s.Text)
		}
	}
	if !strings.Contains(low, "may have turned it off at boot") || !strings.Contains(low, "reads that boot option first") {
		t.Errorf("a10 does not say it may be off and is read first: %s", s.Text)
	}
	if !strings.Contains(s.Action.Manual, `bcdedit /enum "{current}"`) {
		t.Errorf("the manual way does not check first: %s", s.Action.Manual)
	}
}

// Docker on its Hyper-V backend never hears that it needs WSL.
func TestDockerStepsSkipWSLOnTheHyperVBackend(t *testing.T) {
	f := goodPC()
	f.DockerBackend = BackendHyperV
	f.VMPlatformFeature, f.WSLInstalled = No, No
	derive(&f)
	steps := DockerSteps(f, runtime.ReasonNotRunning, true)
	if hasStep(steps, "a3") || hasStep(steps, "a4") {
		t.Fatalf("the Hyper-V backend got WSL steps: %+v", steps)
	}
	if s := stepOf(t, steps, "a6"); s.State != StepNeedsYou {
		t.Fatalf("with WSL steps gone, a6 should not be blocked: %+v", s)
	}
	// An unread backend keeps the WSL steps, named for the WSL 2 backend.
	f.DockerBackend = BackendUnknown
	steps = DockerSteps(f, runtime.ReasonNotRunning, true)
	if s := stepOf(t, steps, "a3"); !strings.HasPrefix(s.Title, "WSL 2 backend") {
		t.Fatalf("a3 title %q does not name the WSL 2 backend", s.Title)
	}
}

// The steps come in A1 to A12 order, docker-users only when the engine refused
// this account, and nothing outside Windows.
func TestDockerStepsOrderAndScope(t *testing.T) {
	ids := func(steps []Step) string {
		var out []string
		for _, s := range steps {
			out = append(out, s.ID)
		}
		return strings.Join(out, " ")
	}
	if got := ids(DockerSteps(goodPC(), runtime.ReasonNotRunning, true)); got != "a1 a2 a3 a4 a5 a6 a7 a9 a10 a11 a12" {
		t.Fatalf("steps %s", got)
	}
	if got := ids(DockerSteps(goodPC(), runtime.ReasonAccessDenied, true)); got != "a1 a2 a3 a4 a5 a6 a7 a8 a9 a10 a11 a12" {
		t.Fatalf("steps on access denied %s", got)
	}
	mac := unknownFacts("darwin")
	derive(&mac)
	if steps := DockerSteps(mac, runtime.ReasonNotRunning, true); steps != nil {
		t.Fatalf("macOS got a Windows checklist: %+v", steps)
	}
}

// A blocked step offers nothing to click: its next action is the earlier step's.
func TestBlockedStepsOfferNoAction(t *testing.T) {
	f := goodPC()
	f.DockerDesktopInstalled = No
	derive(&f)
	for _, s := range DockerSteps(f, runtime.ReasonNotInstalled, true) {
		if s.State == StepBlocked && s.Action.Kind != ActionNone {
			t.Errorf("blocked %s offers %q", s.ID, s.Action.Kind)
		}
	}
}

// The terms step says what the user accepts and that CashPilot never accepts it.
func TestTermsStepCarriesTheLicenceSentence(t *testing.T) {
	f := goodPC()
	f.DockerDesktopStarted = No
	derive(&f)
	s := stepOf(t, DockerSteps(f, runtime.ReasonFirstStart, true), "a7")
	if !strings.Contains(s.Text, "never accepts them for you") || !strings.Contains(s.Text, "250 staff") {
		t.Fatalf("a7 text: %s", s.Text)
	}
}

// Only the Windows step speaks of Windows 10's end of support.
func TestWindows10NoteShowsOnce(t *testing.T) {
	f := goodPC()
	f.Build = 19045
	derive(&f)
	n := 0
	for _, s := range DockerSteps(f, runtime.ReasonNotRunning, true) {
		if strings.Contains(s.Text, "security updates") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the Windows 10 note shows %d times", n)
	}
}

// A PC where Docker runs has no step that needs the user, whatever the advice
// rows say: VirtualBox installed and the Server service on Manual are notes.
func TestDockerRunningLeavesNothingThatNeedsYou(t *testing.T) {
	f := goodPC()
	f.DockerDesktopRunning, f.LanmanServer = Yes, "manual"
	f.OtherHypervisors, f.OtherHypervisorNames = Yes, []string{"VirtualBox"}
	derive(&f)
	for _, s := range DockerSteps(f, runtime.ReasonRunning, true) {
		if s.State == StepNeedsYou || s.State == StepBlocked {
			t.Errorf("Docker runs, but %s is %s: %s", s.ID, s.State, s.Text)
		}
	}
}

// Only this PC's own Docker Desktop running Linux containers proves the advice
// rows: an engine DOCKER_HOST or a context names elsewhere says nothing about
// this PC, and Windows containers run without the hypervisor, nested
// virtualization or the Server service those rows are about.
func TestOnlyALocalLinuxEngineProvesTheAdviceRows(t *testing.T) {
	f := goodPC()
	f.DockerDesktopRunning = Yes
	f.OtherHypervisors, f.OtherHypervisorNames = Yes, []string{"VirtualBox"}
	f.Manufacturer, f.Model = "Microsoft Corporation", "Virtual Machine"
	f.LanmanServer = "manual"
	derive(&f)
	if f.IsVM != Yes {
		t.Fatalf("the fixture reads IsVM as %v", f.IsVM)
	}
	for _, c := range []struct {
		name   string
		reason string
		local  bool
	}{
		{"a remote engine", runtime.ReasonRunning, false},
		{"Windows containers", runtime.ReasonWindowsContainers, true},
	} {
		steps := DockerSteps(f, c.reason, c.local)
		if s := stepOf(t, steps, "a10"); s.State == StepDone {
			t.Errorf("%s: a10 claims Docker runs next to VirtualBox: %s", c.name, s.Text)
		}
		if s := stepOf(t, steps, "a11"); s.State != StepNeedsYou {
			t.Errorf("%s: a11 is %s, want needs-you: %s", c.name, s.State, s.Text)
		}
		if s := stepOf(t, steps, "a12"); s.State != StepNeedsYou || s.Action.Op != OpLanmanServer {
			t.Errorf("%s: a12 is %s with %+v, want needs-you with the fix: %s", c.name, s.State, s.Action, s.Text)
		}
		if LocalLinuxEngine(f, c.reason, c.local) {
			t.Errorf("%s counts as this PC's Docker Desktop running Linux containers", c.name)
		}
	}
	steps := DockerSteps(f, runtime.ReasonRunning, true)
	for id, want := range map[string]string{"a10": StepDone, "a11": StepDone, "a12": StepNote} {
		if s := stepOf(t, steps, id); s.State != want {
			t.Errorf("this PC's Docker Desktop runs Linux containers, but %s is %s", id, s.State)
		}
	}
	if !LocalLinuxEngine(f, runtime.ReasonRunning, true) {
		t.Error("this PC's Docker Desktop runs Linux containers, but it does not count")
	}
	f.DockerDesktopRunning = Unknown
	if LocalLinuxEngine(f, runtime.ReasonRunning, true) {
		t.Error("an engine answers with Docker Desktop not seen running here, and it counts")
	}
}

func TestDesktopPipeProvesLocalSetupWithAConfiguredEngineElsewhere(t *testing.T) {
	f := goodPC()
	f.DockerDesktopRunning = Yes
	f.desktopEngineReason = runtime.ReasonRunning
	steps := DockerSteps(f, runtime.ReasonUnreachable, false)
	for _, id := range []string{"a6", "a7", "a9"} {
		if step := stepOf(t, steps, id); step.State != StepDone {
			t.Errorf("%s is %s when the local Docker Desktop engine answers", id, step.State)
		}
	}
	if !LocalLinuxEngine(f, runtime.ReasonUnreachable, false) {
		t.Fatal("the configured endpoint hid the local Docker Desktop engine")
	}
}
