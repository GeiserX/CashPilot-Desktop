# ADR: one Secure VM (Windows 11 Pro) for desktop-only earners

- **Status:** accepted. The lab ran on 2026-11-24 and 2026-11-25. Three questions wait for a host reboot or a host sleep (Q5 reboot part, Q8, Q16). Three lab results are not proven yet: Q3 (unbinding `ms_tcpip6` alone), Q4 (the locale-free drop-counter read) and Q13 (narrow listeners without registration). §6 lists them with every other open item.
- **Scope:** Windows hosts only. Exactly one Hyper-V VM. Only for services that have no Docker or native path.
- **Overrides:** `docs/research/vm-isolation.md:120` ("Do not build a VM orchestrator"), for this one case. The rule that CashPilot never runs inside a guest stays. `docs/design/managed-runtime.md` (a Linux VM for containers) is not affected.

Words used here:

- **Host:** the user's Windows PC. **Guest:** the Windows VM inside it.
- **Port ACL:** an allow or deny rule that Hyper-V applies to the guest's virtual network port, outside the guest.
- **L1, L2, L3:** the fence layers. L1 is the Internal switch with WinNAT. L2 is the port ACLs. L3 is the host firewall rule on the switch's host adapter (`vEthernet`).
- **Red/green:** open one hole on purpose and see traffic pass (red). Close it and see nothing pass (green). A check that never went red proves nothing.
- **Canary:** a TCP or UDP listener on the host that waits for a random token from the guest. Only an arrival counts.

---

## 1. Decision

CashPilot-Desktop builds one Hyper-V VM, `CashPilot-VM`, from the user's own Windows 11 ISO and key. Every desktop-only earner runs in it. CashPilot stays on the host. It reads the guest only over PowerShell Direct. The fence lives outside the guest:

1. **L1:** an Internal switch with its own WinNAT prefix, a /29 in 172.16/12.
2. **L2:** port ACLs. They deny every private, special and multicast range, all IPv6, the host's own addresses, the user's public IPs and every prefix routed through a tunnel. **The gateway's MAC comes from static neighbour entries, so no Allow rule exists** (§3, Q1).
3. **L3:** an inbound Block rule on the switch's `vEthernet`. IPv6, file sharing and the client are also unbound from that adapter.
4. **The guard:** a SYSTEM task. It disconnects the VM's adapter at boot and on resume. It compares the live state with `desired.json`. It reconnects only on an exact match.

The fence is proven red, then green, at install, before any earner exists.

### Decisions on the open questions

| # | Question | Decision |
|---|---|---|
| 1 | Who holds Hyper-V rights | Setup adds the user to Hyper-V Administrators (`S-1-5-32-578`). The lab showed what this gives (Q6). The setup text says it plainly. |
| 2 | Enhanced Session | The host owner turns it off before product Setup. The setting applies to the whole host. |
| 3 | Code signing | **Phase 2 ships unsigned.** We buy no certificate for now. The release job signs when the secrets exist and skips the step otherwise, so a missing certificate never fails the Windows release. Windows shows a SmartScreen warning on download, and the wizard and `docs/secure-vm.md` explain it. The admin prompt is not affected: it elevates `powershell.exe` (`RunElevated`, `internal/runtime/firewall_windows.go`), so it shows "Windows PowerShell" with Microsoft as the verified publisher. It never says "Unknown publisher" for CashPilot. The cost of a certificate is still open. |
| 4 | 90-day trial | Only as a small "testing only" link, never the default. |
| 5 | Honeygain | Stays out of the VM. |
| 6 | Minimum host | 12 GB RAM and 100 GB free on the instance data volume. |
| 7 | Live test | By hand on a lab host for each release that touches `internal/winvm`. No self-hosted runner. |
| 8 | Is the VM worth a licence | Build it. The wizard shows the licence cost against the expected earnings before the key screen. |
| 9 | Guest edition | A Home or a Pro key is accepted. Q15 proved that the answer file, autologon and PowerShell Direct work on Home. |
| 10 | Approved networks | On by default. A metered network is never approved. |
| 11 | Bandwidth cap | No cap by default. The tile has a slider. Q14 showed that the cap limits the VM's upload only. |

---

## 2. The lab

- Host: Windows 11 Pro 26H2, build 26300.10075, x64, Spanish locale, 32 GB RAM. It had Hyper-V, WSL2 (2.7.3), Docker Desktop 4.73.1 (installed, engine not running), Tailscale (accepting subnet routes), WireGuard 0.5.3 and an OpenVPN TAP-Windows V9 adapter. Another VM ran on an External switch for the whole lab. We never touched it, its switch or its adapter. We recorded its configuration before and after, and it did not change.
- Throwaway guests. Each was Gen 2 with Secure Boot, vTPM, 2 vCPU (`-Maximum 75 -RelativeWeight 50`), 4 GB static memory and an 80 GB dynamic VHDX. Checkpoints were off. Nested virtualization was off.
  - One Windows 11 Enterprise Evaluation 26H2 guest. It ran the fence tests.
  - One Home guest and one Pro guest, from Microsoft's consumer 26H2 ISO (`Windows11_Client_x64_en-us_26300_9457.iso`). The ISO's `setup.exe` has a valid Microsoft signature. The ISO holds 11 editions: Home at index 1 and Pro at index 6.
