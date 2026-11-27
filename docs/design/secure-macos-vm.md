# ADR: one Secure VM (macOS guest) for desktop-only earners

- **Status:** proposed. The lab ran on 2026-11-27. Every question has a lab answer, and the fence held 0 arrivals before and after a host reboot. One decision is not ours to make: Apple's licence excludes "relay service" use of a macOS guest (M8, decision 1). Until Sergio rules on it, nothing here ships. §6 lists the other open items.
- **Scope:** macOS hosts on Apple silicon only. Exactly one macOS guest. Only for services that have no Docker or native path.
- **Overrides:** `docs/research/vm-isolation.md:120` ("Do not build a VM orchestrator"), for this one case, the same way `docs/design/secure-windows-vm.md` does on Windows. The reason is security, not earnings. The rule that CashPilot never runs inside a guest stays. `docs/design/managed-runtime.md` (a Linux VM for containers) is not affected.

Words used here:

- **Host:** the user's Mac. **Guest:** the macOS VM inside it.
- **Tart:** the command-line VM manager that drives Apple's Virtualization.framework (VZ). **Softnet:** Tart's helper that filters the guest's packets in user space before they reach the host's network.
- **vmnet:** the macOS service that gives a VM a NAT network. It shows on the host as a `bridgeNNN` interface. The guest's own port on that bridge is a `vmenetN` member.
- **pf:** the macOS packet filter. Our rules live in the anchor `com.apple/cashpilot`.
- **L1, L2:** the fence layers. L1 is softnet. L2 is the pf anchor.
- **The cut:** `ifconfig <bridge> deletem <vmenet>`. It takes the guest's port off the bridge, so no frame moves.
- **The guest agent:** `tart-guest-agent`, a root daemon in the guest. `tart exec` reaches it over virtio-vsock, not over the network.
- **Red/green:** open one hole on purpose and see traffic pass (red). Close it and see nothing pass (green). A check that never went red proves nothing.
- **Canary:** a TCP or UDP listener that waits for a random token from the guest. Only an arrival counts.

---

## 1. Decision

CashPilot-Desktop builds one macOS guest, `CashPilot-VM`, from Apple's own restore image (IPSW). Every desktop-only earner runs in it. CashPilot stays on the host. It reads the guest only through the guest agent over virtio-vsock (`tart exec`). The fence lives outside the guest:

1. **L1, softnet:** Tart runs the guest with `--net-softnet-block "@host,<own public IPs>"`. Softnet already drops every packet to a private, shared, link-local or multicast IPv4 address and all IPv6. The block list adds the host itself and the user's public IPs. Softnet is fail-closed: if it dies, Tart powers the guest off.
2. **L2, pf:** a root anchor on the guest's bridge. It blocks IPv6 both ways, the host's multicast and broadcast toward the guest (Bonjour), every packet to the gateway except DHCP, and the same private and own-IP list. It kills the guest's old pf states when it loads.
3. **The guard:** a root LaunchDaemon. At boot, at wake and every 60 s, it compares the live fence with what the proof recorded. It cuts the guest's bridge port on any difference. It reconnects only on an exact match.
4. **Devices off:** no audio (`--no-audio`), no clipboard (`--no-clipboard`, and the agent runs without its clipboard part), no shared directory, no USB device beyond the virtual keyboard and pointer.
5. **Start at boot:** a root LaunchDaemon starts the guest before anyone logs in. It starts Tart as the VM's owner inside the owner's launchd domain (`launchctl asuser`). It cuts the port before the guest boots, loads pf, and lets the guard connect.

The fence is proven red, then green, at install, before any earner exists.

### Decisions for Sergio

