#!/bin/sh
# Lab only: the Secure VM guard prototype. Runs on the HOST as root, from a
# LaunchDaemon (guard.plist): at load (boot) and every 60 seconds.
#
#   guard.sh <state-dir>
#
# <state-dir> (root-owned, mode 700) holds what setup proved:
#   desired.env    VM_MAC=<guest MAC>  ANCHOR=com.apple/cashpilot
#                  SOFTNET_BLOCK=<the --block list softnet must run with>
#   desired.rules  the exact output of `pfctl -a $ANCHOR -sr` after the proof
#   desired.table  the sorted output of `pfctl -a $ANCHOR -t cp_deny -T show`
#
# The guard never reads the guest. It reads the host only:
#   1. which bridge holds the guest and which vmenet member is its port (the
#      bridge's address cache maps VM_MAC to that member);
#   2. pf on, the main ruleset still evaluating com.apple/*, and our anchor's
#      rules and table equal to desired.*.
# The cut is `ifconfig <bridge> deletem <vmenet>`: the guest's port leaves the
# bridge, so no frame moves in either direction, whatever pf does. Reconnect
# (`addm`) happens only when every check matches exactly.
#
# Boot and wake: the first pass after a boot, or after a gap longer than 90 s
# (a sleep), cuts first and then checks. A normal pass only ever cuts.
set -u
DIR="$1"
. "$DIR/desired.env"
LOG="$DIR/guard.log"
now=$(date +%s)
last=$(cat "$DIR/last-pass" 2>/dev/null || echo 0)
boot=$(sysctl -n kern.boottime | sed 's/.*sec = \([0-9]*\).*/\1/')
echo "$now" > "$DIR/last-pass"
log() { echo "$(date '+%Y-%m-%dT%H:%M:%S%z') $*" >> "$LOG"; }

# Which bridge and member carry the guest? The bridge's address cache names the
# member port each MAC was learned on.
mac=$(echo "$VM_MAC" | tr 'A-F' 'a-f')
br=""; port=""
for b in $(ifconfig -l | tr ' ' '\n' | grep '^bridge1[0-9][0-9]$'); do
  p=$(ifconfig "$b" | awk -v m="$mac" '$1 == m && $3 ~ /^vmenet/ {print $3; exit}')
  if [ -n "$p" ]; then br="$b"; port="$p"; break; fi
done
[ -f "$DIR/port" ] && saved=$(cat "$DIR/port") || saved=""
[ -n "$port" ] && echo "$br $port" > "$DIR/port"
# A cut port has no traffic, so its MAC leaves the cache: fall back to the last
# port we saw, if that interface still exists.
if [ -z "$port" ] && [ -n "$saved" ] && ifconfig "${saved#* }" >/dev/null 2>&1; then br=${saved% *}; port=${saved#* }; fi
if [ -z "$port" ]; then log "no guest port found (VM off?)"; exit 0; fi
member() { ifconfig "$br" 2>/dev/null | grep -q "member: $port "; }

mode=pass
if [ "$last" -lt "$boot" ] || [ $((now - last)) -gt 90 ]; then mode=boot; fi

drift=""
pfctl -s info 2>/dev/null | grep -q 'Status: Enabled' || drift="$drift pf-off"
pfctl -sr 2>/dev/null | grep -q '^anchor "com.apple/\*" all' || drift="$drift main-ruleset"
pfctl -a "$ANCHOR" -sr 2>/dev/null | sed "s/ on bridge1[0-9][0-9] / on BRIDGE /" | cmp -s - "$DIR/desired.rules" || drift="$drift rules"
pfctl -a "$ANCHOR" -t cp_deny -T show 2>/dev/null | tr -d ' ' | sort | cmp -s - "$DIR/desired.table" || drift="$drift table"
# Softnet (the first layer) must run with the proven block list. pgrep -x
# matches the process name only, so a look-alike name in someone's args
# does not count.
if [ -n "${SOFTNET_BLOCK:-}" ]; then
  sn=$(pgrep -x softnet | head -1)
  [ -n "$sn" ] && ps -o args= -p "$sn" | grep -q -- "--block $SOFTNET_BLOCK\( \|\$\)" || drift="$drift softnet"
fi
# The rules must name the bridge the guest is on right now.
pfctl -a "$ANCHOR" -sr 2>/dev/null | grep -q " on $br " || drift="$drift wrong-bridge"
# ... and the gateway that bridge has now (softnet picks a new /29 per start).
gw=$(ifconfig "$br" 2>/dev/null | awk '/inet / {print $2; exit}')
pfctl -a "$ANCHOR" -sr 2>/dev/null | grep -q "label \"cp-host-gw\"" &&
  pfctl -a "$ANCHOR" -sr 2>/dev/null | grep "cp-host-gw" | grep -q " to $gw " || drift="$drift wrong-gateway"

if [ "$mode" = boot ] && member; then
  ifconfig "$br" deletem "$port" && log "boot/wake: $port cut from $br before the check"
fi
if [ -n "$drift" ]; then
  if member; then ifconfig "$br" deletem "$port" && log "MISMATCH:$drift; $port cut from $br"; else log "MISMATCH:$drift; staying cut"; fi
  exit 0
fi
if ! member; then
  ifconfig "$br" addm "$port" && log "match; $port connected to $br"
else
  log "match; connected"
fi
