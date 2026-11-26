package winvm

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

func requireRemoveKept(t *testing.T, code int, out []byte, reason string) {
	t.Helper()
	doc, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	if code != 0 || err != nil || !strings.Contains(string(doc), reason) {
		t.Fatalf("expected Remove to keep the object because %q, exit %d: %s (decoded: %s)", reason, code, out, doc)
	}
}

func TestRemoveFirewallScriptKeepsChangedRule(t *testing.T) {
	const mock = `
param($owner)
function Get-NetFirewallRule {
  [pscustomobject]@{ Name = 'CashPilotVM-block-in-' + $owner; Description = 'CashPilot Secure VM. cashpilot-owner=' + $owner; Enabled = 'True'; InstanceID = 'x' }
}
function Remove-NetFirewallRule { throw 'would remove the changed firewall rule' }
`
	// Supply a different live digest so the test exercises the removal guard.
	script := firewallPrelude + "\nfunction Get-FirewallIdentity($r) { 'live-changed' }\n" +
		strings.TrimPrefix(scriptRemoveFirewall, firewallPrelude)
	code, out := runOwnedScriptWithFake(t, mock, script,
		map[string]any{"group": "CashPilotVM", "id": "CashPilotVM-block-in-" + tOwner, "owner": tOwner, "identity": "recorded"}, tOwner)
	requireRemoveKept(t, code, out, "the firewall rule differs from Setup")
}

func TestRemoveNATScriptKeepsMappedOrReplacedNAT(t *testing.T) {
	const mock = `
param($id, $mapping)
function Get-NetNat { [pscustomobject]@{ Name = 'CashPilot-VM'; InstanceID = $id; InternalIPInterfaceAddressPrefix = '172.31.250.0/29' } }
function Get-NetNatStaticMapping { if ($mapping) { [pscustomobject]@{ NatName = 'CashPilot-VM' } } }
function Get-NetNatExternalAddress {}
function Remove-NetNat { throw 'would remove the unverified NAT' }
`
	for _, tc := range []struct{ name, id, mapping string }{
		{"static mapping", "nat-id", "yes"},
		{"other instance", "other-id", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out := runOwnedScriptWithFake(t, mock, scriptRemoveNAT,
				map[string]any{"nat": "CashPilot-VM", "id": "nat-id", "prefix": "172.31.250.0/29"}, tc.id, tc.mapping)
			if tc.mapping != "" {
				requireRemoveKept(t, code, out, "it has independent NAT mappings or external addresses")
			} else if code == 0 || !strings.Contains(string(out), "The NAT changed since inventory.") {
				t.Fatalf("expected the NAT identity refusal, exit %d: %s", code, out)
			}
		})
	}
}

func TestRemoveVMScriptRefusesIndependentConfiguration(t *testing.T) {
	const mock = `
param($vmId, $owner, $adapter, $change)
function Get-VM { [pscustomobject]@{ Id = $vmId; Notes = 'cashpilot-owner=' + $owner; State = 'Running' } }
function Get-VMNetworkAdapter {
  [pscustomobject]@{ Id = 'Microsoft:' + $vmId + '\' + $adapter }
  if ($change -eq 'extra adapter') { [pscustomobject]@{ Id = 'Microsoft:' + $vmId + '\other' } }
}
function Get-VMSwitchExtensionPortFeature {}
function Get-VMNetworkAdapterExtendedAcl {}
function Get-VMSnapshot { if ($change -eq 'checkpoint') { 'checkpoint' } }
function Get-VMHardDiskDrive {}
function Stop-VM { throw 'would stop a VM with independent configuration' }
function Remove-VM { throw 'would remove a VM with independent configuration' }
`
	for _, tc := range []struct{ change, digest, refusal string }{
		{"extra adapter", portACLDigest(nil), "The VM has another adapter"},
		{"checkpoint", portACLDigest(nil), "The VM has checkpoints"},
		{"ACL digest", portACLDigest([]PortACL{{ID: "recorded"}}), "The VM has independent port ACLs"},
	} {
		t.Run(tc.change, func(t *testing.T) {
			code, out := runOwnedScriptWithFake(t, mock, scriptRemoveVM,
				map[string]any{"vm": tVMID, "owner": tOwner, "adapter": tAdapterID, "aclDigest": tc.digest},
				tVMID, tOwner, tAdapterID, tc.change)
			if code == 0 || !strings.Contains(string(out), tc.refusal) {
				t.Fatalf("expected %q, exit %d: %s", tc.refusal, code, out)
			}
		})
	}
}