| # | Question | Proposal |
|---|---|---|
| 1 | The macOS licence (§4, M8) | **Open, your call.** Apple's licence allows two macOS guests per Mac for development, testing, macOS Server or "personal, non-commercial use", and excludes "relay service" use. A bandwidth earner is a relay service. We see no way to read it that allows this. |
| 2 | Tart and softnet licences | Download pinned releases on demand, like the native earners. Do not bundle them. Both allow our use (M8). |
| 3 | SUID softnet | Accept it for phase 1. It is the only setuid binary we add, root-owned, in a root-owned folder. Phase 2 can remove it (M8). |
| 4 | Setup Assistant | Automate it over VZ's VNC, with the user's window as the fallback (M6). |
| 5 | FileVault in the guest | Off. Autologin needs it off. The host's FileVault covers the guest's disk image (M7). |
| 6 | Earner set | Wipter, Grass, Teneo, Theta and UpRock. Unsigned apps are refused (M10). |
| 7 | macOS updates in the guest | Turn off automatic OS downloads in the image and keep the background security data updates. CashPilot moves the guest to a new macOS version when it re-proves the fence (M13). |
| 8 | Compute earners | Theta runs other people's compute jobs, and UpRock crawls web pages. Both are inside the fence, so they cannot reach the LAN. We propose to keep both and to say so in the app's text (M12). |

---

## 2. The lab

- Host: a Mac mini M4, 16 GB of RAM, macOS 26.6.1 (25G76). It had Colima with an 8 GB Linux VM running for the whole lab, Tailscale with subnet routes, and Amphetamine. The host keeps itself awake by setting (`SleepDisabled 1`). We lifted that setting for the sleep test only.
- Tart 2.32.1 (Homebrew), softnet 0.26.0, tart-guest-agent 0.15.0, Packer with the Tart builder.
- Guests: macOS 26.6.2 (25G83) from Apple's IPSW. Each had 4 vCPU, 6 GB of memory and an 80 GB sparse disk on an external disk.
  - `cp-lab`, a throwaway guest. It ran the fence and device tests.
  - `cp-earn`, the test bed. It runs the five earners and stays on.
  - One vanilla guest from Cirrus Labs' public image, for M6 only, at 4 GB.
- Accounts: `cpadmin` (admin, password random per build, rotated per clone) and `earner` (standard user, autologin). Passwords came in on stdin, never as arguments.
- Probes ran in the guest through `tart exec`, which needs no guest network.
- Canaries ran on the host and on a second box on the LAN. The host canaries listened on its gateway, LAN, Wi-Fi, tailnet, loopback, IPv6 link-local and ULA addresses, and on a link-local metadata address. A Bonjour canary service also ran on the host. A positive control ran first each time: a token sent from the host side had to arrive.
- The lab scripts are in `scripts/macvm-lab/`. They hold no addresses: each run takes them as arguments.
- At the end we removed every throwaway VM, image, canary, loopback alias and pf reference. The test bed, its base image and its daemons stay.

---

## 3. Results

