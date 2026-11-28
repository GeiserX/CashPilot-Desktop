package winvm

import (
	"strings"
	"testing"
)

// facts is a Windows 11 Pro x64 PC with everything ready, for each test to break
// one thing at a time.
func facts(edit func(*HostFacts)) HostFacts {
	f := unknownFacts("windows")
	f.Supported = true
	f.Build = 26100
	f.EditionID = "Professional"
	f.Arch = "x64"
	f.HypervisorPresent = Yes
	f.Firmware = No
	f.WSLFeature = No
	f.VMPlatformFeature = Yes
	f.WSLInstalled = Yes
	f.WSLVersion = "2.3.26.0"
	if edit != nil {
		edit(&f)
	}
	derive(&f)
	return f
}

func TestHyperVEditionReadsEditionID(t *testing.T) {
	cases := map[string]Tri{
		// Home in every form Windows writes it.
		"Core":                No,
		"CoreN":               No,
		"CoreSingleLanguage":  No,
		"CoreCountrySpecific": No,
		"core":                No,
		// Windows 11 SE.
		"CloudEdition": No,
		// Editions with Hyper-V.
		"Professional":             Yes,
		"ProfessionalN":            Yes,
		"ProfessionalWorkstation":  Yes,
		"ProfessionalEducation":    Yes,
		"Education":                Yes,
		"Enterprise":               Yes,
		"EnterpriseS":              Yes,
		"EnterpriseSN":             Yes,
		"IoTEnterprise":            Yes,
		"ServerDatacenter":         Yes,
		"":                         Unknown,
		"SomethingWindowsAddsNext": Unknown,
	}
	for id, want := range cases {
		if got := HyperVEdition(id); got != want {
			t.Errorf("HyperVEdition(%q) = %s, want %s", id, got, want)
		}
	}
}

func TestVirtualizationReadsHypervisorFirst(t *testing.T) {
	cases := []struct {
		hypervisor, firmware, want Tri
		why                        string
	}{
		{Yes, No, Yes, "a running hypervisor makes the firmware flag read false; virtualization is on"},
		{Yes, Unknown, Yes, "a running hypervisor proves it alone"},
		{No, Yes, Yes, "no hypervisor, firmware on"},
		{No, No, No, "no hypervisor and firmware off: really off"},
		{Unknown, No, Unknown, "firmware off means nothing while a hypervisor may run"},
		{No, Unknown, Unknown, "firmware unread"},
		{Unknown, Unknown, Unknown, "nothing read"},
	}
	for _, c := range cases {
		if got := virtualizationOn(c.hypervisor, c.firmware); got != c.want {
			t.Errorf("hypervisor %s, firmware %s: got %s, want %s (%s)", c.hypervisor, c.firmware, got, c.want, c.why)
		}
	}
	if f := facts(func(f *HostFacts) { f.HypervisorPresent, f.Firmware = Yes, No }); f.Virtualization != Yes {
		t.Errorf("Snapshot facts with HypervisorPresent and firmware false derived %s, want yes", f.Virtualization)
	}
}

func TestWSLState(t *testing.T) {
	cases := []struct {
		name string
		edit func(*HostFacts)
		want string
	}{
		{"current package", nil, WSLReady},
		{"exactly the minimum", func(f *HostFacts) { f.WSLVersion = "2.1.5" }, WSLReady},
		{"too old", func(f *HostFacts) { f.WSLVersion = "2.0.14.0" }, WSLOutdated},
		{"too old in the patch number", func(f *HostFacts) { f.WSLVersion = "2.1.4.0" }, WSLOutdated},
		{"version unreadable", func(f *HostFacts) { f.WSLVersion = "" }, WSLUnknown},
		{"VirtualMachinePlatform off", func(f *HostFacts) { f.VMPlatformFeature = No }, WSLMissing},
		{"no package, no features", func(f *HostFacts) {
			f.WSLInstalled, f.WSLFeature, f.VMPlatformFeature, f.WSLVersion = No, No, Unknown, ""
		}, WSLMissing},
		{"no package, inbox WSL on", func(f *HostFacts) {
			f.WSLInstalled, f.WSLFeature, f.WSLVersion = No, Yes, ""
		}, WSLOutdated},
		{"nothing read", func(f *HostFacts) {
			f.WSLInstalled, f.WSLFeature, f.VMPlatformFeature, f.WSLVersion = Unknown, Unknown, Unknown, ""
		}, WSLUnknown},
	}
	for _, c := range cases {
		if got := facts(c.edit).WSL; got != c.want {
			t.Errorf("%s: WSL = %s, want %s", c.name, got, c.want)
		}
	}
}

