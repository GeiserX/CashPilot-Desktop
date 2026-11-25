package winvm

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// PortACL is the identity and complete match of a Hyper-V port ACL setting.
// The provider represents Both as two distinct settings.
type PortACL struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Direction     uint8  `json:"direction"`
	Action        uint8  `json:"action"`
	Applicability uint8  `json:"applicability"`
	Type          uint8  `json:"type"`
	Local         string `json:"local"`
	LocalBits     uint8  `json:"localBits"`
	Remote        string `json:"remote"`
	RemoteBits    uint8  `json:"remoteBits"`
}

func (a PortACL) prefix() netip.Prefix {
	ip, _ := netip.ParseAddr(a.Remote)
	return netip.PrefixFrom(ip, int(a.RemoteBits)).Masked()
}

// portACLDigest lets Remove verify the same complete inventory immediately
// before deleting the VM, without handing the whole journal to PowerShell.
func portACLDigest(rows []PortACL) string {
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		b, _ := json.Marshal(row)
		keys = append(keys, string(b))
	}
	slices.Sort(keys)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(keys, "\n"))))
}

func (a PortACL) valid() bool {
	q := a.prefix()
	return a.ID != "" && len(a.ID) <= 512 && a.Name != "" && len(a.Name) <= 64 &&
		a.Direction >= 1 && a.Direction <= 2 && a.Action >= 1 && a.Action <= 2 &&
		a.Applicability == 2 && a.Local == "" && a.LocalBits == 0 && q.IsValid() &&
		((q.Addr().Is4() && a.Type == 2) || (q.Addr().Is6() && a.Type == 3))
}

func requireRecordedAdapter(p Plan, live LiveState) error {
	if p.AdapterID == "" || len(live.Adapters) != 1 || !strings.EqualFold(live.Adapters[0].ID, p.AdapterID) {
		return errors.New("the recorded VM adapter is missing or the VM has an additional adapter. Check the adapters in Hyper-V Manager")
	}
	return nil
}

const portACLPrelude = hvPrelude + `$aclFeature = '998BEF4A-5D55-492A-9C43-8B2F5EAE9F2B'
function Get-PortACLs($adapter) {
  Get-VMSwitchExtensionPortFeature -VMNetworkAdapter $adapter -FeatureId $aclFeature
}
function Export-PortACL($row) {
  $s = $row.SettingData
  [ordered]@{ id = [string]$s.InstanceID; name = [string]$s.Name
    direction = [int]$s.Direction; action = [int]$s.Action; applicability = [int]$s.Applicability; type = [int]$s.AclType
    local = [string]$s.LocalAddress; localBits = [int]$s.LocalAddressPrefixLength
    remote = [string]$s.RemoteAddress; remoteBits = [int]$s.RemoteAddressPrefixLength }
}
function Get-PortACLDigest($adapter) {
  [string[]]$keys = @(Get-PortACLs $adapter | ForEach-Object { ConvertTo-Json -InputObject (Export-PortACL $_) -Compress })
  [Array]::Sort($keys, [StringComparer]::Ordinal)
  $sha = [Security.Cryptography.SHA256]::Create()
  try { [BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes(($keys -join ` + "\"`n\"" + `)))).Replace('-', '').ToLowerInvariant() }
  finally { $sha.Dispose() }
}
function Test-PortACL($live, $record) {
  foreach ($k in $live.Keys) { if ($live[$k] -cne $record.$k) { return $false } }
  return $true
}
`

const scriptAddPortACLs = portACLPrelude + `$a = Get-RecordedVMAdapter $in.vm $in.owner $in.adapter $true
$out = @()
foreach ($remote in @($in.remotes)) {
  $ip, $bits = $remote.Split('/')
  $type = if ($ip.Contains(':')) { 3 } else { 2 }
  $names = @()
  $features = @(foreach ($direction in @(1, 2)) {
    $f = Get-VMSystemSwitchExtensionPortFeature -FeatureId $aclFeature
    $s = $f.SettingData
    if (-not $s) { throw 'Hyper-V did not return the port ACL feature.' }
    $s.Name = $in.owner + [guid]::NewGuid().ToString('N')
    $names += $s.Name
    $s.Action = [byte]$in.action
    $s.Direction = [byte]$direction
    $s.Applicability = [byte]2
    $s.AclType = [byte]$type
    $s.RemoteAddress = $ip
    $s.RemoteAddressPrefixLength = [byte]$bits
    $f
  })
  Add-VMSwitchExtensionPortFeature -VMNetworkAdapter $a -VMSwitchExtensionFeature $features | Out-Null
  $added = @(Get-PortACLs $a | Where-Object { $names -ccontains [string]$_.SettingData.Name })
  if ($added.Count -ne 2) { throw 'Hyper-V did not return both new ACL identities.' }
  $out += @($added | ForEach-Object { Export-PortACL $_ })
}
Write-Doc ([ordered]@{ rows = $out })
`

