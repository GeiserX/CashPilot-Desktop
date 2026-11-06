#!/usr/bin/env bash
# Point the winget, Homebrew and AUR packages at a published release.
#
#   packaging/render.sh v0.32.0
#
# Downloads the three release assets and the tagged icon, checks the file names the
# packages rely on are inside the archives, then writes the version and the sha256 of
# each asset into packaging/. Run it before copying a package to its store.
set -euo pipefail

tag="${1:-}"
if [[ ! "$tag" =~ ^v([0-9]+\.[0-9]+\.[0-9]+)$ ]]; then
  echo "usage: $0 vX.Y.Z" >&2
  exit 2
fi
ver="${BASH_REMATCH[1]}"

here="$(cd "$(dirname "$0")" && pwd)"
repo="GeiserX/CashPilot-Desktop"
base="https://github.com/$repo/releases/download/$tag"
aur="$here/aur/cashpilot-desktop-bin"
cask="$here/homebrew/cashpilot-desktop.rb"
winget_root="$here/winget/GeiserX.CashPilotDesktop"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1
}

fetch() {
  curl -fsSL --retry 3 -o "$tmp/$2" "$1"
}

fetch "$base/CashPilot-Desktop-darwin-arm64.zip" mac.zip
fetch "$base/CashPilot-Desktop-linux-amd64.tar.gz" linux.tar.gz
fetch "$base/CashPilot-Desktop-windows-amd64.exe" windows.exe
fetch "https://raw.githubusercontent.com/$repo/$tag/build/appicon.png" icon.png

# The cask installs "cashpilot-desktop.app" and the PKGBUILD installs the file
# "CashPilot Desktop"; refuse to render if a release renamed either.
# (Listings go through a variable: `grep -q` on a pipe can SIGPIPE the lister,
# which pipefail then reports as a missing file.)
mac_list="$(unzip -Z1 "$tmp/mac.zip")"
grep -qx 'cashpilot-desktop.app/' <<<"$mac_list" \
  || { echo "darwin zip has no cashpilot-desktop.app/ at its root" >&2; exit 1; }
linux_list="$(tar -tzf "$tmp/linux.tar.gz")"
grep -qx 'CashPilot Desktop' <<<"$linux_list" \
  || { echo "linux tarball has no 'CashPilot Desktop' file" >&2; exit 1; }

mac_sha="$(sha256 "$tmp/mac.zip")"
linux_sha="$(sha256 "$tmp/linux.tar.gz")"
win_sha="$(sha256 "$tmp/windows.exe" | tr 'a-f' 'A-F')"
icon_sha="$(sha256 "$tmp/icon.png")"
desktop_sha="$(sha256 "$aur/cashpilot-desktop.desktop")"

# Homebrew cask.
V="$ver" S="$mac_sha" perl -pi -e '
  s/^  version ".*"/  version "$ENV{V}"/;
  s/^  sha256 ".*"/  sha256 "$ENV{S}"/;
' "$cask"

# AUR PKGBUILD: new version starts at pkgrel 1.
V="$ver" A="$linux_sha" B="$icon_sha" C="$desktop_sha" perl -0pi -e '
  s/^pkgver=.*$/pkgver=$ENV{V}/m;
  s/^pkgrel=.*$/pkgrel=1/m;
  s/^sha256sums=\(.*?\)/sha256sums=(\x27$ENV{A}\x27\n            \x27$ENV{B}\x27\n            \x27$ENV{C}\x27)/ms;
' "$aur/PKGBUILD"

# AUR .SRCINFO, in the layout `makepkg --printsrcinfo` writes, read from the PKGBUILD
# itself so the two cannot disagree.
(
  cd "$aur"
  # shellcheck disable=SC1091
  source ./PKGBUILD
  {
    printf 'pkgbase = %s\n' "$pkgname"
    printf '\tpkgdesc = %s\n' "$pkgdesc"
    printf '\tpkgver = %s\n' "$pkgver"
    printf '\tpkgrel = %s\n' "$pkgrel"
    printf '\turl = %s\n' "$url"
    for v in "${arch[@]}"; do printf '\tarch = %s\n' "$v"; done
    for v in "${license[@]}"; do printf '\tlicense = %s\n' "$v"; done
    for v in "${depends[@]}"; do printf '\tdepends = %s\n' "$v"; done
    for v in "${optdepends[@]}"; do printf '\toptdepends = %s\n' "$v"; done
    for v in "${provides[@]}"; do printf '\tprovides = %s\n' "$v"; done
    for v in "${conflicts[@]}"; do printf '\tconflicts = %s\n' "$v"; done
    for v in "${options[@]}"; do printf '\toptions = %s\n' "$v"; done
    for v in "${source[@]}"; do printf '\tsource = %s\n' "$v"; done
    for v in "${sha256sums[@]}"; do printf '\tsha256sums = %s\n' "$v"; done
    printf '\npkgname = %s\n' "$pkgname"
  } > .SRCINFO
)

# winget: one directory per version, as in winget-pkgs.
old_dir="$(find "$winget_root" -mindepth 1 -maxdepth 1 -type d | head -1)"
new_dir="$winget_root/$ver"
if [ "$old_dir" != "$new_dir" ]; then
  mv "$old_dir" "$new_dir"
fi
V="$ver" T="$tag" S="$win_sha" perl -pi -e '
  s/^PackageVersion: .*/PackageVersion: $ENV{V}/;
  s{^(  InstallerUrl: https://github.com/GeiserX/CashPilot-Desktop/releases/download/)v[^/]+/}{$1$ENV{T}/};
  s/^(  InstallerSha256: ).*/$1$ENV{S}/;
  s{^(ReleaseNotesUrl: https://github.com/GeiserX/CashPilot-Desktop/releases/tag/).*}{$1$ENV{T}};
' "$new_dir"/*.yaml

echo "Rendered $tag:"
echo "  darwin-arm64.zip     $mac_sha"
echo "  linux-amd64.tar.gz   $linux_sha"
echo "  windows-amd64.exe    $win_sha"
echo "  appicon.png          $icon_sha"