func TestRemoveGuardianScriptKeepsGuardianForForeignTPMVM(t *testing.T) {
	const mock = `
function Get-HgsGuardian { [pscustomobject]@{ SigningCertificate = [byte[]](1,2); EncryptionCertificate = [byte[]](3,4) } }
function Get-VM { [pscustomobject]@{ Name = 'decoy-tpm'; Generation = 2 } }
function Get-VMSecurity { [pscustomobject]@{ TpmEnabled = $true } }
function Get-VMKeyProtector { [byte[]](0,0,0,0) }
function Remove-HgsGuardian { throw 'would remove the guardian the decoy VM needs' }
`
	certificates := base64.StdEncoding.EncodeToString([]byte{1, 2}) + ":" + base64.StdEncoding.EncodeToString([]byte{3, 4})
	digest := sha256.Sum256([]byte(certificates))
	code, out := runOwnedScriptWithFake(t, mock, scriptRemoveGuardian, map[string]any{"id": hex.EncodeToString(digest[:])})
	if code == 0 || !strings.Contains(string(out), "A VM still needs the guardian.") {
		t.Fatalf("expected the guardian dependency refusal, exit %d: %s", code, out)
	}
}

func TestUnregisterGuardTaskScriptRefusesForeignTask(t *testing.T) {
	const mock = `
function Get-ScheduledTask { [pscustomobject]@{ Description = 'Decoy guard task'; State = 'Running' } }
function Stop-ScheduledTask { throw 'would stop the decoy task' }
function Unregister-ScheduledTask { throw 'would unregister the decoy task' }
`
	code, out := runOwnedScriptWithFake(t, mock, scriptUnregisterGuardTask,
		map[string]any{"taskPath": `\CashPilot\`, "taskName": "SecureVMGuard", "owner": tOwner})
	if code == 0 || !strings.Contains(string(out), "The scheduled task was not made by this install.") {
		t.Fatalf("expected the task ownership refusal, exit %d: %s", code, out)
	}
}

func TestRemoveSwitchScriptKeepsIndependentPortOrAddress(t *testing.T) {
	const mock = `
param($switchId, $owner, $extra)
function Get-VMSwitch { [pscustomobject]@{ Id = $switchId; Notes = 'cashpilot-owner=' + $owner; SwitchType = 'Internal' } }
function Get-VMNetworkAdapter {
  param([switch]$All, [switch]$ManagementOS)
  if ($ManagementOS) { [pscustomobject]@{ SwitchId = $switchId; Id = 'Microsoft:mgmt'; DeviceId = 'dev'; MacAddress = '00155D000002'; IsManagementOs = $true } }
  elseif ($All -and $extra -eq 'VM port') { [pscustomobject]@{ SwitchId = $switchId; IsManagementOs = $false; VMId = 'decoy-port' } }
}
function Get-NetAdapter { [pscustomobject]@{ DeviceId = 'dev'; ifIndex = 20; Name = 'vEthernet (CashPilot-VM)'; MacAddress = '00-15-5D-00-00-02' } }
function Get-NetIPInterface { [pscustomobject]@{ Dhcp = 'Disabled' } }
function Get-NetIPAddress {
  [pscustomobject]@{ IPAddress = '172.31.250.1'; PrefixLength = 29 }
  if ($extra -eq 'address') { [pscustomobject]@{ IPAddress = '10.9.9.1'; PrefixLength = 24 } }
}
function Get-NetNeighbor { [pscustomobject]@{ IPAddress = '172.31.250.2'; LinkLayerAddress = '00155D000003'; State = 'Permanent' } }
function Get-NetAdapterBinding { [pscustomobject]@{ Name = 'vEthernet (CashPilot-VM)'; ComponentID = 'ms_tcpip'; Enabled = $true } }
function Remove-VMSwitch { throw 'would remove the switch with independent configuration' }
`
	for _, tc := range []struct{ extra, reason string }{
		{"VM port", "a VM still has an adapter on it"},
		{"address", "the management port has independent addresses, neighbours or bindings"},
	} {
		t.Run(tc.extra, func(t *testing.T) {
			code, out := runOwnedScriptWithFake(t, mock, scriptRemoveSwitch,
				map[string]any{"switchId": tSwitchID, "owner": tOwner, "managementId": "Microsoft:mgmt",
					"mac": "00155D000002", "gateway": "172.31.250.1", "prefixLen": 29, "guest": "172.31.250.2", "guestMac": "00155D000003",
					"bindings": map[string]bool{"ms_tcpip": true}}, tSwitchID, tOwner, tc.extra)
			requireRemoveKept(t, code, out, tc.reason)
		})
	}
}
