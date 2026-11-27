package winvm

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"math/big"
	"net/netip"
	"strings"
	"testing"
	"time"
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
function Get-NetFirewallInterfaceFilter { [pscustomobject]@{ InterfaceAlias = 'vEthernet (CashPilotVM)' } }
function Remove-NetFirewallRule { throw 'would remove the changed firewall rule' }
`
	// Supply a different live digest so the test exercises the removal guard.
	script := firewallPrelude + "\nfunction Get-FirewallIdentity($r) { 'live-changed' }\n" +
		strings.TrimPrefix(scriptRemoveFirewall, firewallPrelude)
	code, out := runOwnedScriptWithFake(t, mock, script,
		map[string]any{"group": "CashPilotVM", "id": "CashPilotVM-block-in-" + tOwner, "owner": tOwner, "identity": "recorded", "alias": "vEthernet (CashPilotVM)"}, tOwner)
	requireRemoveKept(t, code, out, "the firewall rule differs from Setup")
}

func TestRemoveNATScriptKeepsMappedOrReplacedNAT(t *testing.T) {
	const mock = `
param($id, $mapping)
function Get-NetNat { [pscustomobject]@{ Name = 'CashPilot-VM'; InstanceID = $id; InternalIPInterfaceAddressPrefix = '172.31.250.0/29' } }
function Get-NetNatStaticMapping { if ($mapping) { [pscustomobject]@{ NatName = 'CashPilot-VM' } } }
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
				requireRemoveKept(t, code, out, "it has independent NAT mappings")
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

// guardianCertificates keeps the certificates in memory and leaves the Windows
// certificate store alone. Hash the raw bytes here to check the legacy identity.
func guardianCertificates(t *testing.T) (string, string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := func(serial int64, name string) string {
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(serial),
			Subject:      pkix.Name{CommonName: name},
			NotBefore:    time.Unix(1700000000, 0),
			NotAfter:     time.Unix(2100000000, 0),
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		return base64.StdEncoding.EncodeToString(der)
	}
	signing, encryption := cert(1, "Test signing"), cert(2, "Test encryption")
	digest := sha256.Sum256([]byte(signing + ":" + encryption))
	return signing, encryption, hex.EncodeToString(digest[:])
}

const guardianCertificateMock = `
param($signing, $encryption, $shape)
$ErrorActionPreference = 'Stop'
$signingBytes = [Convert]::FromBase64String($signing)
$encryptionBytes = [Convert]::FromBase64String($encryption)
switch ($shape) {
  'objects' {
    $sign = [Security.Cryptography.X509Certificates.X509Certificate2]::new($signingBytes)
    $encrypt = [Security.Cryptography.X509Certificates.X509Certificate2]::new($encryptionBytes)
  }
  'base objects' {
    $sign = [Security.Cryptography.X509Certificates.X509Certificate]::new($signingBytes)
    $encrypt = [Security.Cryptography.X509Certificates.X509Certificate]::new($encryptionBytes)
  }
  'bytes' { $sign = $signingBytes; $encrypt = $encryptionBytes }
  'invalid' { $sign = 'unexpected'; $encrypt = $encryptionBytes }
}
function Get-HgsGuardian { [pscustomobject]@{ SigningCertificate = $sign; EncryptionCertificate = $encrypt } }
`

func runGuardianScript(t *testing.T, mock, script string, arg map[string]any, signing, encryption, shape string) (int, []byte) {
	t.Helper()
	return runOwnedScriptWithFake(t, guardianCertificateMock+mock, "try {\n"+script+`
} finally {
  if ($sign -is [Security.Cryptography.X509Certificates.X509Certificate]) { $sign.Reset() }
  if ($encrypt -is [Security.Cryptography.X509Certificates.X509Certificate]) { $encrypt.Reset() }
}
`, arg, signing, encryption, shape)
}

func TestGuardianIdentityAcceptsCertificateObjectsAndBytes(t *testing.T) {
	signing, encryption, want := guardianCertificates(t)
	script := buildPrelude + `Write-Doc @{ id = (Get-GuardianIdentity (Get-HgsGuardian)) }`
	for _, shape := range []string{"objects", "base objects", "bytes", "invalid"} {
		t.Run(shape, func(t *testing.T) {
			code, out := runGuardianScript(t, "", script, nil, signing, encryption, shape)
			if shape == "invalid" {
				if code == 0 || !strings.Contains(string(out), "guardian signing certificate has unsupported type") {
					t.Fatalf("expected a certificate type error, exit %d: %s", code, out)
				}
				return
			}
			doc, err := decodeOutput(out)
			if code != 0 || err != nil {
				t.Fatalf("exit %d, decode %v: %s", code, err, out)
			}
			var got struct{ ID string }
			if err := decodeDoc(doc, &got); err != nil || got.ID != want {
				t.Fatalf("identity = %q, want %q, decode %v: %s", got.ID, want, err, doc)
			}
		})
	}
}