const scriptRemovePortACLs = portACLPrelude + `$a = Get-RecordedVMAdapter $in.vm $in.owner $in.adapter $true
$rows = @(Get-PortACLs $a)
$remove = @(foreach ($record in @($in.rows)) {
  $found = @($rows | Where-Object { [string]$_.SettingData.InstanceID -ceq $record.id })
  if ($found.Count -eq 0) { continue }
  if ($found.Count -ne 1 -or -not (Test-PortACL (Export-PortACL $found[0]) $record)) {
    throw 'A recorded port ACL has changed and was left in place.'
  }
  $found[0]
})
if ($remove.Count -gt 0) {
  Remove-VMSwitchExtensionPortFeature -VMNetworkAdapter $a -VMSwitchExtensionFeature $remove -Confirm:$false
}
Write-Doc ([ordered]@{ ok = $true })
`

func (h *psHost) AddPortACLs(ctx context.Context, p Plan, action string, ps []netip.Prefix) ([]PortACL, error) {
	arg, err := portACLArgs(p)
	if err != nil {
		return nil, err
	}
	if _, err := checkDenies(ps); action == "Deny" && err != nil {
		return nil, err
	}
	var code uint8
	switch action {
	case "Deny":
		code = 2
	case "Allow":
		code = 1
		if len(ps) != 1 || !ps[0].IsSingleIP() || ps[0].Addr().Zone() != "" {
			return nil, errors.New("a proof Allow requires one host address")
		}
	default:
		return nil, fmt.Errorf("invalid port ACL action %q", action)
	}
	arg["action"] = code
	var result []PortACL
	for batch := range slices.Chunk(ps, 32) {
		arg["remotes"] = batch
		var out struct {
			Rows []PortACL `json:"rows"`
		}
		if err := h.call(ctx, scriptAddPortACLs, arg, &out); err != nil {
			return result, err
		}
		var confirmed []PortACL
		for _, q := range batch {
			var dirs uint8
			for _, a := range out.Rows {
				if a.valid() && strings.HasPrefix(a.Name, p.Owner) && a.Action == code && a.prefix() == q {
					dirs |= a.Direction
					confirmed = append(confirmed, a)
				}
			}
			if dirs != 3 {
				return result, fmt.Errorf("Hyper-V did not confirm both directions for %s %s", action, q)
			}
		}
		if len(confirmed) != 2*len(batch) || len(out.Rows) != len(confirmed) {
			return result, errors.New("Hyper-V returned unexpected ACL identities")
		}
		result = append(result, confirmed...)
	}
	return result, nil
}

func (h *psHost) RemovePortACLs(ctx context.Context, p Plan, rows []PortACL) error {
	arg, err := portACLArgs(p)
	if err != nil {
		return err
	}
	for _, a := range rows {
		if !a.valid() || !strings.HasPrefix(a.Name, p.Owner) {
			return errors.New("port ACL has no verifiable recorded identity")
		}
	}
	for batch := range slices.Chunk(rows, 32) {
		arg["rows"] = batch
		if err := h.ok(ctx, scriptRemovePortACLs, arg); err != nil {
			return err
		}
	}
	return nil
}

func portACLArgs(p Plan) (map[string]any, error) {
	if err := checkVMID("VM ID", p.VMID); err != nil {
		return nil, err
	}
	if err := checkVMID("VM adapter ID", p.AdapterID); err != nil {
		return nil, err
	}
	if err := checkOwner(p.Owner); err != nil {
		return nil, err
	}
	return map[string]any{"vm": p.VMID, "adapter": p.AdapterID, "owner": p.Owner}, nil
}