| Q | Answer | Evidence (short) | Design change |
|---|---|---|---|
| M1 | **Yes, with softnet and pf together.** Either one alone leaves a gap. | With vmnet NAT and no fence, 11 tokens arrived: the host on every address, a LAN box and a tailnet box over IPv4 and IPv6. pf alone: 0 arrivals. One hole opened: 1 arrival (red). Closed: 0. Softnet's default: 2 arrivals (the host's gateway address), and the gateway's DNS resolved private names. Softnet with `--block @host`: 0 arrivals, but the guest still saw the host's Bonjour services. Both layers: 0 arrivals and no Bonjour. The test bed: 0 of 45 targets before and after a host reboot. | Both layers stay. pf kills the guest's old states on load. The image uses public DNS resolvers. |
| M2 | **Yes.** `tart exec` works with the guest's port cut. | Probes ran through a cut port. A vsock scan from the guest to the host's ports 1 to 65535 found nothing open. Shared folder, clipboard and audio were each red when on and green when off. The only USB devices are the virtual keyboard and digitizer. | The guest agent runs with `--run-rpc` only. |
| M3 | **One admin step at install. After that, nothing asks.** | Root owns softnet (setuid), the pf anchor, the guard and the boot daemon. Tart runs as the user. After the install no prompt appeared, at boot or later. In the guest, the agent runs as root, and `earner` is not an admin. | The install text names the setuid binary and the two daemons. |
| M4 | **Yes.** The guard cuts on every drift we made and reconnects only on an exact match. | Rules flushed: cut in 42 s. pf turned off: cut in 19 s. Own IP removed from the table: cut in 46 s. Softnet with a weaker block list, and a wrong gateway: both seen as a mismatch. At boot and after a wake, the first pass cuts, checks, then connects. | None. A drift can live up to 60 s before the cut. |
| M5 | **Yes, after two fixes.** | Guest reboot: same VM process, same softnet, autologin. Host reboot: red first (the guest never started). After the fixes, the guest's port was cut 38 s after boot, the guard connected it, all five earners started, and the fence held 0 arrivals. Host sleep of 103 s: the guard cut after the wake, then connected. The guest, its clock and its earners survived. | The boot daemon uses `launchctl asuser` and `AbandonProcessGroup`. |
| M6 | **Build our own image from Apple's IPSW.** Automating it is fragile. | 1 of 3 Packer builds succeeded. One stalled on SSH, one lost keystrokes. The Cirrus vanilla image has `admin/admin` with passwordless sudo, Gatekeeper off, a world-readable `/etc/kcpassword`, SSH and Screen Sharing on, and no guest agent. | Our IPSW build stays. The user's window is the fallback for Setup Assistant. |
| M7 | **Yes.** Two accounts, FileVault off, Keychain works only in the user's session. | `earner` logs in by itself after every boot. `/etc/kcpassword` is root, mode 600. The admin password was rotated, and the old one is refused. `security` over SSH fails with "User interaction is not allowed". It works through `launchctl asuser`. | Rotate the admin password per clone. Run anything that needs the Keychain through `launchctl asuser`. |
| M8 | **Tart, softnet and VZ allow it. Apple's licence does not.** | VZ refused a third macOS guest. The SLA excludes "relay service" use. Tart and softnet are under source-available licences that allow non-competing use. Code-Hex/vz (MIT) is a Go binding to VZ. | Decision 1. Phase 2 can drive VZ from Go and drop the setuid softnet. |
| M9 | **Yes for the cap. No for pf drop counters.** | A dummynet pipe of 10 Mbit/s cut upload to 8.9 Mbit/s. Download was not touched. Removing it gave back full speed. pf counted 0 for packets that softnet had already dropped. | The slider is an upload limit. The tile does not show "blocked packets" from pf. |
| M10 | **Yes.** Notarized apps install. Unsigned ones are refused. | Five apps passed the SHA-256, Team ID and Gatekeeper checks. Spide ("no usable signature") and Noderr (no Team ID) were refused. Two apps need Rosetta. | Apps go to `/Applications` as root. CashPilot updates them from the catalog pin. |
| M11 | **Yes.** Three apps by a file, Theta by its process, UpRock by its log. | Wipter, Teneo and Grass write a file or key only after sign-in. Theta has no account. UpRock's database is encrypted, and its log says "logged in". | The reader returns presence and a state word only. |
| M12 | **No vendor in the set forbids VMs.** EarnApp does, so it is out. | Read on 2026-11-27. The table is in §4. | The catalog gets three macOS fixes and one new entry. |
| M13 | **Yes, beside Colima.** The cost is 6 GB of RAM and about 1.4 CPU cores. | Colima's VM ran for the whole lab. The guest's memory footprint was its full 6 GB. CPU averaged 1.44 cores, of which macOS's own update scan was 0.66. The disk used 38 GB of 80. VZ held host sleep back by 30 s. | The setup check needs 6 GB of free RAM. Decision 7. |

---

## 4. Per question

Every host command below ran as root on the lab host, unless it says "guest". Guest commands ran through `tart exec`, as root. The probe (`probe.sh`) sends `<token>-<label>` to each target. The canaries (`canary.py`) log what arrives. `fence-run.sh` runs one step and counts the arrivals of that step's token.

### M1. The fence

```sh
# L1: softnet with the host and the user's public IPs blocked
tart run CashPilot-VM --no-graphics --no-audio --no-clipboard --net-softnet-block "@host,<own public IP>/32,..."
# L2: pf anchor on the guest's bridge
fence-pf.sh up <bridge> <gateway> <deny-file> <guest subnet>
# one step
fence-run.sh <vm> <targets> <gateway> <step> <out-dir> <canary logs...>
```

The targets are:

- the host's gateway address (TCP, UDP, SSH, VNC, AirPlay, DNS)
- the host's LAN, Wi-Fi and tailnet addresses, and its IPv6 link-local and ULA addresses
- the LAN router, a LAN box, and a tailnet box over IPv4 and IPv6
- the tailnet DNS server and the link-local metadata address
- the user's public IPs
- six Bonjour service types
- three DNS names (a split-DNS name, a tailnet name, a public name), and two DNS servers asked directly

Two HTTPS fetches and two IP-echo services are the positive controls.

| Step | Layout | Result |
|---|---|---|
| A0, red | vmnet NAT, no fence | **11 arrivals**: the host on its gateway (TCP and UDP), LAN, Wi-Fi, tailnet and both IPv6 addresses, a LAN box (TCP and UDP), and a tailnet box (IPv4 and IPv6). Bonjour showed the host's services. The gateway's DNS resolved a private split-DNS name. |
| A1, green | pf anchor only | 0 arrivals. Internet OK. |
| A2, green | pf only, with guest IPv6 on | 0 arrivals. |
| A3, green | pf loaded after the guest had live connections, states killed | 0 arrivals. Without the state kill, a connection opened before the load stayed open. |
| A4, red | pf with one hole for the LAN box | **1 arrival**, from the LAN box. |
| A5, green | hole closed | 0 arrivals. |
| S0, red | softnet default, no pf | **2 arrivals**, on the host's gateway address (TCP and UDP). The gateway answered DNS, also for private names. The host's Bonjour services showed. |
| S1 | softnet with `--block @host,<own IPs>`, no pf | 0 arrivals. The host's Bonjour services still showed: softnet does not stop the host's multicast toward the guest. |
| S2, green | softnet and pf | 0 arrivals. No Bonjour. Gateway DNS timed out. |
| G0, green | the port cut | 0 arrivals. Nothing works, including the internet. `tart exec` still works. |
| T0, green | the test bed, before the host reboot | 0 arrivals of 45 targets. |
| T1, green | the test bed, after the host reboot (new gateway, new /29) | 0 arrivals of 45 targets. The positive control reached the host canary and the LAN box canary. |

Each start of the guest gets a new random /29 and gateway from softnet, so `start.sh` reads the gateway from the bridge every time. The pf anchor is built from that.

When softnet died, Tart powered the guest off. That is the fail-closed behaviour we want.

A split-DNS name still resolves through public DNS, because it is public. Its connect is blocked. Only the name leaks.

**Answer:** yes, with both layers. Softnet alone lets the host's Bonjour through. pf alone depends on a ruleset that anything with root can flush. **Design:**

- Both layers stay. The guard checks both.
- pf kills the guest's states on load.
- The image sets public DNS resolvers (`networksetup -setdnsservers`). The gateway is blocked, and its DNS would answer private names.

### M2. Host control without network

```sh
tart exec <vm> /usr/bin/true                    # works with the port cut
guest: vsock-probe 2 1 65535                    # connect to every host vsock port
```

| Step | Result |
|---|---|
| `tart exec` with the port cut | works |
| vsock from the guest to the host (CID 2), ports 1 to 65535 | none open |
| Positive control: guest-local CID 3, port 8080 (the agent) | open |
| Shared folder on (red) | the guest read our token file |
| Shared folder off | no virtiofs mount |
| Clipboard on (red) | a token copied on the host came out of the guest's pasteboard |
| Clipboard off, agent without `--run-vdagent` | the clipboard agent fails, no channel |
| Audio on (red) | "Apple Virtual Sound Device" with a microphone input |
| `--no-audio` | a null speaker, no input |
| USB | only the virtual digitizer and keyboard on the XHCI controller |

Tart offers no host-side vsock listener of its own, so there was nothing to make red on the host side. The scan shows that nothing else listens.

**Answer:** yes. **Design:** the agent runs as a root LaunchDaemon with `--run-rpc` only. Tart runs with `--no-audio --no-clipboard` and no `--dir`.

### M3. Privileges

| Part | Runs as | Why |
|---|---|---|
| softnet | setuid root, in a root-owned folder | vmnet needs root |
| pf anchor and dummynet pipe | root | pf |
| the guard | root LaunchDaemon | reads pf, cuts the bridge |
| the boot daemon | root LaunchDaemon, starts Tart as the owner | the guest starts before anyone logs in |
| Tart | the VM's owner | VMs live in the owner's home |
| guest agent | root, in the guest | `tart exec` |
| earner apps | `earner`, a standard user | they need no admin |

