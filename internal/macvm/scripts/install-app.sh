#!/bin/sh
# Downloads, checks and installs one earner app in the Secure VM, then opens it
# in the earner's session. CashPilot runs it as root through the guest agent:
#   install-app.sh <url> <sha256|-> <team-id> <file-name> <App.app> <slug>
#
# It refuses the app unless:
#   - the SHA-256 matches, when one is pinned;
#   - `codesign` names the expected Team ID;
#   - Gatekeeper accepts it (`spctl -a`): a notarized Developer ID app or pkg.
# A .app goes to /Applications, owned by root, so the earner user cannot swap
# its code; a .pkg goes through `installer`. A root LaunchAgent opens the app at
# every login of the earner. A root-owned app cannot update itself; CashPilot
# updates it from the catalog pin instead.
#
# It prints one line per check, then "RESULT ok" or "RESULT refused: <why>".
set -u
url=$1 want_sha=$2 want_team=$3 name=$4 want_app=$5 slug=$6
case "$slug" in ''|*[!a-z0-9-]*) echo "RESULT refused: bad slug"; exit 1 ;; esac
case "$want_app" in *.app) ;; *) echo "RESULT refused: bad app name"; exit 1 ;; esac
case "$want_app" in */*|.*|*..*|*'&'*|*'<'*|*'>'*) echo "RESULT refused: bad app name"; exit 1 ;; esac
case "$want_team" in ''|-|*[!A-Z0-9]*) echo "RESULT refused: no team id"; exit 1 ;; esac
case "$url" in https://*) ;; *) echo "RESULT refused: download must be https"; exit 1 ;; esac
case "$name" in ''|*/*|.*) echo "RESULT refused: file name must be a plain name"; exit 1 ;; esac
# Root's own staging folder: the earner user cannot plant a file or a symlink here.
dl=$(mktemp -d /private/var/tmp/cp-dl.XXXXXX) || { echo "RESULT refused: no staging folder"; exit 1; }
f="$dl/$name"
mnt="" x=""
refuse() { [ -n "$mnt" ] && hdiutil detach "$mnt" >/dev/null 2>&1; rm -rf "$dl"; [ -n "$x" ] && rm -rf "$x"; echo "RESULT refused: $*"; exit 1; }
# The bundle must be the one the catalog names: an archive with another app
# would leave an old copy of the named one in place and pass the check below.
named() { [ "$(basename "$1")" = "$want_app" ] || refuse "the download holds $(basename "$1"), not $want_app"; }
# Copy under a temporary name first, so a failed copy keeps the old version.
place() {
  dst="/Applications/$(basename "$1")"; tmp="/Applications/.cp-new-$(basename "$1")"
  rm -rf "$tmp"
  ditto "$1" "$tmp" || { rm -rf "$tmp"; refuse "copy failed"; }
  chown -R root:wheel "$tmp" || { rm -rf "$tmp"; refuse "chown failed"; }
  # The old version moves aside and comes back if the new one cannot take its place.
  old="/Applications/.cp-old-$(basename "$1")"
  rm -rf "$old"
  if [ -e "$dst" ]; then mv "$dst" "$old" || { rm -rf "$tmp"; refuse "could not move the old version aside"; }; fi
  if ! mv "$tmp" "$dst"; then
    [ -e "$old" ] && mv "$old" "$dst"
    rm -rf "$tmp"; refuse "move into /Applications failed"
  fi
  rm -rf "$old"
  echo "installed $dst"
}
# A pkg names no app, so check what it left: the named app, written by this
# install, signed by the expected team and accepted by Gatekeeper.
pkg_install() {
  a="/Applications/$want_app"
  before=$(stat -f %c "$a/Contents/Info.plist" 2>/dev/null || echo none)
  out=$(installer -pkg "$1" -target / 2>&1) || refuse "pkg install failed: $(echo "$out" | tail -1)"
  echo "$out" | tail -1
  [ -d "$a" ] || refuse "the installer did not put $want_app in /Applications"
  [ "$(stat -f %c "$a/Contents/Info.plist" 2>/dev/null || echo none)" != "$before" ] || refuse "the package did not install $want_app"
  check "$a"
}
/usr/bin/curl -fsSL -m 600 -o "$f" "$url" || refuse "download failed"
sha=$(shasum -a 256 "$f" | awk '{print $1}')
echo "file $name size=$(stat -f %z "$f") sha256=$sha"
[ "$want_sha" = - ] || [ "$sha" = "$want_sha" ] || refuse "sha256 mismatch"