- Network: an Internal switch. The host `vEthernet` took the first address of a /29 in 172.16/12, and `New-NetNat` covered the same /29. The guests took the next addresses, with static MACs. The deny list was the design's list plus the host's public IPs.
- Guest accounts came only from the answer file: `cpadmin` (Administrators) and `earner` (Users), with autologon for `earner`. The passwords were random. They sat in a machine-DPAPI file that only Administrators and SYSTEM can read.
- Probes ran in the guest over PowerShell Direct. A positive control ran first each time.
- At the end we removed every throwaway VM, disk, switch, NAT, rule, task, user and file.

---

## 3. Results

| Q | Answer | Evidence (short) | Design change |
|---|---|---|---|
| Q1 | **Yes.** Static neighbour entries on both sides and no Allow rule: forwarding works, and the gateway's ports are dead. | Without entries, the guest's ARP for the gateway stays `Incomplete`, and there is no internet. With the guest entry alone, the host's ARP for the guest stays `Incomplete`. With both, the internet works, and the egress is the host's IP. With L3 off, the gateway TCP canary, ports 135 and 445, ICMP and the LAN all time out. | **This is the chosen ARP fix.** Both entries are needed. The host's `vEthernet` MAC goes into `desired.json`. |
| Q2 | Extended ACLs work on client Windows 11 and **do not touch ARP**. | With the gateway Allow plus an extended Deny of TCP and UDP to the gateway, ARP stays `Reachable`, and the internet works. The TCP and UDP canaries get nothing. ICMP still passes. An extended Deny with any protocol also leaves ARP alone. A simple Deny beats an extended Allow. | Kept as the documented fallback. Not used. |
| Q3 | L3 leaves WinNAT working. **Outbound host-firewall rules do not filter forwarded traffic.** IPv6 to the host's link-local address is a real path. | An outbound Block for 1.0.0.1 blocks the host itself, not the guest. With an L2 Allow for a LAN server, the server answers even with L3 on. Over IPv6 the guest reaches a canary on the host's link-local `vEthernet` address when `::/0` and L3 are off (red). `::/0` alone blocks it. L3 alone blocks it. **Unbinding `ms_tcpip6` alone is not proven:** its only run was broken (§4, Q3). The guest has no IPv6 default route. | L2 is the only filter for the LAN. L3 protects only the host. The proof gains an **IPv6 red step**. The unbinding stays, but no green step relies on it until it is proven. |
| Q4 | **No.** Meter ACLs never count packets that a Deny drops. | 10 MB to an allowed address read 11 MB. 10 MB of UDP to a denied range read 0 MB. Meter rows also count in whole megabytes. The per-adapter drop counter **Outgoing FailedSecurityPolicy** counted exactly +1000 for 1000 denied datagrams, +37 for 37 denied connects and +0 for 500 allowed ones. We read it through the localized performance-counter path. **The locale-free read is not proven yet** (§4, Q4). | **Drop Meter ACLs.** The tile reads the drop counter and says "blocked packets". |
| Q5 | **Partly.** Everything survives a VM stop and guest reboots. A host reboot and a feature update are not tested. | The sorted ACL list was identical across a VM stop. The host neighbour entry and the L3 rule are in `PersistentStore`. The guest's static ARP survived four guest reboots. | None yet. The re-proof after each Windows build change stays. **Needs a host reboot.** |
| Q6 | **Yes, for a standard account.** | A standard user in Hyper-V Administrators, at medium integrity in a fresh logon, ran `Get-VM`, disconnect and connect, and PowerShell Direct. **It saw every VM on the host, including VMs that are not ours.** Without the group, every call failed with a permission error. We could not test the non-elevated admin case or `vmconnect` without a desktop session. | The setup text says "full control of Hyper-V and of every VM on this PC". |
| Q7 | **Yes.** WinNAT coexists with WSL2 and with a second NetNat. | Two NetNats with separate prefixes both forwarded traffic on build 26300. A WSL2 distro in NAT mode reached the internet at the same time. So did another account's WSL in mirrored mode. Docker Desktop was installed but not running. | The preflight blocks only an **overlapping** NetNat prefix. It picks a free /29. |
| Q8 | **Partly.** The guard logic works as SYSTEM. The boot trigger is not tested. | Run by hand with the VM off, the guard disconnected, compared, connected and started the VM. The guest logged on `earner` by itself, and PassiveApp started. After we removed one Deny, the guard logged `MISMATCH: acl` and stayed disconnected. When we restored the Deny, it reconnected. | `desired.json` holds the switch name, because a disconnect clears it. **Needs a host reboot.** |
| Q9 | **No.** Apps installed in `cpadmin`'s session do not start for `earner`. | PassiveApp installs per user, into the installer's `%LOCALAPPDATA%`. Wipter with `/S /allusers` goes to Program Files, but its autostart is a per-user Run key that the app writes later. After the switch back to `earner`, nothing ran. Installed or first started in `earner`'s session, neither app needed admin. PassiveApp then wrote its Run key and started after a reboot. | **Install mode runs in `earner`'s session.** An admin installer gets the UAC prompt with `cpadmin`'s credentials. The password is rotated after. |
| Q10 | **Yes.** | With KVP, Guest Service Interface and VSS off, PowerShell Direct works, and so does `Copy-Item -ToSession`. With `vmicvmsession` disabled and stopped in the guest, it fails (red), then works again. KVP that was on during setup left the host's name in the guest's registry. | **KVP off before the first boot.** Integration services are picked by ID, never by name. |
| Q11 | **Yes, for traffic that leaves on the physical NIC. Not always.** | After NAT, red 3 (own public IP) showed on the physical NIC, with the destination and port intact. Red 2 (the router) left through the **Tailscale** adapter, because a tailnet subnet route for the LAN has metric 0. pktmon also logs each L2 drop at the VM's port. | Capture on **all NICs** (`--comp nics`), filtered on destination and port. The pktmon drop records count as green evidence. |
| Q12 | Tailscale, WireGuard and OpenVPN TAP all show as non-physical. Work VPN clients were not tested. | All three have `ConnectorPresent=False` and `Virtual=True`. **On a host whose LAN NIC sits under an External vSwitch, the default route uses `vEthernet (<switch>)`, which is not physical either.** | The physical test also accepts a `vEthernet` that is bound to an External switch over a physical NIC. |
| Q13 | **No reachable listener was found.** Registration is proven necessary only for the wildcard listener. | `GuestCommunicationServices` held only the two "VM Session Service" entries. The guest's connections to both were refused. Our own registered listener was reached when bound to the wildcard ID, to `HV_GUID_CHILDREN` or to the guest's own VM ID (red). Without the registry entry, the wildcard listener was refused. We did not test the other two bindings without the entry. | The guard's registry compare is a valid signal for wildcard listeners. The narrow bindings stay open (§6). |
| Q14 | **Yes, for the VM's upload.** | With a cap of 10 Mbit/s, upload fell from 62 to 9.6 Mbit/s. Download was not capped (338, then 250 Mbit/s). A value of 0 removed the cap (56 Mbit/s). | The slider is labelled "upload limit". |
| Q15 | **Yes, for Home and for Pro.** | With the network on, setup skipped OOBE and asked for no Microsoft account. It created two local accounts and logged on `earner` by itself. PowerShell Direct works. `Panther\unattend-original.xml` keeps both passwords in **plain text**. | Delete both Panther files. Machine settings go in `specialize`, not in `FirstLogonCommands`. |
| Q16 | **Not run.** | The lab host offers S3 sleep only. Hibernation is off, and the hypervisor refuses hybrid sleep. | **Needs a host sleep.** |
| Q17 | Markers found for both apps, and a red state seen for both. | Wipter: `%APPDATA%\wipter-app\secure-credentials.json` holds the key `com.wipter.auth.production:<user>` only when signed in. `logs\main.log` shows the connection state. PassiveApp: `%APPDATA%\passiveapp-desktop\data.json` holds `token` only when signed in. | A later `guest.windows.signed_in` block gets these markers (2b). `signin` stays `console` or `none`. The reader returns presence and state only. |