func TestSecureVMHost(t *testing.T) {
	cases := []struct {
		name   string
		edit   func(*HostFacts)
		want   Tri
		reason string
	}{
		{"Windows 11 Pro x64", nil, Yes, ""},
		{"Windows 11 Home", func(f *HostFacts) { f.EditionID = "Core" }, No, HostHomeEdition},
		{"Windows 11 Home N", func(f *HostFacts) { f.EditionID = "CoreN" }, No, HostHomeEdition},
		{"Windows 11 Home single language", func(f *HostFacts) { f.EditionID = "CoreSingleLanguage" }, No, HostHomeEdition},
		{"Windows 10 Pro: same EditionID, older build", func(f *HostFacts) { f.Build = 19045 }, No, HostWindows10},
		{"build 21999", func(f *HostFacts) { f.Build = Windows11Build - 1 }, No, HostWindows10},
		{"build 22000", func(f *HostFacts) { f.Build = Windows11Build }, Yes, ""},
		{"Arm PC", func(f *HostFacts) { f.Arch = "arm64" }, No, HostArm},
		{"Arm Home PC says Arm, which Pro does not fix", func(f *HostFacts) { f.Arch, f.EditionID = "arm64", "Core" }, No, HostArm},
		{"Windows 10 Home says Windows 10", func(f *HostFacts) { f.Build, f.EditionID = 19045, "Core" }, No, HostWindows10},
		{"CPU unread", func(f *HostFacts) { f.Arch = "unknown" }, Unknown, HostUnknownArch},
		{"build unread", func(f *HostFacts) { f.Build = 0 }, Unknown, HostUnknownBuild},
		{"edition unread", func(f *HostFacts) { f.EditionID = "" }, Unknown, HostUnknownEdition},
		{"not Windows", func(f *HostFacts) { f.OS = "darwin" }, No, HostNotWindows},
	}
	for _, c := range cases {
		got := facts(c.edit).SecureVMHost
		if got.Eligible != c.want || got.Reason != c.reason {
			t.Errorf("%s: got %+v, want eligible %s reason %q", c.name, got, c.want, c.reason)
		}
	}
}

func TestVersionLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"2.0.14", "2.1.5", true},
		{"2.1.5", "2.1.5", false},
		{"2.1.5.0", "2.1.5", false},
		{"2.10.0", "2.9.9", false},
		{"1.9", "2.1.5", true},
		{"x.y.z", "2.1.5", true},
	}
	for _, c := range cases {
		if got := versionLess(c.a, c.b); got != c.want {
			t.Errorf("versionLess(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestArchNameIsNotGOARCH(t *testing.T) {
	for code, want := range map[int]string{0: "x86", 5: "arm", 9: "x64", 12: "arm64", 6: "unknown"} {
		if got := archName(code); got != want {
			t.Errorf("archName(%d) = %s, want %s", code, got, want)
		}
	}
}

func TestVirtualMachineReadsTheFirmwareStrings(t *testing.T) {
	cases := []struct {
		manufacturer, model string
		want                Tri
	}{
		{"Microsoft Corporation", "Virtual Machine", Yes},
		{"VMware, Inc.", "VMware7,1", Yes},
		{"innotek GmbH", "VirtualBox", Yes},
		{"QEMU", "Standard PC (Q35 + ICH9, 2009)", Yes},
		{"Microsoft Corporation", "Surface Pro 9", No},
		{"Intel(R) Client Systems", "NUC12DCMi9", No},
		{"", "", Unknown},
	}
	for _, tc := range cases {
		if got := virtualMachine(tc.manufacturer, tc.model); got != tc.want {
			t.Errorf("virtualMachine(%q, %q) = %s, want %s", tc.manufacturer, tc.model, got, tc.want)
		}
	}
}

// vmHostInput is a PC ready for the Secure VM, for each test to break one thing.
func vmHostInput(edit func(*VMHostInput)) VMHostInput {
	in := VMHostInput{
		Net:          testHost(),
		Network:      CurrentNetwork{ID: NetworkID{GatewayMAC: "00-11-22-33-44-55", ProfileID: "{6F1C2D3E-0000-4000-8000-000000000001}"}},
		InstalledRAM: 32 * GiB,
		FreeRAM:      8 * GiB,
		GCS:          []string{"999e53d4-3d5c-4c3e-8779-bed06ec056e1"},
	}
	if edit != nil {
		edit(&in)
	}
	return in
}

func checkOf(r VMHostReport, id string) VMHostCheck {
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	return VMHostCheck{ID: id, OK: "missing"}
}

func TestCheckVMHostReady(t *testing.T) {
	r := CheckVMHost(vmHostInput(nil))
	if !r.Ready {
		t.Fatalf("a ready PC is not ready: %+v", r.Checks)
	}
	if len(r.Checks) != 6 {
		t.Errorf("%d checks, want 6", len(r.Checks))
	}
	if len(r.GCSBaseline) != 1 || r.GCSBaseline[0] != "999E53D4-3D5C-4C3E-8779-BED06EC056E1" {
		t.Errorf("GCSBaseline = %v", r.GCSBaseline)
	}
}

func TestCheckVMHostRefusals(t *testing.T) {
	cases := []struct {
		name, id string
		want     Tri
		edit     func(*VMHostInput)
	}{
		{"foreign NAT", CheckNATExists, No, func(in *VMHostInput) {
			in.NATs = []LiveNAT{{Name: "OtherWorkload", Prefix: pfx("10.99.0.0/30")}}
		}},
		{"NAT with unknown prefix", CheckNATExists, No, func(in *VMHostInput) {
			in.NATs = []LiveNAT{{Name: "OtherWorkload"}}
		}},
		{"default route through a VPN", CheckDefaultRoute, No, func(in *VMHostInput) { in.Net.Egress = egressAll(ifWireGuard) }},
		{"routes not read", CheckDefaultRoute, Unknown, func(in *VMHostInput) { in.Net = HostNet{} }},
		{"routes read but no lookups", CheckDefaultRoute, No, func(in *VMHostInput) { in.Net.Egress = nil }},
		{"metered", CheckMetered, No, func(in *VMHostInput) { in.Network.Metered = true }},
		{"network unknown", CheckMetered, Unknown, func(in *VMHostInput) { in.Network = CurrentNetwork{} }},
		{"8 GB installed", CheckInstalledRAM, No, func(in *VMHostInput) { in.InstalledRAM = 8 * GiB }},
		{"RAM unread", CheckInstalledRAM, Unknown, func(in *VMHostInput) { in.InstalledRAM = 0 }},
		{"free RAM below guest + 1 GiB", CheckFreeRAM, No, func(in *VMHostInput) { in.FreeRAM = 7*GiB - 1 }},
		{"free RAM unread", CheckFreeRAM, Unknown, func(in *VMHostInput) { in.FreeRAM = 0 }},
		{"GCS unread", CheckGCSBaseline, Unknown, func(in *VMHostInput) { in.GCS = nil }},
	}
	for _, c := range cases {
		r := CheckVMHost(vmHostInput(c.edit))
		if got := checkOf(r, c.id); got.OK != c.want {
			t.Errorf("%s: %s = %s (%s), want %s", c.name, c.id, got.OK, got.Detail, c.want)
		}
		if r.Ready {
			t.Errorf("%s: still ready", c.name)
		}
	}
}

// TestCheckVMHostAcceptsItsOwnNATOnARerun: Windows keys every NAT's ID by its
// name ("name;0"). A Setup rerun must accept the NAT it recorded, or it
// refuses its own NAT and tells the user to remove it by hand (live run 5). A
// NAT that differs from the record in ID or prefix is still refused.
func TestCheckVMHostAcceptsItsOwnNATOnARerun(t *testing.T) {
	nat := LiveNAT{Name: "CashPilot-VM", ID: "CashPilot-VM;0", Prefix: pfx("172.31.250.0/29")}
	r := CheckVMHost(vmHostInput(func(in *VMHostInput) {
		in.RecordedNAT = nat
		in.NATs = []LiveNAT{nat}
	}))
	if got := checkOf(r, CheckNATExists); got.OK != Yes {
		t.Errorf("preflight = %+v, want the recorded NAT accepted", got)
	}
	for name, live := range map[string]LiveNAT{
		"other prefix": {Name: nat.Name, ID: nat.ID, Prefix: pfx("172.31.251.0/29")},
		"other ID":     {Name: nat.Name, ID: "CashPilot-VM;1", Prefix: nat.Prefix},
		"no record":    nat,
	} {
		r := CheckVMHost(vmHostInput(func(in *VMHostInput) {
			if name != "no record" {
				in.RecordedNAT = nat
			}
			in.NATs = []LiveNAT{live}
		}))
		got := checkOf(r, CheckNATExists)
		if got.OK != No || !strings.Contains(got.Detail, `A NAT network named "CashPilot-VM" already exists`) {
			t.Errorf("%s: preflight = %+v, want the foreign NAT refusal", name, got)
		}
	}
}

func TestCheckVMHostRAMEdges(t *testing.T) {
	// Exactly the guest plus 1 GiB free, and exactly 12 GiB installed, pass.
	r := CheckVMHost(vmHostInput(func(in *VMHostInput) { in.FreeRAM = 7 * GiB; in.InstalledRAM = MinHostRAM }))
	if !r.Ready {
		t.Errorf("the exact minimums failed: %+v", r.Checks)
	}
	// A lab guest of 4 GiB needs 5 GiB free, and a lab minimum lowers the
	// installed floor, in a lab build only: a product build keeps 12 GiB
	// whatever the input says.
	r = CheckVMHost(vmHostInput(func(in *VMHostInput) {
		in.GuestMemory, in.FreeRAM = 4*GiB, 5*GiB
		in.MinHostRAM, in.InstalledRAM = 8*GiB, 8*GiB
	}))
	if r.Ready != labBuild {
		t.Errorf("lab sizes with labBuild=%v: ready=%v, want %v: %+v", labBuild, r.Ready, labBuild, r.Checks)
	}
	// An empty GCS list is a valid baseline: nothing registered.
	r = CheckVMHost(vmHostInput(func(in *VMHostInput) { in.GCS = []string{} }))
	if got := checkOf(r, CheckGCSBaseline); got.OK != Yes || r.GCSBaseline == nil {
		t.Errorf("an empty GCS list: %+v %v", got, r.GCSBaseline)
	}
}
