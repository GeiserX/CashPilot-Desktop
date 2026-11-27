#!/bin/sh
# Lab only: download, check and install one earner app INSIDE the guest. Runs
# as root through the guest agent:
#   tart exec <vm> /bin/sh -c "$(cat install-app.sh)" install-app <url> <sha256|-> <team-id|->
#
#   install-app.sh <url> <sha256|-> <team-id|-> [<file-name>]
#
# It refuses the app unless:
#   - the SHA-256 matches, when one is given;
#   - `codesign` names the expected Team ID, when one is given;
#   - Gatekeeper accepts it (`spctl -a`): a notarized Developer ID app or pkg.
# Then a .app goes to /Applications (owner root, so the earner user cannot
# swap its code), and a .pkg goes through `installer`. Nothing is started: the
# first start happens in the earner's session, so the app registers its own
# login item there. A root-owned app cannot update itself; CashPilot updates
# it from the catalog pin instead.
#
# It prints one line per check, then "RESULT ok" or "RESULT refused: <why>".
set -u
url=$1 want_sha=$2 want_team=$3 name=${4:-$(basename "$url" | sed 's/%20/ /g; s/+/ /g')}
case "$name" in ''|*/*|.*) echo "RESULT refused: file name must be a plain name"; exit 1 ;; esac
# Root's own staging folder: the earner user cannot plant a file or a symlink here.
dl=$(mktemp -d /private/var/tmp/cp-dl.XXXXXX) || { echo "RESULT refused: no staging folder"; exit 1; }
f="$dl/$name"
mnt=""
refuse() { [ -n "$mnt" ] && hdiutil detach "$mnt" >/dev/null 2>&1; rm -rf "$dl"; echo "RESULT refused: $*"; exit 1; }
# Copy under a temporary name first, so a failed copy keeps the old version.
place() {
  dst="/Applications/$(basename "$1")"; tmp="/Applications/.cp-new-$(basename "$1")"
  rm -rf "$tmp"
  ditto "$1" "$tmp" || { rm -rf "$tmp"; refuse "copy failed"; }
  chown -R root:wheel "$tmp" || { rm -rf "$tmp"; refuse "chown failed"; }
  rm -rf "$dst" && mv "$tmp" "$dst" || refuse "move into /Applications failed"
  echo "installed $dst"
}
pkg_install() {
  out=$(installer -pkg "$1" -target / 2>&1) || refuse "pkg install failed: $(echo "$out" | tail -1)"
  echo "$out" | tail -1
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
      check "$app"; place "$app"
    elif [ -n "$pkg" ]; then
      check "$pkg"; pkg_install "$pkg"
    else refuse "no .app or .pkg in the dmg"; fi
    hdiutil detach "$mnt" >/dev/null 2>&1 ;;
  *.pkg) check "$f"; pkg_install "$f" ;;
  *.zip)
    x=$(mktemp -d /tmp/cpzip.XXXX); ditto -x -k "$f" "$x"
    app=$(find "$x" -maxdepth 2 -name '*.app' | head -1); [ -n "$app" ] || refuse "no .app in the zip"
    check "$app"; place "$app" ;;
  *) refuse "unknown file type" ;;
esac
rm -rf "$dl"
echo "RESULT ok"