---

## 4. Per question

Every command below ran on the lab host as an elevated administrator, unless it says "guest". The guest's commands ran over PowerShell Direct (`Invoke-Command -VMName <vm> -Credential <cpadmin>`). The probe opens a TCP connection with a 3-second timeout and sends a random token. The canary on the host records what arrives.

### Q1. Static neighbour entries, no Allow rule

```powershell
# L2: the deny list from the design, with no Allow for the gateway
Add-VMNetworkAdapterAcl -VMName $vm -RemoteIPAddress <range> -Direction Both -Action Deny   # once per range
# guest
New-NetNeighbor -InterfaceIndex <ifIndex> -IPAddress <gateway> -LinkLayerAddress <host vEthernet MAC> -State Permanent
# host
New-NetNeighbor -InterfaceAlias 'vEthernet (<switch>)' -IPAddress <guest IP> -LinkLayerAddress <guest MAC> -State Permanent
```

| Step | Layout | Result |
|---|---|---|
| S0, red | Gateway Allow /32, L3 off | Internet OK. Gateway canary: **arrived** with the right token. |
| S1 | No Allow, no static entries | Internet dead. Guest neighbour for the gateway: `Incomplete`. This is the ARP trap, seen again. |
| S2 | Guest entry only | Internet dead. Host neighbour for the guest: `Incomplete`. |
| S3, green | Both entries, L3 **off** | Internet OK. The egress equals the host's public IP. The gateway canary, gateway ports 135 and 445, the host's LAN address on port 22 and a LAN server on port 443 all **time out**. Ping to the gateway fails. Nothing arrived. |
| S4 | Both entries, L3 on | Internet OK. Nothing arrived. |
| S5 | Gateway Allow, L3 on (L3 alone) | Nothing arrived. |
| S6, red 4 | Gateway Allow, L3 off | **Arrived.** |
| S7, green | Both entries, no Allow, L3 on | Internet OK. Nothing arrived. |

`New-NetNeighbor` writes to `PersistentStore` by default, on the host and in the guest. WinNAT forwarded with `Forwarding` still `Disabled` on the `vEthernet`.

**Answer:** yes. **Design:** we take fix 1 from the design's "gateway ARP trap" section.

- The answer file sets the guest entry. Setup sets the host entry.
- The guard compares both entries and the host `vEthernet` MAC.
- If Repair rebuilds the switch, it rewrites the guest entry over PowerShell Direct.
- A guest that deletes its entry only cuts itself off.

### Q2. Extended ACLs as the fallback

