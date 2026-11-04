# App stores and package managers

Today CashPilot Desktop is installed by downloading it from the latest release:
https://github.com/GeiserX/CashPilot-Desktop/releases/latest

The `packaging/` directory holds ready packages for three package managers. None of them is published yet.

| Channel | Platform | Package | Install command once published |
|---|---|---|---|
| winget | Windows x64 | `packaging/winget/GeiserX.CashPilotDesktop/<version>/` | `winget install GeiserX.CashPilotDesktop` |
| Homebrew, own tap | macOS Apple Silicon | `packaging/homebrew/cashpilot-desktop.rb` | `brew install --cask geiserx/cashpilot/cashpilot-desktop` |
| AUR | Arch Linux x86_64 | `packaging/aur/cashpilot-desktop-bin/` | `yay -S cashpilot-desktop-bin` |

Each package installs the same binary the release publishes. Nothing is rebuilt.

- winget installs the Windows exe as a portable app and adds the `cashpilot-desktop` command. The exe is not code-signed yet, so the winget-pkgs checks may hold it for manual review.
- The Homebrew cask installs the signed and notarized `cashpilot-desktop.app`. `brew uninstall` also unloads the background helper (`cloud.cashpilot.desktop.helper`). `brew uninstall --zap` also removes the app data, the helper's LaunchAgent, the preferences and the WebKit data.
- The AUR package installs the Linux binary as `/usr/bin/cashpilot-desktop`, with a menu entry and the icon. Docker or Podman are optional dependencies, because either one works.

Docker Desktop or Podman is not installed by any of these packages. The setup wizard detects it and explains what to install.

## Refreshing the packages for a release

```bash
packaging/render.sh v0.32.0
go test ./packaging/
```

`render.sh` downloads the three release assets and the icon at that tag. It checks that the zip holds `cashpilot-desktop.app` and the tarball holds `CashPilot Desktop`. It then writes the version and the sha256 of each file into all three packages, renames the winget version directory and regenerates the AUR `.SRCINFO`.

`go test ./packaging/` checks three things. Every package downloads an asset name that `.github/workflows/desktop-release.yml` really publishes. All packages name the same version. Every checksum is a well-formed sha256, and the AUR `.SRCINFO` matches the `PKGBUILD`.

Then copy each package to its store:

- winget: the version directory goes to `manifests/g/GeiserX/CashPilotDesktop/<version>/` in https://github.com/microsoft/winget-pkgs, as a pull request. Run `winget validate --manifest <dir>` on Windows first.
- Homebrew: `cashpilot-desktop.rb` goes to `Casks/` in the tap repository. The install command above assumes a tap named `GeiserX/homebrew-cashpilot`, which does not exist yet.
- AUR: `PKGBUILD`, `.SRCINFO` and `cashpilot-desktop.desktop` go to the `cashpilot-desktop-bin` AUR git repository.

## Channels we do not offer

- **Mac App Store.** Apps there must run in the App Sandbox. CashPilot Desktop talks to the Docker socket outside its container, downloads and runs the native earner binaries, and installs a LaunchAgent. App Review Guidelines 2.4.5 and 2.5.2 rule out all three: https://developer.apple.com/app-store/review/guidelines/
- **Official Homebrew cask.** A self-submitted cask must clear Homebrew's notability bar (90 forks, 90 watchers, 225 stars), far above where this repository is: https://github.com/Homebrew/brew/blob/main/docs/Package-Acceptance-Policy.md
- **Microsoft Store.** Every executable must be signed by a trusted certificate authority (policy 10.2.9), and ours is not signed yet: https://learn.microsoft.com/en-us/windows/apps/publish/store-policies
- **Flathub.** Apps must build from source with no network access during the build, and must ask for the minimum permissions. CashPilot Desktop needs the Docker socket and host services, which give it the same power as host root: https://docs.flathub.org/docs/for-app-authors/requirements
- **Snap.** The `docker` interface is reserved, and classic confinement needs manual approval.

Packages also exist only for Apple Silicon Macs (Homebrew) and x86_64 Linux (AUR), because those are the only builds the release makes.
