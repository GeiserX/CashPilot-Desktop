package runtime

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf16"
)

// A native binary's firewall_block is kept as one outbound Windows Firewall rule per
// service, scoped to that binary's path. This file holds the parts that do not need
// Windows: the rule's name, the PowerShell that reads, creates and deletes it, and the
// check that a rule found on the machine is the one we asked for. The calls that run
// PowerShell live in firewall_windows.go.
//
// Why a firewall rule and why outbound by program: Mysterium on Windows runs with
// --userspace, where every customer connection is a socket the node process opens
// itself. A rule matching that program and those remote addresses therefore stops
// exactly the customer traffic, which Windows cannot do for traffic it forwards.

// firewallRuleName is the rule's display name. The firewall's COM interface looks
// rules up by display name, which is how the unelevated check finds it.
func firewallRuleName(slug string) string {
	return "CashPilot: block " + slug + " from private networks"
}

// validFirewallNetworks parses every entry as an IPv4 CIDR and returns them in
// canonical form. A value that is not a plain CIDR never reaches PowerShell.
func validFirewallNetworks(nets []string) ([]string, error) {
	out := make([]string, 0, len(nets))
	for _, n := range nets {
		ip, ipNet, err := net.ParseCIDR(strings.TrimSpace(n))
		if err != nil || ip.To4() == nil {
			return nil, fmt.Errorf("firewall_block entry %q is not an IPv4 CIDR", n)
		}
		out = append(out, ipNet.String())
	}
	return out, nil
}

// psQuote makes a PowerShell single-quoted literal: inside one, only ' is special
// and it is escaped by doubling.
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// psEncode is the -EncodedCommand form of a script: base64 of its UTF-16LE bytes.
func psEncode(script string) string {
	units := utf16.Encode([]rune(script))
	b := make([]byte, 2*len(units))
	for i, u := range units {
		binary.LittleEndian.PutUint16(b[2*i:], u)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// firewallCheckScript reads the rule and whether the firewall is on for the
// networks the machine is connected to. It runs unelevated: the HNetCfg.FwPolicy2
// COM object can be read by a standard user.
func firewallCheckScript(name string) string {
	return `$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$fw = New-Object -ComObject HNetCfg.FwPolicy2
$cur = $fw.CurrentProfileTypes
$off = $false
foreach ($p in 1, 2, 4) { if (($cur -band $p) -and -not $fw.FirewallEnabled($p)) { $off = $true } }
$r = $null
try { $r = $fw.Rules.Item(` + psQuote(name) + `) } catch { }
$o = [ordered]@{ firewallOff = $off; found = ($null -ne $r) }
if ($r) { $o.program = $r.ApplicationName; $o.remote = $r.RemoteAddresses; $o.direction = $r.Direction; $o.action = $r.Action; $o.enabled = [bool]$r.Enabled; $o.profiles = $r.Profiles }
$o | ConvertTo-Json -Compress
`
}

// firewallCreateScript replaces any rule of that name with the one we want. It
// needs admin rights.
func firewallCreateScript(name, program string, nets []string) string {
	quoted := make([]string, len(nets))
	for i, n := range nets {
		quoted[i] = psQuote(n)
	}
	return `$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
Get-NetFirewallRule -DisplayName ` + psQuote(name) + ` -ErrorAction SilentlyContinue | Remove-NetFirewallRule
New-NetFirewallRule -DisplayName ` + psQuote(name) + ` -Description ` + psQuote("Created by CashPilot. Keeps the VPN customers of this program off your private networks. Deleted when the service is removed from CashPilot.") + ` -Direction Outbound -Action Block -Program ` + psQuote(program) + ` -RemoteAddress ` + strings.Join(quoted, ",") + ` -Profile Any -Enabled True | Out-Null
`
}

// firewallRemoveScript deletes the rule. It needs admin rights.
func firewallRemoveScript(name string) string {
	return `$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
Get-NetFirewallRule -DisplayName ` + psQuote(name) + ` -ErrorAction SilentlyContinue | Remove-NetFirewallRule
`
}

// firewallState is what firewallCheckScript prints. Direction 2 is outbound, Action
// 0 is block, Profiles 0x7FFFFFFF is every profile (the COM interface's values).
type firewallState struct {
	FirewallOff bool   `json:"firewallOff"`
	Found       bool   `json:"found"`
	Program     string `json:"program"`
	Remote      string `json:"remote"`
	Direction   int    `json:"direction"`
	Action      int    `json:"action"`
	Enabled     bool   `json:"enabled"`
	Profiles    int64  `json:"profiles"`
}

func parseFirewallState(out []byte) (firewallState, error) {
	var st firewallState
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &st); err != nil {
		return st, fmt.Errorf("could not read the firewall check: %w: %q", err, strings.TrimSpace(string(out)))
	}
	return st, nil
}

// matches reports whether the rule found is an enabled outbound block rule for
// exactly this program and exactly these networks, on every profile.
func (st firewallState) matches(program string, nets []string) bool {
	if !st.Found || !st.Enabled || st.Direction != 2 || st.Action != 0 || st.Profiles != 0x7FFFFFFF {
		return false
	}
	if !strings.EqualFold(filepath.Clean(st.Program), filepath.Clean(program)) {
		return false
	}
	got, ok := windowsRemoteAddresses(st.Remote)
	if !ok {
		return false
	}
	want := slices.Clone(nets)
	slices.Sort(want)
	return slices.Equal(got, want)
}

// windowsRemoteAddresses turns the firewall's own form of a rule's addresses
// ("100.64.0.0/255.192.0.0,169.254.0.0/255.255.0.0", a bare address for a /32)
// into sorted CIDRs. Anything else (a range, a keyword such as LocalSubnet) is not
// a rule we wrote, so it does not match.
func windowsRemoteAddresses(s string) ([]string, bool) {
	var out []string
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		addr, mask, hasMask := strings.Cut(item, "/")
		ip := net.ParseIP(addr).To4()
		if ip == nil {
			return nil, false
		}
		ones := 32
		if hasMask {
			m := net.ParseIP(mask).To4()
			if m == nil {
				return nil, false
			}
			var bits int
			ones, bits = net.IPMask(m).Size()
			if bits == 0 {
				return nil, false
			}
		}
		out = append(out, (&net.IPNet{IP: ip.Mask(net.CIDRMask(ones, 32)), Mask: net.CIDRMask(ones, 32)}).String())
	}
	slices.Sort(out)
	return out, len(out) > 0
}