```powershell
Add-VMNetworkAdapterExtendedAcl -VMNetworkAdapter $a -Action Deny -Direction Outbound -RemoteIPAddress <gateway>/32 -Protocol TCP -Weight 10
Add-VMNetworkAdapterExtendedAcl -VMNetworkAdapter $a -Action Deny -Direction Outbound -RemoteIPAddress <gateway>/32 -Protocol UDP -Weight 11
```

- **Red:** with the gateway Allow, no extended ACL and L3 off, the TCP and UDP canaries both received the token.
- **Green:** with the two extended Denies, ARP resolved (`Reachable`), and the internet worked. The TCP canary on port 135 and the UDP canary got nothing. Ping to the gateway still answered.
- Any protocol: an extended Deny with no `-Protocol` also left ARP `Reachable` and the internet working.
- Mixing: an extended Allow (TCP 443 to one LAN server, weight 100) did not open the simple Deny of 192.168/16.

**Answer:** extended ACLs work on client Windows 11 26H2. They filter IP only and never touch ARP. A Deny wins in both directions between the two ACL kinds. **Design:** this is the documented fallback. Q1 is simpler and needs no Allow rule, so we do not use it.

### Q3. Host firewall and IPv6

- Inbound Block on the `vEthernet` (L3): WinNAT kept forwarding in every step of Q1 with L3 on.
- Outbound rules: `New-NetFirewallRule -Direction Outbound -Action Block -RemoteAddress 1.0.0.1`.
  - Positive control: the host's own connection to 1.0.0.1:443 was blocked.
  - The guest's connection to the same address was **open**.
  - The same held for a LAN server opened at L2. An outbound Block rule did not stop the guest. An inbound Block on the `vEthernet` did not stop it either. Only the host's own LAN address stayed closed.
- IPv6:
  - The guest has only a link-local address and no `::/0` route. The lab line has no global IPv6.
  - Red: with the `::/0` Deny removed, L3 off and `ms_tcpip6` bound on the `vEthernet`, the guest **reached a canary on the host's link-local address**.
  - Green: L3 alone stopped it. The `::/0` Deny alone stopped it.
  - **Unbinding `ms_tcpip6` alone is not proven.** Its only run was broken. The target address kept the host's own scope ID, and the guest's scope ID was added after it. So every probe in that run failed with "host not found" or "no route", the red step included. A run with the fixed target went red, but it did not repeat the unbinding case.

**Answer:** L3 keeps forwarding. The host firewall gives no second filter for forwarded traffic. WinNAT alone does not close IPv6, because the link-local path to the host is real. **Design:**

- L2 is the only filter for everything beyond the host. L3 guards the host's own addresses.
- The proof gains an IPv6 red step. It drops `::/0` and binds `ms_tcpip6` for a moment, then sees the link-local canary arrive. Green is the normal layout.
- `ms_tcpip6` stays unbound in the normal layout. Its own red/green test is open (§6). Until it passes, the UI credits the link-local path to L2 and L3 only.
- We could not see what WinNAT does with global IPv6, because the lab line has none.

### Q4. Counting blocked traffic

| Test | Result |
|---|---|
| Meter `0.0.0.0/1` + `128.0.0.0/1`, Both, 20 MB download | The counters lagged and missed the download |
| Meter on an allowed /32, 10 MB of UDP sent | 11 MB after 10 s, then flat for 80 s (positive control) |
| Meter on `192.168.0.0/16` beside the Deny, 10 MB of UDP to a denied address | **0 MB** for 90 s |
| Meter on `10.0.0.0/8`, 200 TCP connects to a denied address | **0 MB** |
| Meter on a /24 inside the 192.168/16 Deny | The Deny still held, and the counter read 0 MB |
| Drop counter, 1000 UDP to a denied address | `Outgoing FailedSecurityPolicy` **+1000** |
| Drop counter, 37 TCP connects to a denied address | **+37** |
| Drop counter, 500 UDP to an allowed address | **+0** |

So Meter ACLs do not count drops. The drop counter does.

**How we read it, and what is not proven.** The +1000, +37 and +0 came from `Get-Counter` with the counter path in the host's language (Spanish). A path in the host's language breaks on every other locale. The same counter is exposed as the performance class `Win32_PerfRawData_NvspNicDropReasonsStats_HyperVVirtualNetworkAdapterDropReasons`, with the properties `OutgoingFailedSecurityPolicy` and `IncomingFailedSecurityPolicy`. On the lab host, a query of that class returned the class but **no instances**. We have no saved run that reads the counter through it. So:

- The planned read is that WMI class.
- The fallback is PDH with the English counter names (`PdhAddEnglishCounterW`), which works on any locale.
- The instance name holds the VM name and the localized adapter name. The reader matches it on the VM name and the adapter ID.
- Before the tile ships, a live run must show the chosen read go up by the number of denied packets (red) and stay flat for allowed ones (green).

pktmon logs the same drops at the VM's port as `DropReason: Failed security policy`.

**Answer:** no. **Design:**

- Drop the Meter rows.
- The tile reads `FailedSecurityPolicy` (outgoing and incoming) every 60 s and shows the difference: "Apps tried to reach your network: 37 packets blocked".
- It counts packets, not attempts, because one TCP connect is 1 to 3 SYNs.
- The counter is cumulative. The reader stores the last value and treats a lower value as a reset.
- The green step of the proof becomes "no arrival, and `FailedSecurityPolicy` rose".

