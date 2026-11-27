#!/bin/bash
# Lab only: start one Secure VM with the full layout, in the order the product
# needs. Runs on the HOST as the VM's owner, with sudo for the root steps.
#
#   start.sh <vm> <state-dir> <deny-file> <softnet-dir> [--no-guard]
#
# TART_DISPLAY=--vnc-experimental swaps --no-graphics for the VZ VNC server
# (127.0.0.1, random password in the run log), so a lab can drive the screen.
#
# Run as root (from a LaunchDaemon at boot) with OWNER=<user> set, it starts
# tart as that user and does the root steps directly.
#
#   <deny-file>    the user's public IPs and the other boxes' public IPs, one per
#                  line (they go to softnet's --block and to the pf table)
#   <softnet-dir>  a root-owned directory with the SUID-root softnet binary
#   <state-dir>    root-owned, mode 700: desired.*, guard.log (RUNLOG= sets the tart log)
#
# Order:
#   1. tart run with softnet (--block @host and the deny list), no audio, no
#      clipboard, no shared directory. Softnet filters from the first frame.
#   2. as soon as the guest's vmenet port shows on a bridge, cut it (deletem),
#      before the guest has booted.
#   3. load the pf anchor for that bridge and gateway, record desired.*.
#   4. run the guard once: it connects the port only on an exact match.
set -euo pipefail
vm=$1 dir=$2 deny=$3 sn=$4; guard=${5:-}
here=$(cd "$(dirname "$0")" && pwd)
block="@host$(grep -v '^[[:space:]]*#' "$deny" | sed 's#^\([0-9.]*\)$#\1/32#' | tr '\n' ',' | sed 's/,$//; s/^/,/')"
if [ "$(id -u)" = 0 ]; then
  owner=${OWNER:?set OWNER when running as root}
  ohome=$(dscl . -read "/Users/$owner" NFSHomeDirectory | awk '{print $2}')
  # launchctl asuser: a system daemon sits outside the owner's per-user launchd
  # domain, where Virtualization.framework's XPC services live. Without it tart
  # starts, prints nothing and never brings the VM up (seen at host boot).
  ouid=$(id -u "$owner")
  as_owner() { launchctl asuser "$ouid" sudo -u "$owner" -H env PATH="$sn:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin" "$@"; }
  sudo() { "$@"; }
else
  ohome=$HOME
  as_owner() { env PATH="$sn:$PATH" "$@"; }
fi
cfg="$ohome/.tart/vms/$vm/config.json"
# At boot the VM may live on a disk that mounts after launchd starts us.
for _ in $(seq 1 300); do [ -f "$cfg" ] && break; sleep 1; done
mac=$(python3 -I -c 'import json,sys; print(json.load(open(sys.argv[1]))["macAddress"])' "$cfg")
echo "vm=$vm mac=$mac block=$block"

members() { for b in $(ifconfig -l | tr ' ' '\n' | grep '^bridge1[0-9][0-9]$'); do ifconfig "$b" | awk -v b="$b" '/member: vmenet/ {print b, $2}'; done; }
before=$(members)
# Until the fence is loaded, a failure stops the guest: launchd lets tart outlive
# this script, so an early exit must not leave it running unfenced.
if as_owner tart list | awk -v v="$vm" '$1 == "local" && $2 == v && $NF == "running" {f=1} END {exit !f}'; then
  echo "$vm is already running; leaving it to the guard"; exit 0
fi
handed_off=0
cleanup() { [ "$handed_off" = 1 ] || { echo "start failed; stopping $vm"; as_owner tart stop "$vm" >/dev/null 2>&1 || true; }; }
trap cleanup EXIT
as_owner nohup tart run "$vm" ${TART_DISPLAY:---no-graphics} --no-audio --no-clipboard \
  --net-softnet-block "$block" > "${RUNLOG:-/tmp/run-$vm.log}" 2>&1 &
# Our port is the vmenet member that was not there before the start. It shows
# before the guest has booted, so the cut lands before the guest sends a frame.
br=""; port=""
for _ in $(seq 1 600); do
  new=$(members | while read -r l; do case "$before" in (*"$l"*) ;; (*) echo "$l" ;; esac; done | head -1)
  [ -n "$new" ] && { br=${new% *}; port=${new#* }; break; }
  sleep 0.1
done
[ -n "$port" ] || { echo "guest port never showed"; exit 1; }
sudo ifconfig "$br" deletem "$port"
echo "cut $port from $br at $(date +%T)"
gw=$(ifconfig "$br" | awk '/inet / {print $2; exit}')
mask=$(ifconfig "$br" | awk '/inet / {print $4; exit}')
bits=$(python3 -I -c 'import sys; print(bin(int(sys.argv[1],16)).count("1"))' "$mask")
subnet=$(python3 -I -c 'import ipaddress,sys; print(ipaddress.ip_network(sys.argv[1]+"/"+sys.argv[2], strict=False))' "$gw" "$bits")
sudo "$here/fence-pf.sh" up "$br" "$gw" "$deny" "$subnet" >/dev/null 2>&1
sudo sh -c "pfctl -a com.apple/cashpilot -sr 2>/dev/null | sed 's/ on bridge1[0-9][0-9] / on BRIDGE /' > '$dir/desired.rules'
  pfctl -a com.apple/cashpilot -t cp_deny -T show 2>/dev/null | tr -d ' ' | sort > '$dir/desired.table'
  printf 'VM_MAC=%s\nANCHOR=com.apple/cashpilot\nSOFTNET_BLOCK=%s\n' '$mac' '$block' > '$dir/desired.env'
  echo '$br $port' > '$dir/port'; echo 0 > '$dir/last-pass'"
echo "fence loaded on $br gw=$gw subnet=$subnet"
handed_off=1
[ "$guard" = --no-guard ] && exit 0
sudo /bin/sh "$here/guard.sh" "$dir"
sudo tail -2 "$dir/guard.log"
