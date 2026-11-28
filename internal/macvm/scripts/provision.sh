#!/bin/sh
# Turns a fresh Secure VM guest into the earner's. CashPilot runs it once, as
# root, through the guest agent (`tart exec -i`), after the fence proof.
#
# The guest booted with no account: the admin step wrote the guest agent and
# .AppleSetupDone into its disk. This script adds the standard user `earner`,
# which logs in by itself, and the system settings every earner needs.
#
# stdin, one per line, so they never show in `ps`:
#   line 1: the earner password (random, never kept)
#   line 2: the same password as /etc/kcpassword bytes, in hex
#
# It prints "PROVISIONED new" on the first run, "PROVISIONED already" after.
set -eu
[ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 1; }

# Awake, no screen saver, no Spotlight indexing.
pmset -a sleep 0 displaysleep 0 disksleep 0 powernap 0 >/dev/null
# powerd saves these to disk up to a minute later, and the restart after this
# script can come first. Then the display sleeps after 10 minutes and locks
# the earner's screen behind a password nobody knows. Wait until they are saved.
pm=/Library/Preferences/com.apple.PowerManagement.plist
saved() {
  for k in "System Sleep Timer" "Display Sleep Timer" "Disk Sleep Timer"; do
    [ "$(plutil -extract "AC Power.$k" raw "$pm" 2>/dev/null)" = 0 ] || return 1
  done
}
n=0
until saved; do
  n=$((n + 1))
  [ "$n" -le 120 ] || { echo "the guest did not save its sleep settings" >&2; exit 1; }
  sleep 1
done
systemsetup -setsleep Off >/dev/null 2>&1 || true
defaults write /Library/Preferences/com.apple.screensaver loginWindowIdleTime 0
mdutil -a -i off >/dev/null 2>&1 || true
# No remote doors: no Screen Sharing, no Remote Login. CashPilot reaches the
# guest only through the agent, and shows its screen through Tart's own VNC.
launchctl disable system/com.apple.screensharing 2>/dev/null || true
launchctl bootout system/com.apple.screensharing 2>/dev/null || true
launchctl disable system/com.openssh.sshd 2>/dev/null || true
launchctl bootout system/com.openssh.sshd 2>/dev/null || true
# Gatekeeper must be on: install-app.sh trusts its verdict.
spctl --status | grep -q "assessments enabled" || { echo "Gatekeeper is off in the guest" >&2; exit 1; }
# Public DNS on every network service. The fence blocks the gateway, and the
# gateway's DNS would also answer the host's private names.
networksetup -listallnetworkservices | tail -n +2 | sed 's/^\*//' | while IFS= read -r svc; do
  networksetup -setdnsservers "$svc" 9.9.9.9 149.112.112.112
done
# No automatic macOS upgrade: CashPilot proved this guest, and an upgrade
# restarts it. Security data (XProtect) still updates.
su=/Library/Preferences/com.apple.SoftwareUpdate
defaults write $su AutomaticDownload -bool false
defaults write $su AutomaticallyInstallMacOSUpdates -bool false
defaults write $su ConfigDataInstall -bool true
defaults write $su CriticalUpdateInstall -bool true
# UTC: the guest does not need the host's time zone.
systemsetup -settimezone UTC >/dev/null 2>&1 || ln -sf /usr/share/zoneinfo/UTC /etc/localtime

read -r EARNER_PW
read -r KCPW_HEX
# The marker is written last, so a run that stopped half way runs again.
done_marker=/var/db/.CashPilotProvisioned
if [ -f "$done_marker" ]; then
  echo "PROVISIONED already"
  exit 0
fi
if ! id earner >/dev/null 2>&1; then
  # The login password goes first: a run that stops before the account exists
  # writes both again with a new password, so an account never lacks its
  # login password.
  (umask 077; printf '%s' "$KCPW_HEX" | xxd -r -p > /etc/kcpassword)
  chown root:wheel /etc/kcpassword; chmod 600 /etc/kcpassword
  printf '%s\n' "$EARNER_PW" | sysadminctl -addUser earner -fullName "Earner" -password - -home /Users/earner >/dev/null 2>&1 || true
  id earner >/dev/null 2>&1 || { echo "could not create the earner account" >&2; exit 1; }
fi
# An account from a stopped run keeps its first password; a second kcpassword
# would be for a password the account does not have.
[ -s /etc/kcpassword ] || { echo "the earner account exists without its login password; remove the Secure VM and install it again" >&2; exit 1; }
createhomedir -c -u earner >/dev/null 2>&1 || true
dseditgroup -o checkmember -m earner admin >/dev/null 2>&1 && { echo "earner is an admin" >&2; exit 1; }
defaults write /Library/Preferences/com.apple.loginwindow autoLoginUser earner
# A screen that was locked when the guest stopped locks again at the next
# automatic login, and nobody knows the earner's password to unlock it.
defaults write /Library/Preferences/com.apple.loginwindow autoLoginUserScreenLocked -bool false

# Skip the per-user Setup Assistant screens.
v=$(sw_vers -productVersion); b=$(sw_vers -buildVersion)
sa=/Users/earner/Library/Preferences/com.apple.SetupAssistant.plist
mkdir -p /Users/earner/Library/Preferences/ByHost
for k in DidSeeCloudSetup DidSeePrivacy DidSeeSiriSetup DidSeeScreenTime DidSeeAppearanceSetup \
         DidSeeTouchIDSetup DidSeeAccessibility DidSeeActivationLock DidSeeIntelligence \
         DidSeeApplePaySetup DidSeeSyncSetup DidSeeSyncSetup2 DidSeeTrueTonePrivacy DidSeeTermsOfAddress; do
  defaults write "$sa" "$k" -bool true
done
defaults write "$sa" LastSeenCloudProductVersion "$v"
defaults write "$sa" LastSeenBuddyBuildVersion "$b"
defaults write "$sa" LastPreLoginTasksPerformedVersion "$v"
defaults write "$sa" LastPreLoginTasksPerformedBuild "$b"
defaults write "$sa" SkipFirstLoginOptimization -bool true
# No screen saver for earner. A user with no session refuses
# `defaults -currentHost`, so write the ByHost file by path.
hw=$(ioreg -rd1 -c IOPlatformExpertDevice | awk -F'"' '/IOPlatformUUID/{print $4}')
[ -n "$hw" ] || { echo "could not read the guest's hardware UUID" >&2; exit 1; }
ss="/Users/earner/Library/Preferences/ByHost/com.apple.screensaver.$hw.plist"
defaults write "$ss" idleTime -int 0
# Only what this script wrote: after the first login, macOS keeps folders in
# the Library that even root may not chown.
chown earner:staff /Users/earner/Library /Users/earner/Library/Preferences \
  /Users/earner/Library/Preferences/ByHost "$sa" "$ss"
touch "$done_marker"
# CashPilot restarts the guest next, and `tart stop` may force it off: flush
# first, or the last writes are lost.
sync
echo "PROVISIONED new $(sw_vers -productVersion) ($(sw_vers -buildVersion))"