### Q5. Persistence

- Tested: the sorted ACL list (30 rows) was identical after `Stop-VM`. `Get-NetNeighbor -PolicyStore PersistentStore` lists the host entry. `Get-NetFirewallRule -PolicyStore PersistentStore` lists the L3 rule. The NetNat object persists. The guest's static ARP entry survived four guest reboots: the internet still worked in the Q1 layout, which has no Allow rule.
- Not tested: a host reboot and a feature update. The reboot is scripted and waits for a lab window (§6). A feature update cannot be run on demand. The re-proof after each change of the host's Windows build stays in the design.

### Q6. Hyper-V Administrators without elevation

A throwaway local user ran a probe through a scheduled task, which gives a fresh logon each time. For the test only, the user got "Log on as a batch job", and we removed the right after. The probe ran `Get-VM`, `Disconnect-VMNetworkAdapter` with `Connect-VMNetworkAdapter`, and PowerShell Direct as `cpadmin`.

| Case | Token | Result |
|---|---|---|
| R1: standard user, not in the group | medium integrity | `Get-VM` and disconnect fail with "You do not have the required permission". PowerShell Direct fails. |
| G1: standard user in Hyper-V Administrators | medium integrity, `S-1-5-32-578` | `Get-VM` shows **every VM on the host**. Disconnect and connect work. PowerShell Direct works. |

- Not testable this way: a batch logon of an administrator gets a full (high-integrity) token. So the "admin, not elevated" case needs an interactive desktop.
- `vmconnect`: not tested, because it needs a desktop session.

**Answer:** yes, for a standard account, after one new logon. **Design:**

- The setup text names the real scope: the group controls **every** VM on the PC.
- The live test adds the interactive cases: an admin account that is not elevated, and `vmconnect`.

### Q7. NetNat beside Docker Desktop and WSL2

- `New-NetNat` for our /29 succeeded while another NetNat (a /30) already existed. Both stayed active.
- Both forwarded traffic. Our guest and a guest behind the other NetNat both reached the internet.
- A WSL2 distro (Alpine minirootfs, imported and removed again) ran in NAT mode. It got its own network ("WSL (Hyper-V firewall)", ICS type) and reached the internet. Both NetNats were still active.
- Another account's WSL in mirrored mode ran throughout. It mirrored our `vEthernet` into its VM: an "FSE Switch (vEthernet (<our switch>))" network appeared. The L2 Deny on the gateway covers this.
- Docker Desktop 4.73.1 is installed, but we did not start its engine, because it needs an interactive session.

**Answer:** yes, on build 26300, with prefixes that do not overlap. **Design:** the preflight no longer blocks on "another NetNat". It blocks only a NetNat whose prefix overlaps ours, and it picks a free /29. The live test adds a run with Docker Desktop started.

### Q8. The guard starts the VM

The prototype guard ran as a SYSTEM scheduled task with an `AtStartup` trigger. We started it by hand with the VM off:

```
guard start; interactive users: 2
disconnected
match; connected
Start-VM issued
```

- The guest logged on `earner` by itself, and PassiveApp started from its Run key. The egress was the host's public IP.
- **Red:** we removed one Deny ACL and ran the guard. It logged `MISMATCH: acl; staying disconnected`, and the adapter had no switch.
- **Green:** we restored the Deny and ran the guard. The adapter was connected again.
- `Disconnect-VMNetworkAdapter` clears `SwitchName`, so the reconnect must name the switch.

**Answer:** partly. The logic works. A host reboot must show whether the boot trigger starts the VM before anyone signs in. The run is scripted and waits (§6).

### Q9. Install mode

1. Install mode set autologon to `cpadmin` (`DefaultUserName` plus the LSA secret `DefaultPassword`) and restarted the guest.
2. As `cpadmin`, elevated, on `cpadmin`'s desktop:
   - PassiveApp 1.0.15, `/S`, went to `C:\Users\cpadmin\AppData\Local\Programs\PassiveApp Desktop`, with an HKCU uninstall entry.
   - Wipter 1.25.1039, `/S /allusers`, went to `C:\Program Files\Wipter`, with an HKLM uninstall entry.
3. Autologon went back to `earner`, and the guest restarted. **No app ran.** `earner` cannot use PassiveApp, because it sits in `cpadmin`'s profile. Wipter had no Run key for `earner`.
4. As `earner`, without elevation:
   - The PassiveApp installer succeeded into `earner`'s profile.
   - Wipter from Program Files started.
   - After its first start, PassiveApp wrote its HKCU Run key and came back after a guest reboot.
   - Wipter wrote no Run key before sign-in. A guest where Wipter is signed in has one.

The installer files matched the expected SHA-256 values. Their signers were valid: ByteCrafted LLC for Wipter and Rngde for PassiveApp.

**Answer:** no. **Design:**

- Install mode no longer switches the console to `cpadmin`. It keeps `earner` at the console.
- Most earner installers install per user and need no admin.
- An installer that needs admin raises UAC in `earner`'s session. The user types `cpadmin`'s credentials into the prompt. CashPilot can type them with "Type clipboard text".
- When install mode ends, CashPilot rotates `cpadmin`'s password as before.
- Every app first starts in `earner`'s session, so it registers its own autostart there.
- The design's fallback (one admin autologon account) is not needed.

