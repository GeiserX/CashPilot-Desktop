#!/bin/sh
# Takes one earner out of the guest, as root through the guest agent:
#   remove-app.sh <App.app> <process> <slug>
# Its data (the sign-in) stays until the VM is removed.
set -u
app=$1 proc=$2 slug=$3
case "$slug" in ''|*[!a-z0-9-]*) echo "bad slug" >&2; exit 1 ;; esac
case "$app" in ''|*/*|.*|*..*) echo "bad app name" >&2; exit 1 ;; esac
la="/Library/LaunchAgents/org.cashpilot.earner.$slug.plist"
if uid=$(id -u earner 2>/dev/null); then
  launchctl bootout "gui/$uid/org.cashpilot.earner.$slug" 2>/dev/null || true
  /usr/bin/pkill -x -u "$uid" "$proc" 2>/dev/null || true
  sleep 2
  /usr/bin/pkill -9 -x -u "$uid" "$proc" 2>/dev/null || true
fi
rm -f "$la" || { echo "could not remove $la" >&2; exit 1; }
rm -rf "/Applications/$app" || { echo "could not remove /Applications/$app" >&2; exit 1; }
[ -e "$la" ] || [ -e "/Applications/$app" ] && { echo "$slug is still there" >&2; exit 1; }
echo "removed $slug"