The install needs one admin authorization. After it, nothing asked for a password, at boot, at wake or in daily use.

**Answer:** one admin step at install. **Design:** the setup text names the setuid softnet, the guard and the boot daemon. Removal deletes all three.

### M4. The guard

```sh
guard.sh <state-dir>     # from guard.plist: at load and every 60 s
```

| Drift | Seen as | Time to cut |
|---|---|---|
| D1: `pfctl -a com.apple/cashpilot -F rules` | `rules` | 42 s |
| D2: `pfctl -d` (pf off, and NAT stops too) | `pf-off` | 19 s |
| D3: an own IP removed from the table | `table` | 46 s |
| D3b: the port added back by hand (`addm`) while the drift stays | cut again at the next pass | under 60 s |
| D4: softnet started with a weaker block list | `softnet` | next pass |
| D4b: the anchor loaded for a wrong gateway | `rules` | next pass |
| Host boot | first pass cuts, checks, connects | 0 s |
| Host wake after 103 s | `boot/wake: vmenet0 cut`, then `match; connected` | 0 s |

After each drift was undone exactly, the next pass reconnected the port. After D2, `pfctl -E` was needed before it could.

The guard decides "wake" from a gap of more than 90 s since its last pass. A sleep of 2 s did not count as a wake. That is acceptable, because every pass checks the whole fence.

**Answer:** yes. **Design:** no change. A drift can last up to 60 s. The guard never reads the guest.

### M5. Persistence

| Step | Result |
|---|---|
| Guest reboot (`shutdown -r` in the guest) | the same Tart process, the same softnet process and the same bridge port. `earner` logged in by itself |
| Host reboot 1, red | the boot daemon ran, Tart printed nothing, and the guest never got a port. `start.sh` gave up after 60 s |
| The same script from a root shell | worked |
| Fix 1 | start Tart through `launchctl asuser <owner uid>`. A system daemon sits outside the owner's launchd domain, where VZ's XPC services live |
| Fix 2 | `AbandonProcessGroup` in the plist. Without it, launchd kills Tart when `start.sh` exits |
| Host reboot 2, green | ssh came back within 15 s of the boot. The guest's port showed, and `start.sh` cut it, 38 s after the boot. It loaded pf for the new gateway and ran the guard, which connected the port. All five earners were running in under 2 minutes. T1: 0 arrivals |
| Host sleep, first try | refused: the lab host sets `SleepDisabled 1`. We lifted it for the test only |
| Host sleep, second try | the system slept 2 s and woke at once (DarkWake) |
| Host sleep, third try, green | slept 103 s, woke on its timer. The guard's first pass cut, checked and connected. The guest kept running. Its clock was right, the internet worked, and all five earners were alive |

VZ delayed every host sleep by 30 s: the kernel log shows `com.apple.Virtua timed out(30000 ms)`.

**Answer:** yes. **Design:**

- The boot daemon starts Tart with `launchctl asuser` and has `AbandonProcessGroup`.
- At boot it waits up to 300 s for the VM's disk, which may live on a volume that mounts late.

### M6. Guest image

| Source | Result |
|---|---|
| Apple IPSW, Packer with the Tart builder, Setup Assistant typed over VZ's VNC | 1 of 3 builds succeeded. One stalled waiting for SSH. One lost its place in the keystrokes and typed a command into the wrong field |
| `tart create --from-ipsw latest` | failed once with `VZErrorDomain Code=10001` ("The restore image catalog failed to load"). A pinned IPSW URL works |
| Cirrus Labs vanilla image | `admin`/`admin`, passwordless sudo for `admin`, Gatekeeper off, `/etc/kcpassword` mode 644, SSH and Screen Sharing on, no guest agent |

Packer typed capital letters and symbols. Our own VNC client (`vncdotool`) could not: VZ's VNC server ignored Shift and Command from it. Apple's Screen Sharing app sends them correctly. Text that needs them can go through the earner's pasteboard instead (`launchctl asuser <uid> pbcopy`), followed by the app's Edit > Paste menu.

**Answer:** build from Apple's IPSW. The Cirrus image is made for CI, and every point above breaks our design. **Design:**