### Q10. PowerShell Direct with KVP off

- KVP, Guest Service Interface and VSS were off. Heartbeat, Shutdown and Time Sync were on. In that state, `Invoke-Command -VMName` worked. `vmickvpexchange` was stopped in the guest, and the host read 0 KVP items.
- **Red:** we set `vmicvmsession` to disabled and stopped it in the guest. PowerShell Direct then failed with `PSSessionStateBroken`. After we set the service back to manual, it worked. Stopping the service alone is not a red step, because the service starts again by trigger.
- `Copy-Item -ToSession` over PowerShell Direct copied the installers into the guest with the Guest Service Interface off.
- KVP leak: on the guest that had KVP on during setup, the guest's `HKLM\SOFTWARE\Microsoft\Virtual Machine\Guest\Parameters` holds `HostName`, `PhysicalHostName` and `PhysicalHostNameFullyQualified` (the host's computer name), plus `VirtualMachineName`. On the two guests where KVP was off before the first start, the key does not exist.
- Localization trap: on a Spanish host the services have Spanish names ("Latido", "Intercambio de pares clave-valor" and so on). The IDs are stable:

| Service | ID |
|---|---|
| Guest Service Interface | `6C09BB55-D683-4DA0-8931-C9BF705F6480` |
| Heartbeat | `84EAAE65-2F2E-45F5-9BB5-0E857DC8EB47` |
| Key-Value Pair Exchange | `2A34B1C2-FD73-4043-8A5B-DD2159BC743F` |
| Shutdown | `9F8233AC-BE49-4C79-8EE3-E7E1985B2077` |
| Time Synchronization | `2497F4DE-E9FA-4204-80E4-4B75C46419C0` |
| VSS | `5CED1297-4598-4915-A5FC-AD21BB4D02A4` |

**Answer:** yes. **Design:**

- Setup disables KVP, Guest Service Interface and VSS **before the first `Start-VM`**.
- Setup and the guard select integration services by the ID suffix of `Get-VMIntegrationService`. The same rule applies to every localized name: performance counters, group names and adapter names.

### Q11. pktmon after NAT

```powershell
pktmon filter add R2 -i <router IP> -t TCP -p <port>
pktmon filter add R3 -i <own public IP> -t TCP -p <port>
pktmon start --capture --comp nics --pkt-size 128 -f red.etl   # wait 3 s before probing
pktmon stop; pktmon etl2txt red.etl -o red.txt
```

- Host control: the host's own probes were captured, so the filters work. The router probe left through the Tailscale adapter, not the physical NIC.
- **Red:** we added an L2 Allow /32 for the router and for the own public IP.
  - For the own public IP, we had to remove the Deny /32 first. An Allow and a Deny with the exact same prefix cannot coexist (`0x800700B7`, "already exists").
  - The guest's SYN to the router showed at the VM port. It was NATed into the **Tailscale tunnel** with the host's tailnet address. The router answered with a RST through the tailnet.
  - The guest's SYN to the own public IP showed on the **physical NIC**, with the host's LAN address as the source and the destination and port intact.
- **Green:** we removed the Allows and restored the Deny. pktmon logged both SYNs as dropped at the VM port, with `DropReason: Failed security policy`. Nothing showed on any NIC.
- The cause of the Tailscale path: the host accepts a tailnet subnet route for its own LAN (`192.168.x.0/24` through Tailscale, metric 0). It beats the on-link route, which has metric 256.

**Answer:** yes, filtered on destination and port, but only on the adapter that the host routes the target through. **Design:**

- The proof captures on all NICs (`--comp nics`). It counts a post-NAT packet on any adapter as an arrival.
- It waits 3 s after `pktmon start`, because the first SYN can be missed otherwise.
- It reads pktmon's drop records at the VM port as green evidence.
- The red step for the own public IP removes and restores the Deny.
- The "Routes" rule must expect the home LAN itself to route through a tunnel. That traffic is still inside the denied private ranges.

### Q12. VPN adapters and the "physical" test

| Adapter | `-Physical` | ConnectorPresent | Virtual |
|---|---|---|---|
| Tailscale Tunnel | no | False | True |
| WireGuard Tunnel (a test tunnel to a TEST-NET peer, removed after) | no | False | True |
| TAP-Windows Adapter V9 (OpenVPN) | no | False | True |
| `vEthernet (<switch>)`, the host adapter of an External switch over the physical NIC | **no** | False | True |
| The host's wired and Wi-Fi NICs | yes | True | False |

The lab host's **default route goes through that `vEthernet`**. The design's first rule was "the default route must leave through `Get-NetAdapter -Physical`". It would disconnect the VM forever on this host, and on any PC that bridges its NIC to an External vSwitch.

**Answer:** the three VPN types tested show as non-physical. Work VPN clients (Cisco, GlobalProtect, FortiClient) are not tested. **Design:** an adapter counts as physical when it is in `Get-NetAdapter -Physical`, **or** when it is the `vEthernet` of a `Get-VMSwitch -SwitchType External` whose `NetAdapterInterfaceDescription` is a physical adapter. The live test adds one work VPN client when we can get one.

### Q13. hv_sock listeners on the host

The test listener was C#, loaded with `Add-Type`. The socket was `AF_HYPERV` (34), and `SOCKADDR_HV` was family, reserved, VmId and ServiceId. The guest connected to `HV_GUID_PARENT` (`a42e7cda-d03f-480c-9cc2-a4de20abb878`) with a new service GUID. Two runs used two different service GUIDs.

