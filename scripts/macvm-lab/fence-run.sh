#!/bin/bash
# Lab only: one fence step. Runs on the HOST as the user who owns the VM.
#
#   fence-run.sh <vm> <targets-file> <gateway-ip> <step-name> <out-dir> [canary-log ...]
#
# Each canary-log is a local path or "<ssh-host>:<path>" (a canary on another
# box). The step:
#   1. makes a random token,
#   2. runs probe.sh in the guest over `tart exec` (no guest network needed),
#   3. collects every canary log and keeps only the lines with this token,
#   4. prints the probe table with an "ARRIVED" column.
# An ARRIVED target is red. Green needs a timeout and no arrival.
set -euo pipefail
vm=$1 targets=$2 gw=$3 step=$4 out=$5; shift 5
here=$(cd "$(dirname "$0")" && pwd)
mkdir -p "$out"
tok="cp$(openssl rand -hex 6)"
sed "s/ GW / $gw /" "$targets" > "$out/$step.targets"
start=$(date +%s)
tart exec -i "$vm" /bin/sh -c "$(cat "$here/probe.sh")" probe "$tok" < "$out/$step.targets" > "$out/$step.probe.tsv"
sleep 2
: > "$out/$step.arrivals"
# grep exits 1 for "no match", which is the green case. Anything else (a log
# that is missing or unreadable, ssh failing) means we did not look, so the
# step is inconclusive, never "0 arrivals".
lost=""
for c in "$@"; do
  rc=0
  case "$c" in
    *:*) ssh -o ConnectTimeout=5 "${c%%:*}" "if test -r '${c#*:}'; then grep -F '$tok' '${c#*:}'; else exit 2; fi" >> "$out/$step.arrivals" 2>/dev/null || rc=$? ;;
    *)   if [ -r "$c" ]; then grep -F "$tok" "$c" >> "$out/$step.arrivals" 2>/dev/null || rc=$?; else rc=2; fi ;;
  esac
  [ "$rc" -le 1 ] || lost="$lost $c"
done
echo "# step=$step token=$tok took=$(( $(date +%s) - start ))s"
while IFS=$'\t' read -r label kind target port result; do
  hit=$(grep -F "\"$tok-$label\"" "$out/$step.arrivals" | head -1 | sed -E 's/.*"bind": "([^"]*)".*"peer": "([^"]*)".*/\1 from \2/' || true)
  printf '%-18s %-5s %-28s %-6s %-22s %s\n' "$label" "$kind" "$target" "$port" "$result" "${hit:+ARRIVED $hit}"
done < "$out/$step.probe.tsv" | tee "$out/$step.table"
n=$(grep -c ARRIVED "$out/$step.table" || true)
echo "# arrivals: $n"
if [ -n "$lost" ]; then echo "# INCONCLUSIVE: could not read canary log(s):$lost"; exit 3; fi
