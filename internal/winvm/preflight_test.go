package winvm

import "testing"

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