check() { # $1 = path of .app or .pkg
  case "$1" in
    *.pkg)
      sig=$(pkgutil --check-signature "$1" 2>&1)
      echo "$sig" | sed -n 's/^ *1\. /signer: /p; s/^ *Status: /status: /p' | head -2
      team=$(echo "$sig" | sed -n 's/.*Developer ID Installer: .*(\([A-Z0-9]*\)).*/\1/p' | head -1)
      ga=$(spctl -a -vv -t install "$1" 2>&1); garc=$? ;;
    *)
      cs=$(codesign -dv --verbose=2 "$1" 2>&1)
      echo "$cs" | grep -E '^(Identifier|TeamIdentifier|Authority=Developer ID Application|Timestamp)' | head -4
      team=$(echo "$cs" | sed -n 's/^TeamIdentifier=//p')
      # lipo needs the developer tools, which a guest does not have; file(1) is in the base system
      echo "arch: $(file -b "$1/Contents/MacOS/$(defaults read "$1/Contents/Info" CFBundleExecutable 2>/dev/null)" 2>/dev/null | grep -oE 'arm64|x86_64' | sort -u | tr '\n' ' ')"
      echo "version: $(defaults read "$1/Contents/Info" CFBundleShortVersionString 2>/dev/null)"
      ga=$(spctl -a -vv -t exec "$1" 2>&1); garc=$? ;;
  esac
  echo "gatekeeper: $(echo "$ga" | tr '\n' ' ')"
  [ "$want_team" = - ] || [ "$team" = "$want_team" ] || refuse "team id '$team' is not '$want_team'"
  # spctl's first line is exactly "<path>: accepted". Matching the whole line
  # keeps a file name that contains ": accepted" from passing.
  [ "$garc" = 0 ] && [ "$(echo "$ga" | head -1)" = "$1: accepted" ] || refuse "Gatekeeper rejects it"
}

case "$f" in
  *.dmg)
    mnt=$(mktemp -d /tmp/cpdmg.XXXX)
    hdiutil attach -nobrowse -readonly -noautoopen -mountpoint "$mnt" "$f" >/dev/null || refuse "dmg attach failed"
    app=$(find "$mnt" -maxdepth 1 -name '*.app' | head -1)
    pkg=$(find "$mnt" -maxdepth 1 -name '*.pkg' | head -1)
    if [ -n "$app" ]; then
      named "$app"; check "$app"; place "$app"
    elif [ -n "$pkg" ]; then
      check "$pkg"; pkg_install "$pkg"
    else refuse "no .app or .pkg in the dmg"; fi
    hdiutil detach "$mnt" >/dev/null 2>&1 ;;
  *.pkg) check "$f"; pkg_install "$f" ;;
  *.zip)
    x=$(mktemp -d /tmp/cpzip.XXXX); ditto -x -k "$f" "$x"
    app=$(find "$x" -maxdepth 2 -name '*.app' | head -1); [ -n "$app" ] || refuse "no .app in the zip"
    named "$app"; check "$app"; place "$app" ;;
  *) refuse "unknown file type" ;;
esac
rm -rf "$dl"; [ -n "$x" ] && rm -rf "$x"
[ -d "/Applications/$want_app" ] || { echo "RESULT refused: the installer did not put $want_app in /Applications"; exit 1; }
# UpRock keeps its sign-in in the keychain, unless its credentials file exists.
# Under boot load its keychain probe can time out: it then makes that file
# empty, uses it from then on, and the sign-in is lost. A file made before its
# first start makes it use the file every time. An UpRock that ran before keeps
# the store it has.
if [ "$slug" = uprock ] && id earner >/dev/null 2>&1; then
  ud="/Users/earner/Library/Application Support/UpRock"
  if [ ! -e "$ud" ]; then
    sudo -u earner /bin/sh -c 'umask 077; mkdir -p "$1" && printf "{}\n" > "$1/credentials.json"' sh "$ud" \
      && echo "made UpRock's credentials file"
  fi
fi
la="/Library/LaunchAgents/org.cashpilot.earner.$slug.plist"
cat > "$la" <<PLIST || { echo "RESULT refused: could not write $la"; exit 1; }
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>org.cashpilot.earner.$slug</string>
  <key>ProgramArguments</key>
  <array><string>/usr/bin/open</string><string>-g</string><string>/Applications/$want_app</string></array>
  <key>RunAtLoad</key><true/>
  <key>LimitLoadToSessionType</key><string>Aqua</string>
</dict>
</plist>
PLIST
chown root:wheel "$la" && chmod 644 "$la" || { echo "RESULT refused: could not set up $la"; exit 1; }
# Open it now when the earner is logged in; otherwise its next login does.
if uid=$(id -u earner 2>/dev/null) && [ "$(stat -f %u /dev/console)" = "$uid" ]; then
  launchctl asuser "$uid" /usr/bin/sudo -u earner /usr/bin/open -g "/Applications/$want_app" || true
  echo "opened in the earner's session"
fi
echo "RESULT ok"