- Pin the IPSW URL and its hash.
- The base provisioning (`provision.sh base`) installs the agent and keeps the guest awake. It turns Spotlight indexing and Screen Sharing off, checks Gatekeeper, sets public DNS and turns Remote Login off from the next boot on.
- Setup Assistant is automated. When the build fails, the user finishes it in the window.

### M7. Accounts, FileVault and Keychain

| Check | Result |
|---|---|
| `earner` | standard user, `dseditgroup -o checkmember -m earner admin` says no |
| Autologin | `/etc/kcpassword` (root, mode 600) and `autoLoginUser earner`. It survives guest and host reboots |
| FileVault in the guest | off. Autologin does not work with it on |
| Admin password rotated (`sysadminctl -resetPasswordFor`) | the new one works (`dscl . -authonly`), the old one is refused |
| `security add-generic-password` over SSH | fails: "User interaction is not allowed" |
| The same through `launchctl asuser <earner uid>` | works |

Every clone of a base shares the base's admin password until it is rotated.

**Answer:** yes. **Design:**

- The first `tart exec` after a clone rotates the admin password. The passwords live in the host's Keychain, never in a file.
- Anything that needs the earner's Keychain runs through `launchctl asuser`.

### M8. Licences and limits

| Item | Finding |
|---|---|
| macOS Tahoe SLA §2B(iii) | up to two more copies in VMs, for development, testing, macOS Server or personal, non-commercial use. It excludes relay service use |
| VZ concurrent limit | a third macOS guest was refused while two ran |
| Tart | Homebrew ships 2.32.1 under Fair Source License 0.9. Upstream is now 2.42.0 under FSL-1.1-ALv2. Both allow use that does not compete with Tart |
| softnet | 0.26.0, under FSL-1.1-ALv2. It allows use that does not compete with softnet |
| Code-Hex/vz v3.8.0 | MIT. A Go binding to VZ. It needs the `com.apple.security.virtualization` entitlement on our signed binary |

**Answer:** the tools allow it. Apple's licence does not, as far as we can read it. **Design:** decision 1 is Sergio's. Phase 2 can drive VZ from Go with Code-Hex/vz, filter in our own process and drop the setuid softnet.

### M9. Caps and counters

```sh
fence-pf.sh up <bridge> <gateway> <deny-file> <guest subnet> 10000   # upload cap in kbit/s
```

| Cap | Upload (guest) | Download (guest) |
|---|---|---|
| none | 29.6 MB/s | 74.5 MB/s |
| 10 Mbit/s | **1.11 MB/s (8.9 Mbit/s)** | 78.7 MB/s |
| removed | 21.8 MB/s | not measured |

pf's rule counters stayed at 0 for traffic toward ranges that softnet already drops. Softnet drops before pf sees the packet.

**Answer:** yes for the cap. pf cannot count what softnet blocks. **Design:** the slider is an upload limit. The tile shows the guest's traffic from the bridge's byte counters, not "blocked packets".

### M10. Install mode and app trust

```sh
tart exec <vm> /bin/sh -c "$(cat install-app.sh)" install-app <url> <sha256> <team-id>
```

| App | Version | Team ID | Arch | Result |
|---|---|---|---|---|
| Wipter | 1.25.1048 | 55Y88XCDLZ (Dolph Data LLC) | arm64 | accepted |
| Grass | 8.1.0 | ZD9734TW8Q (Lower Tribeca Corp) | arm64 | accepted |
| Teneo Beacon | 0.4.9 | 5A4KV93G7B (Teneo Labs Inc.) | universal | accepted, SHA-256 matches the release |
| Theta Edge Node | 5.2.15 | 5Q394YSNFN (Theta Labs) | x86_64, needs Rosetta | accepted |
| UpRock Mining | 0.0.38 | Y46RSV89ZD (Three Amigos Group, LLC) | x86_64, needs Rosetta | accepted, SHA-256 matches |
| Spide | none in the plist | none | x86_64 | **refused**: "no usable signature" |
| Noderr Micronode | 1.0.7 | none (identifier `a.out`) | universal | **refused** by Gatekeeper |

