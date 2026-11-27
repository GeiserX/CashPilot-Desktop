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
for c in "$@"; do
  case "$c" in
    *:*) ssh -o ConnectTimeout=5 "${c%%:*}" "grep -F '$tok' '${c#*:}'" >> "$out/$step.arrivals" 2>/dev/null || true ;;
    *)   grep -F "$tok" "$c" >> "$out/$step.arrivals" 2>/dev/null || true ;;
  esac
done
echo "# step=$step token=$tok took=$(( $(date +%s) - start ))s"
while IFS=$'\t' read -r label kind target port result; do
  hit=$(grep -F "\"$tok-$label\"" "$out/$step.arrivals" | head -1 | sed -E 's/.*"bind": "([^"]*)".*"peer": "([^"]*)".*/\1 from \2/' || true)
  printf '%-18s %-5s %-28s %-6s %-22s %s\n' "$label" "$kind" "$target" "$port" "$result" "${hit:+ARRIVED $hit}"
done < "$out/$step.probe.tsv" | tee "$out/$step.table"
n=$(grep -c ARRIVED "$out/$step.table" || true)
echo "# arrivals: $n"
