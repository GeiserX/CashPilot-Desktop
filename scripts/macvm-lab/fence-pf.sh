#!/bin/sh
# Lab only: the pf fence for one Secure VM. Runs on the HOST as root.
#
#   fence-pf.sh up   <bridge-if> <gateway-ip> <deny-file> <guest-subnet> [upload-kbit]
#   fence-pf.sh cut  <bridge-if> <guest-subnet>  # block everything, kill the guest's live states
#   fence-pf.sh down                    # remove our anchor and our pf reference
#   fence-pf.sh show                    # rules with counters
#
# <deny-file> holds one IPv4 address or CIDR per line: the user's public IPs,
# the other CashPilot boxes' public IPs, the host's own addresses. The fixed
# private, special and multicast ranges are added here, so the file only holds
# what changes per host.
#
# The rules go into the anchor com.apple/cashpilot. The stock /etc/pf.conf
# already evaluates every child of com.apple (anchor "com.apple/*", and the same
# for dummynet-anchor), so nothing in /etc/pf.conf changes. pf is enabled with
# `pfctl -E`, which takes a reference: pf stays on while any holder has one,
# and `down` gives ours back with `pfctl -X`.
set -eu
ANCHOR=com.apple/cashpilot
STATE=${CASHPILOT_FENCE_STATE:-/var/run/cashpilot-fence}
mkdir -p "$STATE"

fixed_deny="0.0.0.0/8 10.0.0.0/8 100.64.0.0/10 127.0.0.0/8 169.254.0.0/16 172.16.0.0/12
192.0.0.0/24 192.0.2.0/24 192.168.0.0/16 198.18.0.0/15 198.51.100.0/24 203.0.113.0/24
224.0.0.0/4 240.0.0.0/4 255.255.255.255/32"

enable_pf() {
  [ -s "$STATE/pf-token" ] && return 0
  pfctl -E 2>&1 | sed -n 's/^Token : //p' > "$STATE/pf-token"
}

case "${1:-}" in
up)
  ifc="$2"; gw="$3"; denyfile="$4"; subnet="$5"; kbit="${6:-0}"
  extra=$(grep -v '^[[:space:]]*#' "$denyfile" | tr '\n' ' ')
  {
    echo "table <cp_deny> const { $(echo $fixed_deny) $extra }"
    # IPv6 is off for the guest in both directions.
    echo "block drop in  quick log on $ifc inet6 all label \"cp-v6-in\""
    echo "block drop out quick log on $ifc inet6 all label \"cp-v6-out\""
    # The host's own multicast and broadcast (Bonjour, NetBIOS) never reach the guest.
    echo "block drop out quick on $ifc inet from any to { 224.0.0.0/4, 255.255.255.255 } label \"cp-mcast-out\""
    # DHCP to the host's bootpd only. No DNS to the host: the guest uses public resolvers.
    echo "pass in quick on $ifc inet proto udp from any port 68 to { $gw, 255.255.255.255 } port 67 keep state label \"cp-dhcp\""
    echo "block drop in quick log on $ifc inet from any to $gw label \"cp-host-gw\""
    echo "block drop in quick log on $ifc inet from any to <cp_deny> label \"cp-deny\""
    echo "pass in quick on $ifc inet from any to any keep state label \"cp-internet\""
    if [ "$kbit" -gt 0 ]; then
      echo "dummynet in quick on $ifc inet from any to ! <cp_deny> pipe 7301"
    fi
  } > "$STATE/rules.pf"
  if [ "$kbit" -gt 0 ]; then dnctl pipe 7301 config bw "${kbit}Kbit/s"; else dnctl pipe 7301 delete 2>/dev/null || true; fi
  pfctl -a "$ANCHOR" -f "$STATE/rules.pf"
  enable_pf
  # A state made before these rules keeps passing its flow (trap M1-T1): kill
  # every state from and to the guest subnet, so each flow meets the new rules.
  pfctl -k "$subnet" >/dev/null 2>&1 || true
  pfctl -k 0.0.0.0/0 -k "$subnet" >/dev/null 2>&1 || true
  pfctl -a "$ANCHOR" -sr
  ;;
cut)
  ifc="$2"; subnet="$3"
  printf 'block drop quick on %s all label "cp-cut"\n' "$ifc" > "$STATE/rules.pf"
  pfctl -a "$ANCHOR" -f "$STATE/rules.pf"
  enable_pf
  pfctl -k "$subnet" >/dev/null 2>&1 || true   # drop the guest's live states so open flows die too
  pfctl -k 0.0.0.0/0 -k "$subnet" >/dev/null 2>&1 || true
  pfctl -a "$ANCHOR" -sr
  ;;
down)
  pfctl -a "$ANCHOR" -F all 2>/dev/null || true
  dnctl pipe 7301 delete 2>/dev/null || true
  if [ -s "$STATE/pf-token" ]; then pfctl -X "$(cat "$STATE/pf-token")" 2>/dev/null || true; rm -f "$STATE/pf-token"; fi
  ;;
show)
  pfctl -s info | head -1
  pfctl -a "$ANCHOR" -vsr
  ;;
*)
  sed -n '2,20p' "$0"; exit 2 ;;
esac
