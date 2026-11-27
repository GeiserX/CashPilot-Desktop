#!/bin/sh
# Lab only: turn a fresh Setup-Assistant guest into the Secure VM layout. Runs
# INSIDE the guest as root, in two parts:
#
#   provision.sh base <agent-path> <agent-sha256>
#       once, on the base image, over the build's SSH session: installs the
#       guest agent as a root LaunchDaemon with --run-rpc only (no
#       --run-vdagent, so no clipboard channel), keeps the guest awake, turns
#       Screen Sharing off, checks Gatekeeper, and turns Remote Login off from the
#       next boot on.
#       After this the base is reached only through `tart exec`.
#   provision.sh autostart <app-path> <short-name>
#       for an earner app that registers no login item of its own: a
#       LaunchAgent in /Library/LaunchAgents opens it (in the background) at
#       every GUI login. The folder and the file belong to root, so the earner
#       user cannot change or delete them. Only `earner` logs in to the guest.
#   provision.sh earner
#       per clone, over `tart exec -i`: creates the standard user `earner` and
#       logs it in automatically. Passwords arrive on stdin, one per line, so
#       they never show in `ps` or in a shell history (`sysadminctl -password -`
#       reads the password from stdin):
#         line 1: the earner password
#         line 2: the same password as /etc/kcpassword bytes, in hex (kcpassword.py)
set -eu
MODE="$1"
[ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 1; }
if [ "$MODE" = base ]; then
AGENT_SRC="$2"; AGENT_SHA="$3"

# 1. Guest agent
got=$(shasum -a 256 "$AGENT_SRC" | awk '{print $1}')
[ "$got" = "$AGENT_SHA" ] || { echo "agent sha mismatch: $got" >&2; exit 1; }
install -d -o root -g wheel -m 755 /usr/local/libexec
install -o root -g wheel -m 755 "$AGENT_SRC" /usr/local/libexec/tart-guest-agent
cat > /Library/LaunchDaemons/org.cashpilot.guest-agent.plist <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>org.cashpilot.guest-agent</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/libexec/tart-guest-agent</string>
    <string>--run-rpc</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardErrorPath</key><string>/var/log/cashpilot-guest-agent.log</string>
</dict>
</plist>
PLIST
chown root:wheel /Library/LaunchDaemons/org.cashpilot.guest-agent.plist
chmod 644 /Library/LaunchDaemons/org.cashpilot.guest-agent.plist
launchctl bootout system/org.cashpilot.guest-agent 2>/dev/null || true
launchctl bootstrap system /Library/LaunchDaemons/org.cashpilot.guest-agent.plist

# Awake, no screen saver at the login window, no Spotlight indexing
pmset -a sleep 0 displaysleep 0 disksleep 0 powernap 0 >/dev/null
systemsetup -setsleep Off >/dev/null 2>&1 || true
defaults write /Library/Preferences/com.apple.screensaver loginWindowIdleTime 0
mdutil -a -i off >/dev/null 2>&1 || true
# DNS: the fence blocks the gateway, and the gateway's DNS would also resolve
# the host's private names. Use public resolvers instead.
networksetup -setdnsservers Ethernet 9.9.9.9 149.112.112.112
# No remote doors, Gatekeeper on
launchctl disable system/com.apple.screensharing 2>/dev/null || true
launchctl bootout system/com.apple.screensharing 2>/dev/null || true
spctl --status | grep -q "assessments enabled" || { echo "Gatekeeper is off" >&2; exit 1; }
# Remote Login off from the next boot on. The running sshd keeps the caller's
# session, so the build can still shut the guest down cleanly.
launchctl disable system/com.openssh.sshd
echo "base provisioned: $(sw_vers -productVersion) ($(sw_vers -buildVersion))"
exit 0
fi

if [ "$MODE" = autostart ]; then
APP="$2"; NAME="$3"; LA=/Library/LaunchAgents
[ -d "$APP" ] || { echo "no app at $APP" >&2; exit 1; }
cat > "$LA/org.cashpilot.earner.$NAME.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>org.cashpilot.earner.$NAME</string>
  <key>ProgramArguments</key>
  <array><string>/usr/bin/open</string><string>-g</string><string>$APP</string></array>
  <key>RunAtLoad</key><true/>
  <key>LimitLoadToSessionType</key><string>Aqua</string>
</dict>
</plist>
PLIST
chown root:wheel "$LA/org.cashpilot.earner.$NAME.plist"; chmod 644 "$LA/org.cashpilot.earner.$NAME.plist"
echo "autostart: $NAME -> $APP"
exit 0
fi

[ "$MODE" = earner ] || { echo "mode: base | autostart | earner" >&2; exit 2; }
read -r EARNER_PW
read -r KCPW_HEX
# The earner account: standard, no admin, logs in by itself
if ! id earner >/dev/null 2>&1; then
  printf '%s\n' "$EARNER_PW" | sysadminctl -addUser earner -fullName "Earner" -password - -home /Users/earner
  createhomedir -c -u earner >/dev/null 2>&1 || true
fi
dseditgroup -o checkmember -m earner admin >/dev/null 2>&1 && { echo "earner is admin" >&2; exit 1; }
(umask 077; printf '%s' "$KCPW_HEX" | xxd -r -p > /etc/kcpassword)
chown root:wheel /etc/kcpassword; chmod 600 /etc/kcpassword
defaults write /Library/Preferences/com.apple.loginwindow autoLoginUser earner

# Skip the per-user Setup Assistant screens for earner
v=$(sw_vers -productVersion); b=$(sw_vers -buildVersion)
sa=/Users/earner/Library/Preferences/com.apple.SetupAssistant.plist
mkdir -p /Users/earner/Library/Preferences
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
chown -R earner:staff /Users/earner/Library

# Per-user: no screen saver for earner
sudo -u earner defaults -currentHost write com.apple.screensaver idleTime 0
echo "earner provisioned: $(sw_vers -productVersion) ($(sw_vers -buildVersion))"