The guest has no developer tools, so `install-app.sh` reads the architecture with `file`, not `lipo`. Rosetta installed only after the guest had working DNS.

The guest shows "Support Ending for Intel-based Apps" for Theta: that version "will not open in a future release of macOS". Theta and UpRock run only under Rosetta, so they stop working when Apple removes it.

Apps go to `/Applications`, owned by root. So they cannot update themselves. Grass shows a banner asking to turn on Auto Update. We left it off.

Grass, Teneo and UpRock registered their own login items in `earner`'s session. Wipter and Theta did not. For those, `provision.sh autostart` writes a root-owned LaunchAgent in `earner`'s library that runs `open -g <app>` at login.

**Answer:** yes. **Design:**

- The catalog pins URL, SHA-256 and Team ID per app. A failed check refuses the app.
- Rosetta is installed in the image.
- CashPilot updates the apps from the catalog pin.
- Apps without a login item get the root-owned LaunchAgent.

### M11. Sign-in markers

All paths are under `/Users/earner/Library/Application Support/`.

| App | Signed in | Not signed in | Read only |
|---|---|---|---|
| Wipter | `wipter-app/secure-credentials.json` exists | the file is absent | presence |
| Teneo | `teneo.node/.token`, `.email`, `.consent_accepted`, `.node_api_key` exist | the files are absent | presence |
| Grass | `io.getgrass.desktop/store.json` has the keys `wynd:device_id` and `wynd:status` | `store.json` already has `accessToken`, `refreshToken` and `wynd:authenticated: "true"` before any account sign-in, from an anonymous device login. Only the two keys above are new | the key names, never a value |
| UpRock | `UpRock/logs/mining.log` has `logged in` and `set earning {"enabled": true}` | not seen | the last state words in the log |
| Theta | no account. The node wallet lives in `Theta Edge Node/ThetaEdgeNode/` | none | process running |

UpRock's `main.db` is encrypted ("file is not a database").

The guest's screen is VZ's own framebuffer, served by VZ's VNC server on the host's loopback. Driving it needs no Screen Recording or Accessibility grant on the host.

**Answer:** yes. Three apps have a file marker, Theta is read by its process, and UpRock by its log. **Design:** a later `guest.macos.signed_in` block in the catalog gets these markers. The reader returns booleans and a state word, never a value or a log line.

### M12. Earners

Read on 2026-11-27.

| App | macOS client | Sign-in | Vendor stance on VMs | Source |
|---|---|---|---|---|
| Wipter | arm64 | email and password | no VM clause found | https://www.wipter.com/en |
| Grass | arm64 | email and password or a one-time code, reCAPTCHA | no VM or VPS clause in the terms | https://www.grass.io/terms-and-conditions |
| Teneo Beacon | universal | email | none found. Our test IP got "IP not eligible" | https://github.com/TeneoProtocolAI/teneo-node-app-release-beta |
| Theta Edge Node | x86_64 | none, a local node wallet | none found in the setup docs | https://docs.thetatoken.org |
| UpRock Mining | x86_64 | email, one-time code | none found. Its help centre lists "Desktop (Windows, Linux and MacOS)" | https://uprock.com |
| EarnApp | universal | device link | "Installing EarnApp on Virtual Machines (VMs), Docker containers, or hosting services is strictly prohibited" | https://help.earnapp.com/hc/en-us/articles/10199416541969 |
| Honeygain | x86_64 | email and password | "in case the user setup is flawed, our fraud-prevention algorithms will be triggered, which may get your Honeygain account banned" | https://honeygain.zendesk.com/hc/en-us/articles/360013096900 |
| Bitping | arm64 | email and password | servers allowed | https://github.com/BitpingApp/Bitping-Node |
| MystNodes | universal | claim on the web | VPS accepted | https://github.com/mysteriumnetwork/myst-launcher-release |

EarnApp and Honeygain stay out. The EarnApp and Honeygain quotes come from earlier reads. Cloudflare blocked our re-read on 2026-11-27.

What the apps do inside the guest:

- Teneo said "IP not eligible" on our home line. The message names 1.1.1.1 and WARP, but it stayed after we moved the guest to Quad9. So it is the IP, not the DNS.
- Theta runs compute jobs. It downloaded miniconda and two Python environments into `earner`'s home (2.4 GB). It also tried to call `cc` and opened the developer-tools install dialog. We cancelled it.
- UpRock crawls web pages for others. Its log asks for a browser it does not find.
- Teneo and Theta asked for Local Network access. We answered Don't Allow. That is a guest-side layer only. The fence does not depend on it.

**Answer:** no app in the set forbids VMs. **Design:** the catalog changes in §5.

### M13. Coexistence and cost

| Measure | Value |
|---|---|
| Colima beside the guest | ran for the whole lab, 8 GB VM, no conflict. Each uses its own vmnet bridge |
| Guest memory on the host | 6.0 GB physical footprint (all of the guest's 6 GB) |
| Host memory | 16 GB, with Colima: 63% free, 2 GB of swap in use |
| CPU, 60 s average with five earners | 1.44 cores for the VM process. In the guest: the macOS update scan 0.66, Theta's `edgecore` 0.38 |
| Tart itself | 0.2 cores, with the VNC server on |
| Disk | 38 GB on disk of an 80 GB sparse image. The guest's root uses 12 GB |
| Host sleep | delayed 30 s by VZ |

The guest checks for macOS updates on its own, with automatic download on.

**Answer:** yes. **Design:** setup checks for 6 GB of free RAM and 40 GB of free disk. Decision 7 covers the guest's updates.

---

## 5. Changes to the design

1. **Two fence layers:** softnet with `--block @host,<own IPs>`, and pf. Neither one alone is enough (M1).
2. **pf kills the guest's states** when it loads (M1).
3. **Public DNS in the image.** The gateway is blocked, and its DNS would answer private names (M1).
4. **The boot daemon** starts Tart with `launchctl asuser <owner uid>` and sets `AbandonProcessGroup` (M5).
5. **The admin password** is rotated after each clone, and the passwords live in the host's Keychain (M7).
6. **Rosetta** goes in the image. Two earners are x86_64 only, and Apple warns that such apps will stop opening (M10).
7. **Autostart:** a root-owned LaunchAgent in `earner`'s library for apps with no login item of their own (M10).
8. **App updates** come from the catalog pin. The apps are root-owned and cannot update themselves (M10).
9. **The user's window** uses a client that sends modifier keys, such as Apple's Screen Sharing. Sign-in text that needs Shift goes through the earner's pasteboard and the app's Paste menu. The pasteboard is cleared after each use (M6).
10. **The bandwidth slider** is an upload limit. The tile does not count blocked packets (M9).
11. **Sign-in markers:** the Grass marker is `wynd:device_id`, not `accessToken`. UpRock is read from its log (M11).
12. **The catalog:**
    - Teneo, UpRock and Spide get their macOS clients listed.
    - Spide and Noderr stay out of the guest: unsigned.
    - Kaisar Node (arm64) is not in the catalog yet.
    - EarnApp and Honeygain never get a `guest.macos` block.
13. **The guest's macOS updates** follow decision 7 (M13).

## 6. Still open

| Item | Why open | What closes it |
|---|---|---|
| M8, Apple's licence | "Relay service" is excluded | Sergio's decision 1 |
| M6, reliable Setup Assistant automation | 1 of 3 builds succeeded | A build that waits on screen states, not on fixed delays, and 5 green builds in a row |
| M6, VNC modifier keys from our own client | VZ's VNC server ignored Shift and Command from `vncdotool` | Test another client library, or keep the pasteboard path |
| M9, the traffic tile | The bridge byte counters are the plan. We did not read them under the cap | One run that compares the bridge counters with the guest's own counters |
| M11, UpRock's red state | We saw only the signed-in log | A fresh install, read before sign-in |
| M12, Teneo | "IP not eligible" on our line | A test from another residential IP |
| M13, guest updates | Automatic download is on in the test bed | Decision 7, then the image setting |
| Phase 2, Code-Hex/vz | Not built | A Go prototype that boots the guest with our own filter and no setuid helper |
| M10, Theta and UpRock on Rosetta | Both are x86_64 only, and macOS warns that Intel apps will stop opening | An arm64 build from each vendor, or both leave the set |
| A macOS update on the host | Cannot be run on demand | The re-proof after each change of the host's macOS build |