| Run | Listener bound to | Registered in `GuestCommunicationServices` | Guest result |
|---|---|---|---|
| 1, red | wildcard (all zeros) | yes | **connected**. The host received the token from the guest's VM ID. |
| 1 | wildcard | no (entry deleted) | refused (`10061`) |
| 1 | the guest's own VM ID | yes, re-created just before | refused (`10061`) |
| 1 | another VM's ID | yes | refused (`10061`) |
| 1, green | none, entry deleted | no | refused, for our GUID and for both registered services, `999E53D4-…` and `A5201C21-…` (VM Session Service) |
| 2, red | `HV_GUID_CHILDREN` | yes | **connected** |
| 2, red | the guest's own VM ID | yes | **connected** |

- With Docker Desktop installed (not running) and WSL2 running, `GuestCommunicationServices` held only Microsoft's two VM Session Service entries.
- Neither entry accepts a connection from the guest.
- We deleted every test entry at the end.

**What this proves and what it does not.**

- A wildcard listener needs its registry entry. Without it, the guest is refused.
- A listener bound to `HV_GUID_CHILDREN` or to the guest's own VM ID accepts the guest when registered. We never ran those two bindings without the entry. So "unregistered means refused" is not proven for them.
- The same registered, own-VM-ID binding was refused in run 1 and connected in run 2. In run 1 the entry had just been created. So a new entry can take a moment to apply.
- A listener bound to another VM's ID refused our guest.

**Answer:** no host listener was reachable. **Design:**

- The guard compares the registered list on every pass. Any change is drift.
- That compare is a proven signal for wildcard listeners only. The narrow bindings get their own red/green test (§6) before the UI calls VMBus closed by test.
- The live test repeats Q13 with Docker Desktop running.

### Q14. Bandwidth cap

```powershell
Set-VMNetworkAdapter -VMName $vm -MaximumBandwidth 10000000   # bits per second; 0 removes it
```

| Cap | Download | Upload |
|---|---|---|
| none | 338.4 Mbit/s | 62.1 Mbit/s |
| 10 Mbit/s | 250.4 Mbit/s | **9.6 Mbit/s** |
| removed (0) | 225.9 Mbit/s | 55.9 Mbit/s |

The guest measured with `curl.exe` against `speed.cloudflare.com`: a 30 MB download and a 12 MB upload.

**Answer:** yes. On client Windows 11 with an Internal switch, the cap limits traffic **leaving** the VM. **Design:** the slider is an upload limit and says so. The tile shows the VM's upload, measured on the host.

### Q15. Home and Pro from an answer file

The answer file sets:

- `/IMAGE/NAME` to "Windows 11 Home" or "Windows 11 Pro".
- Microsoft's generic install key for that edition in `UserData/ProductKey`. A generic key installs the edition but does not activate it.
- `cpadmin` and `earner` under `LocalAccounts`, and autologon for `earner`.
- The `OOBE` hide flags.
- The static IP and DNS in `specialize`.
- `powercfg` and the lock-screen policy in `specialize` `RunSynchronous`.

The network was connected through the fence during setup.

| | Home (`EditionID Core`) | Pro (`Professional`) |
|---|---|---|
| OOBE | skipped, `IMAGE_STATE_COMPLETE`, no Microsoft account screen | same |
| Local accounts | `cpadmin`, `earner` (both `Local`) | same |
| Autologon | `earner` at the console | same |
| PowerShell Direct as `cpadmin` | works | works |
| `Panther\unattend.xml` | passwords masked (`*SENSITIVE*DATA*DELETED*`) | same |
| `Panther\unattend-original.xml` | **both passwords in plain text** | same |
| Any other file in `Panther` or `Sysprep` with a password | not checked | none |
| KVP data in the guest (KVP off before the first boot) | absent | absent |
| Licence | `LicenseStatus 5` (no key), as expected | same |

- The design's first PowerShell Direct session, run on Home, set new passwords for both accounts and rewrote the autologon LSA secret. It deleted both Panther files, removed both DVD drives and deleted the answer ISO. After a reboot, `earner` logged on by itself, and PowerShell Direct worked with the new `cpadmin` password.
- `FirstLogonCommands` did not run for the standard autologon user: its marker file was missing on all three guests. `specialize` `RunSynchronous` did run as SYSTEM: the lock-screen policy was set, and hibernation was off.

**Answer:** yes, for both. **Design:**

- The wizard accepts a Home or a Pro key.
- Machine settings, including the guest's static ARP entry, go in `specialize` `RunSynchronous`, never in `FirstLogonCommands`.
- The first session deletes `unattend.xml` **and** `unattend-original.xml`.
- The answer file uses `PlainText=false` anyway. It only base64-encodes the password, so the deletion is what protects it.

### Q16. Host sleep

**Not run.** `powercfg /a` on the lab host lists S3 only. Hibernation is not on, and hybrid sleep is not available ("the hypervisor does not support this standby state"). The run is scripted (§6):

- It registers a resume-triggered SYSTEM task (System log, `Microsoft-Windows-Power-Troubleshooter`, event 1) and a wake timer.
- It captures the guest's port with pktmon across the sleep.
- It compares the first guest packet after resume with the guard's disconnect time.

