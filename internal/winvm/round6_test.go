package winvm

import (
	"context"
	"encoding/base64"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestR6HeaderAloneIsIncomplete(t *testing.T) {
	if _, err := parsePcapng(pcapng()[:28]); err == nil {
		t.Fatal("a section header alone counts as complete evidence")
	}
}

func TestR6PositiveCanaryCannotMaskCaptureFailure(t *testing.T) {
	s := StepSpec{ID: StepRed1LAN, Kind: Red, Applies: true}
	e := Evidence{Arrived: []string{"witness"}, Err: "pktmon: missing component export"}
	if got := Judge(s, e); got.Outcome != Broken {
		t.Fatalf("control passed with incomplete capture evidence: %+v", got)
	}
}

func TestR6DisconnectLeavesReplacementAdapter(t *testing.T) {
	const mock = `
param($fixtureVM, $fixtureOwner)
function Get-VM { [pscustomobject]@{ Id = $fixtureVM; Notes = 'cashpilot-owner=' + $fixtureOwner } }
function Get-VMNetworkAdapter { [pscustomobject]@{ Id = '00000000-0000-4000-8000-000000000123'; SwitchName = 'owned' } }
function Disconnect-VMNetworkAdapter { throw 'would disconnect replacement NIC' }
`
	code, out := runOwnedScriptWithFake(t, mock, scriptDisconnect,
		map[string]any{"vm": tVMID, "adapter": tAdapterID, "owner": tOwner}, tVMID, tOwner)
	if code == 0 || !strings.Contains(string(out), "recorded VM adapter") {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestR6RemoveSwitchLeavesIndependentManagementPort(t *testing.T) {
	const mock = `
param($fixtureSwitch, $fixtureOwner)
function Get-VMSwitch { [pscustomobject]@{ Id = $fixtureSwitch; Notes = 'cashpilot-owner=' + $fixtureOwner; SwitchType = 'Internal' } }
function Get-VMNetworkAdapter {
  [pscustomobject]@{ SwitchId = $fixtureSwitch; IsManagementOs = $true; Id = $fixtureSwitch }
  [pscustomobject]@{ SwitchId = $fixtureSwitch; IsManagementOs = $true; Id = 'foreign' }
}
function Remove-VMSwitch { throw 'would destroy independent management port' }
`
	code, out := runOwnedScriptWithFake(t, mock, scriptRemoveSwitch,
		map[string]any{"switchId": tSwitchID, "managementId": tSwitchID, "owner": tOwner}, tSwitchID, tOwner)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	if err != nil || !strings.Contains(string(raw), "management port") {
		t.Fatalf("expected a report about the management port, got %s (%v)", raw, err)
	}
}

func TestR6ManagementAdapterGate(t *testing.T) {
	const mock = `
param($switchId, $tag, $portId, $dhcp)
function Get-VMSwitch { [pscustomobject]@{ Id = $switchId; Name = 'CashPilot-VM'; SwitchType = 'Internal'; Notes = 'cashpilot-owner=' + $tag } }
function Get-VMNetworkAdapter { [pscustomobject]@{ SwitchId = $switchId; Id = $portId; DeviceId = 'device'; MacAddress = '00155D000002' } }
function Get-NetAdapter { [pscustomobject]@{ DeviceId = 'device'; ifIndex = 20; MacAddress = '00-15-5D-00-00-02' } }
function Get-NetIPAddress {}
function Get-NetIPInterface { [pscustomobject]@{ Dhcp = $dhcp } }
function New-NetIPAddress { throw 'would add a gateway to an unverified adapter' }
function Set-NetIPInterface { throw 'would change DHCP on an existing adapter' }
function Set-VMNetworkAdapter { throw 'would change the MAC on an existing adapter' }
`
	for _, tc := range []struct{ name, port, dhcp, detail string }{
		{"replacement", "different-port", "Disabled", "recorded management adapter is missing"},
		{"DHCP enabled", "recorded-port", "Enabled", "DHCP is enabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out := runOwnedScriptWithFake(t, mock, scriptEnsureSwitch,
				map[string]any{"switch": "CashPilot-VM", "switchId": tSwitchID, "managementId": "recorded-port",
					"owner": tOwner, "mac": "00155D000002", "gateway": "172.31.250.1", "prefixLen": 29}, tSwitchID, tOwner, tc.port, tc.dhcp)
			if code == 0 || !strings.Contains(string(out), tc.detail) {
				t.Fatalf("exit %d: %s", code, out)
			}
		})
	}
}

func TestR6RemoveWrapperPreservesChildExitAndReportsInventory(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("internal/runtime/scripts/secure-vm-directory.ps1", `function Open-CPDDirectory { [IO.MemoryStream]::new() }`)
	write("internal/runtime/scripts/secure-vm-files.ps1", "")
	wrapper, err := filepath.Abs("../../scripts/winvm-lab/t2-remove.ps1")
	if err != nil {
		t.Fatal(err)
	}
	const mock = `
param($WrapperPath, $FixtureRoot, $Assert)
function Get-FileHash { [pscustomobject]@{ Hash = 'unused' } }
function Get-VM { [pscustomobject]@{ Name = 'cpd-svm-1'; Id = 'foreign' } }
function Get-VMSwitch {}
function Get-NetNat {}
function Get-NetFirewallRule {}
function Get-ScheduledTask {}
$extra = @{}
if ($Assert -eq 'yes') { $extra.AssertClean = $true }
& $WrapperPath -Instance 1 -Exe 'unused.exe' -Repo $FixtureRoot @extra
exit $LASTEXITCODE
`
	for _, tc := range []struct {
		name, child, assertion string
		want                   int
	}{
		{"report foreign VM", "0", "no", 0},
		{"assert clean", "0", "yes", 1},
		{"pass through child failure", "23", "no", 23},
	} {
		t.Run(tc.name, func(t *testing.T) {
			write("internal/winvm/scripts/remove.ps1", "param($Exe, $ExeHash, $DataDir, $TaskPath, $TaskName)\nexit "+tc.child)
			code, out := runFakePowerShell(t, mock, wrapper, root, tc.assertion)
			if code != tc.want || !strings.Contains(string(out), "remove exit code: "+tc.child) ||
				!strings.Contains(string(out), "clean host: False") || !strings.Contains(string(out), "left: vm foreign") {
				t.Fatalf("exit %d, want %d: %s", code, tc.want, out)
			}
		})
	}
}

func TestR6PktmonRequiresTraceMetadataForEveryExpectedNIC(t *testing.T) {
	ctx := context.Background()
	const list = `[{"Id":4,"Name":"Ethernet","Type":2,"ifIndex":7},{"Id":9,"Name":"vEthernet","Type":6,"ifIndex":20}]`
	h, r, _ := newSeam(`{"list":`+jsonQuote(list)+`}`, cannedHostNet, `{"ok":true}`)
	mockCaptureStart(h, r)
	p := &psProber{h: h, owner: tOwner, switchID: tSwitchID, tmp: filepath.Join(t.TempDir(), proofDir)}
	capture, err := p.Pktmon(ctx, []PktFilter{{Addr: netip.MustParseAddr("192.0.2.50")}})
	if err != nil {
		t.Fatal(err)
	}
	r.answers = append(r.answers, `{"components":[{"ID":4,"Name":"Ethernet","Type":2,"IfIndex":7}]}`)
	if _, err := capture.Stop(ctx); err == nil || !strings.Contains(err.Error(), "trace is missing NIC 9") {
		t.Fatalf("Stop = %v, want missing trace NIC 9", err)
	}
}

func TestR6PortACLRemovalMatchesTheCompleteRecordedRow(t *testing.T) {
	const mock = `
param($fixtureVM, $fixtureOwner, $fixtureAdapter)
function Get-VM { [pscustomobject]@{ Id = $fixtureVM; Notes = 'cashpilot-owner=' + $fixtureOwner } }
function Get-VMNetworkAdapter { [pscustomobject]@{ Id = $fixtureAdapter } }
function Get-VMSwitchExtensionPortFeature {
  $base = @{ InstanceID = 'recorded'; Name = $fixtureOwner; Direction = 1; Action = 2
    Applicability = 2; AclType = 2; LocalAddress = ''; LocalAddressPrefixLength = 0
    RemoteAddress = '192.0.2.10'; RemoteAddressPrefixLength = 32 }
  [pscustomobject]@{ SettingData = [pscustomobject]$base }
  $other = $base.Clone()
  $other.InstanceID = 'foreign'
  $other.Direction = 2
  $other.LocalAddress = '192.0.2.100'
  $other.LocalAddressPrefixLength = 32
  [pscustomobject]@{ SettingData = [pscustomobject]$other }
}
function Remove-VMSwitchExtensionPortFeature {
  param($VMNetworkAdapter, $VMSwitchExtensionFeature, [switch]$Confirm)
  if ($VMSwitchExtensionFeature.Count -ne 1 -or $VMSwitchExtensionFeature[0].SettingData.InstanceID -ne 'recorded') {
    throw 'would remove the independent rule'
  }
}
`
	base := PortACL{ID: "recorded", Name: tOwner, Direction: 1, Action: 2, Applicability: 2,
		Type: 2, Remote: "192.0.2.10", RemoteBits: 32}
	args := map[string]any{"vm": tVMID, "owner": tOwner, "adapter": tAdapterID, "rows": []PortACL{base}}
	if code, out := runOwnedScriptWithFake(t, mock, scriptRemovePortACLs, args, tVMID, tOwner, tAdapterID); code != 0 {
		t.Fatalf("remove exact row: exit %d: %s", code, out)
	}
	base.Direction = 2
	args["rows"] = []PortACL{base}
	code, out := runOwnedScriptWithFake(t, mock, scriptRemovePortACLs, args, tVMID, tOwner, tAdapterID)
	if code == 0 || !strings.Contains(string(out), "was left in place") {
		t.Fatalf("remove changed row: exit %d: %s", code, out)
	}
}