func TestRemoveGuardianScriptKeepsGuardianForForeignTPMVM(t *testing.T) {
	const mock = `
function Get-VM { [pscustomobject]@{ Name = 'decoy-tpm'; Generation = 2 } }
function Get-VMSecurity { [pscustomobject]@{ TpmEnabled = $true } }
function Get-VMKeyProtector { [byte[]](0,0,0,0) }
function Remove-HgsGuardian { throw 'would remove the guardian the decoy VM needs' }
`
	signing, encryption, digest := guardianCertificates(t)
	code, out := runGuardianScript(t, mock, scriptRemoveGuardian, map[string]any{"id": digest}, signing, encryption, "objects")
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

// TestRemoveSwitchScriptIgnoresWindowsNeighbours: Windows adds Permanent
// neighbour entries of its own to the host vNIC: multicast in 224.0.0.0/4 and
// the subnet broadcast. Live run 5 kept the switch because 239.255.255.250 and
// 172.31.250.7 counted as user configuration. A neighbour a person added
// still keeps it.
func TestRemoveSwitchScriptIgnoresWindowsNeighbours(t *testing.T) {
	const mock = `
param($switchId, $owner, $extra)
function Get-VMSwitch { [pscustomobject]@{ Id = $switchId; Notes = 'cashpilot-owner=' + $owner; SwitchType = 'Internal' } }
function Get-VMNetworkAdapter {
  param([switch]$All, [switch]$ManagementOS)
  if ($ManagementOS) { [pscustomobject]@{ SwitchId = $switchId; Id = 'Microsoft:mgmt'; DeviceId = 'dev'; MacAddress = '00155D000002'; IsManagementOs = $true } }
}
function Get-NetAdapter { [pscustomobject]@{ DeviceId = 'dev'; ifIndex = 20; Name = 'vEthernet (CashPilot-VM)'; MacAddress = '00-15-5D-00-00-02' } }
function Get-NetIPInterface { [pscustomobject]@{ Dhcp = 'Disabled' } }
function Get-NetIPAddress { [pscustomobject]@{ IPAddress = '172.31.250.1'; PrefixLength = 29 } }
function Get-NetNeighbor {
  foreach ($n in @(@('255.255.255.255', 'FF-FF-FF-FF-FF-FF'), @('239.255.255.250', '01-00-5E-7F-FF-FA'), @('224.0.0.252', '01-00-5E-00-00-FC'),
      @('224.0.0.22', '01-00-5E-00-00-16'), @('172.31.250.7', 'FF-FF-FF-FF-FF-FF'), @('172.31.250.2', '00-15-5D-00-00-03'))) {
    [pscustomobject]@{ IPAddress = $n[0]; LinkLayerAddress = $n[1]; State = 'Permanent' }
  }
  if ($extra -eq 'user neighbour') { [pscustomobject]@{ IPAddress = '172.31.250.5'; LinkLayerAddress = '00-15-5D-00-00-09'; State = 'Permanent' } }
}
function Get-NetAdapterBinding { [pscustomobject]@{ Name = 'vEthernet (CashPilot-VM)'; ComponentID = 'ms_tcpip'; Enabled = $true } }
function Remove-VMSwitch { if ($extra -eq 'user neighbour') { throw 'would remove the switch with a user neighbour' } }
`
	arg := map[string]any{"switchId": tSwitchID, "owner": tOwner, "managementId": "Microsoft:mgmt",
		"mac": "00155D000002", "gateway": "172.31.250.1", "prefixLen": 29, "guest": "172.31.250.2", "guestMac": "00-15-5D-00-00-03",
		"bindings": map[string]bool{"ms_tcpip": true}, "broadcast": "172.31.250.7"}
	code, out := runOwnedScriptWithFake(t, mock, scriptRemoveSwitch, arg, tSwitchID, tOwner, "none")
	doc, err := decodeOutput(out)
	var got struct{ Kept string }
	if code != 0 || err != nil || decodeDoc(doc, &got) != nil || got.Kept != "" {
		t.Fatalf("Windows' own neighbours kept the switch: exit %d, %v: %s (%s)", code, err, out, doc)
	}
	code, out = runOwnedScriptWithFake(t, mock, scriptRemoveSwitch, arg, tSwitchID, tOwner, "user neighbour")
	requireRemoveKept(t, code, out, "the management port has independent addresses, neighbours or bindings")
}

func TestSubnetBroadcast(t *testing.T) {
	for in, want := range map[string]string{"172.31.250.0/29": "172.31.250.7", "10.0.0.0/8": "10.255.255.255", "192.168.1.8/30": "192.168.1.11"} {
		if got := subnetBroadcast(netip.MustParsePrefix(in)).String(); got != want {
			t.Errorf("broadcast of %s = %s, want %s", in, got, want)
		}
	}
}

// TestRemoveNATScriptIgnoresWinNATPortPools: WinNAT lists external address
// port pools of its own on every NAT, two per host IPv4 address (live run 5:
// 127.0.0.1 and every host address, 100 ports each). Remove kept the NAT for
// them. Only a static mapping keeps it now.
func TestRemoveNATScriptIgnoresWinNATPortPools(t *testing.T) {
	const mock = `
function Get-NetNat { [pscustomobject]@{ Name = 'CashPilot-VM'; InstanceID = 'CashPilot-VM;0'; InternalIPInterfaceAddressPrefix = '172.31.250.0/29' } }
function Get-NetNatStaticMapping {}
function Get-NetNatExternalAddress {
  foreach ($ip in '127.0.0.1', '127.0.0.1', '172.22.250.118', '172.22.250.118') {
    [pscustomobject]@{ NatName = 'CashPilot-VM'; IPAddress = $ip; PortStart = 50270; PortEnd = 50369 }
  }
}
$global:removed = $false
function Remove-NetNat { $global:removed = $true }
`
	code, out := runOwnedScriptWithFake(t, mock, scriptRemoveNAT+"\nif (-not $global:removed) { throw 'the NAT was not removed' }\n",
		map[string]any{"nat": "CashPilot-VM", "id": "CashPilot-VM;0", "prefix": "172.31.250.0/29"})
	doc, err := decodeOutput(out)
	var got struct{ Kept string }
	if code != 0 || err != nil || decodeDoc(doc, &got) != nil || got.Kept != "" {
		t.Fatalf("WinNAT's own port pools kept the NAT: exit %d, %v: %s (%s)", code, err, out, doc)
	}
}

// firewallRuleMock is Setup's rule with every filter Get-FirewallIdentity
// reads. $alias is its interface filter, $adapters the host's adapter GUIDs.
const firewallRuleMock = `
param($owner, $alias, $adapters)
function Get-NetFirewallRule {
  [CmdletBinding()] param($Group, $Name)
  [pscustomobject]@{ InstanceID = 'CashPilotVM-block-in-' + $owner; Name = 'CashPilotVM-block-in-' + $owner; Group = 'CashPilotVM'
    Description = 'CashPilot Secure VM. cashpilot-owner=' + $owner; Direction = 'Inbound'; Action = 'Block'; Profile = 'Any'; Enabled = 'True'
    EdgeTraversalPolicy = 'Block'; LooseSourceMapping = 'False'; LocalOnlyMapping = 'False' }
}
function Get-NetFirewallAddressFilter { [pscustomobject]@{ LocalAddress = 'Any'; RemoteAddress = 'Any' } }
function Get-NetFirewallPortFilter { [pscustomobject]@{ Protocol = 'Any'; LocalPort = 'Any'; RemotePort = 'Any'; IcmpType = 'Any'; DynamicTarget = 'Any' } }
function Get-NetFirewallInterfaceFilter { [pscustomobject]@{ InterfaceAlias = $alias } }
function Get-NetFirewallApplicationFilter { [pscustomobject]@{ Program = 'Any'; Package = '' } }
function Get-NetFirewallServiceFilter { [pscustomobject]@{ Service = 'Any' } }
function Get-NetFirewallInterfaceTypeFilter { [pscustomobject]@{ InterfaceType = 'Any' } }
function Get-NetFirewallSecurityFilter { [pscustomobject]@{ Authentication = 'NotRequired'; Encryption = 'NotRequired'; OverrideBlockRules = 'False'; LocalUser = 'Any'; RemoteUser = 'Any'; RemoteMachine = 'Any' } }
function Get-NetAdapter { [CmdletBinding()] param([switch]$IncludeHidden) foreach ($g in @($adapters -split ',' | Where-Object { $_ })) { [pscustomobject]@{ InterfaceGuid = $(if ($g -eq 'null') { $null } else { '{' + $g + '}' }) } } }
$global:removed = $false
function Remove-NetFirewallRule { [CmdletBinding()] param($InputObject) $global:removed = $true }
`

// TestRemoveFirewallScriptAcceptsTheDeletedVNIC: deleting the switch makes
// Windows rewrite the rule's interface to the deleted vNIC's GUID, so the
// identity recorded at Setup no longer matched and Remove left the rule (live
// run 5). Remove accepts that rewrite only when no adapter has the GUID.
func TestRemoveFirewallScriptAcceptsTheDeletedVNIC(t *testing.T) {
	const alias = "vEthernet (CashPilotVM)"
	const guid = "d06d4780-fd91-4a3a-b948-509c99ad604a"
	code, out := runFakePowerShell(t, firewallRuleMock+"\n"+firewallPrelude+"\nWrite-Doc @{ id = (Get-FirewallIdentity (Get-NetFirewallRule)) }\n", tOwner, alias, "")
	doc, err := decodeOutput(out)
	var rec struct{ ID string }
	if code != 0 || err != nil || decodeDoc(doc, &rec) != nil || rec.ID == "" {
		t.Fatalf("recorded identity: exit %d, %v: %s", code, err, out)
	}
	arg := map[string]any{"group": "CashPilotVM", "id": "CashPilotVM-block-in-" + tOwner, "owner": tOwner, "identity": rec.ID, "alias": alias}
	check := scriptRemoveFirewall + "\nif (-not $global:removed) { throw 'the rule was not removed' }\n"
	for _, tc := range []struct{ name, iface, adapters string }{
		{"vNIC still there", alias, guid},
		{"vNIC deleted", guid, ""},
		// Some hidden adapters have no interface GUID; the check must not throw on them.
		{"vNIC deleted, an adapter without a GUID", guid, "null"},
	} {
		code, out := runOwnedScriptWithFake(t, firewallRuleMock, check, arg, tOwner, tc.iface, tc.adapters)
		doc, err := decodeOutput(out)
		var got struct{ Kept string }
		if code != 0 || err != nil || decodeDoc(doc, &got) != nil || got.Kept != "" {
			t.Fatalf("%s: Remove left Setup's rule: exit %d, %v: %s (%s)", tc.name, code, err, out, doc)
		}
	}
	for _, tc := range []struct{ name, iface, adapters string }{
		{"rule moved to a live adapter", guid, guid},
		{"rule moved to another alias", "Ethernet", ""},
	} {
		code, out := runOwnedScriptWithFake(t, firewallRuleMock, scriptRemoveFirewall, arg, tOwner, tc.iface, tc.adapters)
		requireRemoveKept(t, code, out, "the firewall rule differs from Setup")
		_ = tc.name
	}
}

// TestRemoveGuardianScriptAnswersOnlyItsDocument: Remove-HgsGuardian warns
// that it leaves the guardian's two certificates. A non-interactive
// PowerShell prints the warning on standard output, so on live run 5 the
// answer was not base64 and Remove never finished. Remove now answers only
// its document.
func TestRemoveGuardianScriptAnswersOnlyItsDocument(t *testing.T) {
	const mock = `
function Get-VM {}
$global:removed = $false
function Remove-HgsGuardian {
  [CmdletBinding()] param($InputObject, [switch]$Confirm)
  $global:removed = $true
  Write-Warning 'The guardian has been removed successfully. Manually remove the following certificates associated with the guardian.'
  'UntrustedGuardian'
}
`
	signing, encryption, digest := guardianCertificates(t)
	check := scriptRemoveGuardian + "\nif (-not $global:removed) { throw 'the guardian was not removed' }\n"
	code, out := runGuardianScript(t, mock, check, map[string]any{"id": digest}, signing, encryption, "objects")
	if _, err := decodeOutput(out); code != 0 || err != nil {
		t.Fatalf("exit %d, decode %v: %s", code, err, out)
	}
}

// TestRemoveGuardianScriptKeepsItsCertificates: the guardian's certificates
// carry the private keys a copy or backup of the VM needs to start its vTPM.
// Remove deletes the guardian and leaves them, and the Go side reports them.
func TestRemoveGuardianScriptKeepsItsCertificates(t *testing.T) {
	const mock = `
function Get-VM {}
$global:deleted = @()
function Remove-HgsGuardian { [CmdletBinding()] param($InputObject, [switch]$Confirm) }
function Test-Path { param([string]$LiteralPath) $true }
function Remove-Item { param([string]$LiteralPath, [switch]$DeleteKey) $global:deleted += $LiteralPath }
`
	signing, encryption, digest := guardianCertificates(t)
	check := scriptRemoveGuardian + "\nif ($global:deleted.Count -gt 0) { throw ('deleted=' + ($global:deleted -join ';')) }\n"
	code, out := runGuardianScript(t, mock, check, map[string]any{"id": digest}, signing, encryption, "bytes")
	if _, err := decodeOutput(out); code != 0 || err != nil {
		t.Fatalf("Remove deleted the guardian's certificates: exit %d, decode %v: %s", code, err, out)
	}
}