Hibernation stays untested unless we turn it on for the test.

### Q17. Sign-in markers

| App | Signed in | Never signed in (red) | Read only |
|---|---|---|---|
| Wipter 1.25.1039 | `%APPDATA%\wipter-app\secure-credentials.json` exists with the key `com.wipter.auth.production:<Windows user>`. `logs\main.log` has `User … successfully logged in!` and later `Receiving state update from front:  Online` | the file is absent, and `main.log` has `No access token detected, redirecting to sign in` | the file's presence and the key name, and the last `state update from front: <State>` word |
| PassiveApp Desktop 1.0.15 | `%APPDATA%\passiveapp-desktop\data.json` has a non-empty `token` and `earningEnabled: true` | `data.json` has only `earningEnabled` | whether `token` is present and non-empty, never its value |

`main.log` also contains the account's email. `secure-credentials.json` holds an encrypted token. The reader returns booleans and the state word only, never a log line. The signed-in evidence came from a guest where both apps are signed in. The red evidence came from fresh installs in a throwaway guest, with no sign-in.

---

## 5. Changes to the design

1. **ARP:** static neighbour entries on both sides, with no gateway Allow (Q1). The guest entry goes in `specialize`. The host `vEthernet` MAC goes into `desired.json`.
2. **Blocked-packet counters:** the Meter ACLs are removed. The tile reads `OutgoingFailedSecurityPolicy` and `IncomingFailedSecurityPolicy`. The planned read is the class `Win32_PerfRawData_NvspNicDropReasonsStats_HyperVVirtualNetworkAdapterDropReasons`, with PDH English counter names as the fallback. Neither read is proven yet (Q4). The tile says "packets".
3. **The proof:**
   - pktmon captures on all NICs (`--comp nics`) and waits 3 s first (Q11).
   - Red 3 swaps the own-IP Deny for an Allow and back (Q11).
   - A new IPv6 red step targets the host's link-local canary (Q3).
   - Green requires no arrival **and** a rising `FailedSecurityPolicy` (Q4).
4. **The physical-adapter test** accepts the host `vEthernet` of an External switch that is bound to a physical NIC (Q12).
5. **NetNat preflight:** it blocks only on an overlapping prefix (Q7).
6. **Install mode:** it stays in `earner`'s session. Admin installers get UAC with `cpadmin`'s credentials (Q9).
7. **Integration services:** KVP, Guest Service Interface and VSS go off before the first boot. Every integration service is selected by ID. Every localized name is read through a stable ID or an English name, never through text in the host's language (Q10, Q4).
8. **The first PowerShell Direct session:** it deletes `Panther\unattend.xml` and `Panther\unattend-original.xml`, rotates both passwords, rewrites the autologon LSA secret, and removes the DVD drives and the answer ISO (Q15).
9. **Privileges text:** "full control of Hyper-V and of every VM on this PC" (Q6).
10. **The bandwidth slider** is an upload limit (Q14).
11. **The guard:** `desired.json` holds the switch name, because `Disconnect-VMNetworkAdapter` clears it (Q8).
12. **Removal:** the vTPM's `UntrustedGuardian` already existed on the lab host, created by another VM. Setup reused it. This confirms the rule "remove it only if setup created it".
13. **The release workflow:** no change. It is already true on `main`. The Windows signing step in `.github/workflows/desktop-release.yml` exits 0 when `WINDOWS_SIGN_CERT` is empty, so the release goes on unsigned (decision 3).
14. **The catalog:** a Desktop-only `guest.windows` block marks the apps that may run in the VM. It names the process, the expected publisher, the official download page and how the user signs in. A service with `container_prohibited` never gets one.

## 6. Still open

| Item | Why open | What closes it |
|---|---|---|
| Q5, the host reboot part | A reboot also restarts the other VMs on the lab host | One scripted reboot. The before and after snapshots compare ACLs, NAT, the firewall group, a persistent neighbour entry and the bindings. |
| Q8 | Same reboot | The same run. The guard task logs the boot time and the first sign-in, and we check the guest's apps. |
| Q16 | A sleep pauses the other VMs | One scripted S3 sleep with a wake timer |
| Q5, the feature update part | Cannot be run on demand | The re-proof after each change of the host's Windows build stays |
| Q3, unbinding `ms_tcpip6` alone | Its only run was broken (Q3) | A red/green run: record the host's link-local address before the unbinding, add a guest neighbour entry, and require pktmon to show the frames delivered with no arrival |
| Q4, the locale-free drop-counter read | The WMI class returned no instances. Only the localized `Get-Counter` path was seen counting. | A live run of the WMI read, and of the PDH English-name fallback, red then green |
| Q13, `HV_GUID_CHILDREN` and own-VM-ID listeners without registration | Never run unregistered | Per binding: register and connect (red), delete the entry and be refused (green), register again and connect |
| Q6, the admin account that is not elevated, and `vmconnect` | Needs an interactive desktop | The manual live test |
| Q7 and Q13 with Docker Desktop running | Its engine needs an interactive session | The manual live test |
| Q12, work VPN clients | No client in the lab | Test when one is available |
| Global IPv6 through WinNAT | The lab line has no IPv6 | A test on a dual-stack line. The `::/0` Deny closes it today. |
| The cost of a code-signing certificate | Not part of this lab | The later signing work |
