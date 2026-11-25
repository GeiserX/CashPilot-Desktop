# Secure VM live-run checklist

Use a Windows 11 Pro Hyper-V host with HGS tooling and NTFS. Run the candidate against throwaway resources.

## Keep `bunker` read-only

- Use read-only queries for `bunker` and its resources. Never create an object named `bunker`.
- Never rename, start, stop, save, checkpoint, move or reconfigure `bunker`. Leave its switch, adapters, disks, guardian and checkpoints untouched.
- Leave all guardians in place. Never swap a guardian or replace `UntrustedGuardian`. If Setup would create it, stop and report.
- Keep the host running. Reboot and sleep tests require separate owner approval and guards. Never roll back or restore a host image.
- Check the owner's RDP session and its network path before each stage. Confirm the session ID and the interface that carries its traffic. Stop and report if any step could disrupt that session.
- Stop and report any change to the `bunker` inventory, any failed read or any ambiguous identity. Take read-only evidence after a stop.
- Before CashPilot changes an existing object, match its live identity to the run's journal. Stop if an action would target an unrecorded object.
- For manual fixture changes and cleanup, match the live identity to the fixture ledger. Never select a mutation target by name, MAC address or path pattern alone.

## Record the baseline

1. Record the candidate commit, Windows build and PowerShell versions. Build the product and `winvmlab` binaries for Windows amd64.
2. Save `before.json` before any fixture creation or Setup. Use the inventory below. Keep it in a private evidence directory outside CashPilot's data.
3. Read Enhanced Session policy with `Get-VMHost`. If Setup requires the policy off and it is on, stop and ask the owner. Leave the policy untouched.
4. Check for a real CashPilot install under `C:\ProgramData\CashPilot`. Run product-name Setup only if that directory is free of production data.
5. Confirm that the owner approves the test account and the network paths used by proof. Verify there is enough free RAM and disk space for the guest.

### Read-only inventory

Use `Get-VM -Name bunker` for the first lookup. Require exactly one result. Record its VM ID and use that ID for every later sample.

| Scope | Fields to record |
| --- | --- |
| VM | Record ID, name, state, uptime, sample time in UTC, Notes, generation, version, configuration path, checkpoint type and `AutomaticStartAction`. |
| VM adapters | Use `Get-VMNetworkAdapter` on the VM. Record adapter IDs, MAC addresses, dynamic MAC settings, switch IDs and adapter guard settings. |
| Port ACLs | Use `Get-VMNetworkAdapterAcl` and `Get-VMNetworkAdapterExtendedAcl`. Use `Get-VMSwitchExtensionPortFeature` on each adapter for feature ID `998BEF4A-5D55-492A-9C43-8B2F5EAE9F2B`, including their setting IDs and values. |
| VM and checkpoint media | Use `Get-VMHardDiskDrive` on the VM and on its `Get-VMSnapshot` results. Record each attachment ID, controller and path. Use `Get-VHD` to walk each parent chain to its base. Record each path, disk type, virtual size and file size. Record the file's last write time in UTC. Record the volume serial and NTFS file ID, for example with `fsutil file queryfileid` for the ID. Record DVD IDs and media paths with `Get-VMDvdDrive`. |
| Checkpoints | Record checkpoint IDs, names, types, creation times and parent IDs. |
| VM security | Record `Get-VMSecurity` settings, including TPM, shielding and encryption. Record the length and SHA-256 of the bytes from `Get-VMKeyProtector`. Record the signing and encryption certificate fingerprints of the guardian used by `bunker` and of `UntrustedGuardian`, if present. Keep protector bytes and private keys out of the evidence. |
| Integration services | Use `Get-VMIntegrationService` on the VM. Record service IDs, names, enabled state and status. |
| VM switches and host vNICs | Use `Get-VMSwitch` for the full switch list. Record IDs, names, types, Notes and physical interface GUIDs. For `bunker`'s switch IDs, record the switch ports and any host vNIC's port ID, interface GUID, MAC, addresses, routes and bindings. |
| Host policy and packet monitor | Record `EnableEnhancedSessionMode` from `Get-VMHost`. Save the filter list from `pktmon filter list` and the session configuration from `pktmon status` and `Get-EtwTraceSession -Name PktMon`. Record filter names and clauses, session state and trace path. |
| NAT | Use `Get-NetNat` for the full list. Record names, provider instance IDs, prefixes, settings, static mappings and external addresses. |
| Hyper-V Administrators | Record the member SIDs of local group `S-1-5-32-578`. |
| CashPilot tasks | Record the registered tasks under `\CashPilot\`. Save the task paths, names and registration XML. |
| CashPilot firewall and proof registrations | Record the rules in group `CashPilotVM`, the selected lab groups `cpd-svm-<n>` and their `-proof` groups. Include rule IDs, descriptions and filters. Record the GUIDs and `ElementName` values for proof keys under `HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Virtualization\GuestCommunicationServices`. |

Use the same fields and ordering for every JSON file. Sort arrays by provider ID or by stable field values when the provider has no ID. Serialize selected fields with `ConvertTo-Json -Depth 32`. Compare the JSON with `git diff --no-index -- before.json after.json` or another JSON diff tool.

Save and compare `bunker`'s inventory after each test stage. For a running VM, compare the uptime increase with elapsed time at the provider's reporting precision. Keep both raw uptime readings. Treat any other uptime change or any change to another field as a failure.

## Create the decoys

1. Reserve unused lab instance numbers for collisions, the NAT refusal and successful Setup. Check the host inventory and instance directories for each number. Keep their ledgers separate.
2. Create a powered-off `decoy-persistent` VM with a vNIC on the internal switch `decoy-internal`. Attach its differencing VHDX. Record both the base VHDX and its child.
3. Give the management vNIC on `decoy-internal` a distinct MAC with CashPilot's `00-15-5D` prefix. Check that the MAC is unused. Choose an unused subnet outside host subnets, non-default routes and NAT prefixes.
4. For the collision number, create an internal switch and a VM named `cpd-svm-<collision>`. These are throwaway fixtures. Put a distinct 32-hex `cashpilot-owner=` tag in the switch and VM Notes. Keep the VM off.
5. Create a harmless disabled rule in the `cpd-svm-<collision>` firewall group and another in `cpd-svm-<collision>-proof`. Register a disabled task at `\CashPilot\VMGuard-cpd-svm-<collision>`. Use the decoy tag in their descriptions.
6. Create a test GuestCommunicationServices key with a fresh GUID. Start its `ElementName` with `CashPilot Secure VM proof cpd-svm-<collision>.`
7. Use a separate refusal run for the decoy NAT. Windows WinNAT supports one internal NAT prefix. If the host already has a NAT, leave it in place and report the blocked NAT test. If the NAT list is empty, create `cpd-svm-<nat-collision>` as the decoy NAT on the decoy subnet. Keep its instance free of other name collisions. [WinNAT documentation](https://learn.microsoft.com/en-us/windows-server/virtualization/hyper-v/setup-nat-network)
8. Save `decoys-before.json` before CashPilot touches either lab instance. Record the same fields as the host baseline for each fixture. Include all VM, switch and adapter IDs. Record the file identities and parent chains. Include the NAT provider ID, task path and XML, firewall rule IDs and filters, and registry key GUID.
9. Run Setup for the collision number. Require refusal. Run Remove against the same instance with no ownership journal for the decoys. Require it to report the matching objects and leave them in place. Save and diff the decoy inventory after each command.
10. If you created the decoy NAT, run Setup and Remove for its instance number. Require Setup to refuse the NAT collision. Require Remove to list the NAT. Diff the NAT inventory after both commands. Remove only the decoy NAT by its recorded provider ID at the end of this run.
11. If any NAT remains, stop and report that successful Setup is blocked. Record that NAT coexistence is untested.
12. Save a fresh baseline for the remaining decoys. Keep them in place through the successful Setup, Repair and final Remove. Save and diff their inventory after each operation. Stop and report any changed or missing decoy.

## Use pktmon without disturbing existing captures

1. Check the recorded filters and session again before Setup runs its proof. If someone else's session is active, keep it running. Require the candidate to refuse capture. Stop and report the blocked successful proof.
2. If the baseline has filters, leave them in place. Check that proof either refuses or reports incomplete evidence. Stop and report that the unfiltered proof is blocked.
3. For a negative filter test, add only a uniquely named CashPilot test filter. Record its exact definition. Run this test only with no active session and an empty filter baseline.
4. Stop only a CashPilot test session whose trace path and file ID match the run ledger. Remove only the run's filters. Use a clear-all command only if the baseline was empty and every live filter is recorded for this run. If identity is uncertain, stop and report.
5. Capture the final filter list and session configuration. Diff them against the baseline. Stop and report any difference.
6. Keep trace files private. ETL and pcapng files can contain packet headers from every NIC, including `bunker`'s. Delete the run's trace files by their recorded identities after review. Keep only the proof results and interface counts. [Pktmon filter commands](https://learn.microsoft.com/en-us/windows-server/administration/windows-commands/pktmon-filter)

## Run Setup and prove the fence

1. Use a throwaway bootstrap directory for the wrong-hash, existing-directory, junction and foreign-file cases. Point junctions only at recorded fixture directories. Require refusal in each case.
2. Run [t2-build.ps1](../scripts/winvm-lab/t2-build.ps1) for the successful instance with the Windows ISO. Record the storage volume printed before Setup. The 128 GiB dynamic VHDX and VM configuration belong in the private instance directory.
3. Verify the directory ACLs and pinned ancestors before any credential or VM write. Require access for SYSTEM and Administrators only. Compare volume serial numbers and NTFS file IDs from PowerShell, Go and the journal. Check the directories, VHDX and temporary files.
4. Compare the live VM, guest adapter, switch and management adapter IDs with the journal. Verify the VM and switch owner tags. Match the host vNIC by device ID. Record its MAC, DHCP setting, gateway, neighbour and bindings. Record its routes, DNS and forwarding settings. Compare the live NAT provider ID and prefix with the journal.
5. Inspect the guard task XML and Event Log. Verify the task path, SYSTEM principal, highest privilege, executable and data directory. Check the startup, resume and five-minute triggers in the XML. Require `IgnoreNew` and one live guard. Verify that the guard keeps an unproven VM disconnected.
6. Read the built-in proof with [t2-proof.ps1](../scripts/winvm-lab/t2-proof.ps1). Match the expected NIC IDs and interface indexes to the ETL metadata. Check each export for a valid interface block. [Pktmon capture options](https://learn.microsoft.com/windows-server/administration/windows-commands/pktmon-start)
7. Require a positive witness for each applicable red control. Require zero escapes and the required drop or delivery witness for each green control. Cover LAN, router, public IP, gateway, L2 and L3 alone, IPv6, physical adapters and tunnel adapters. Use only approved test targets. Record why any optional target was absent. Require the functional check to pass with the fence restored.
8. Inject missing, unreadable, empty and header-only exports before proof evaluates the run's capture evidence. For a NIC inventory change, use an isolated test vNIC with no RDP or production traffic. If a fault needs a production object, report the case as blocked. Require capture or export failure to leave the VM unproven even when a canary arrives. Cancel a proof and require a restored fence or a disconnected VM.
9. Register a test hv_sock GUID. Observe the guest probe deliver its token to the host listener. Remove the test registration and keep the listener bound. Require explicit guest refusal for that GUID and each baseline service, with zero accepts after unregistering. Test cleanup with an idle listener, a pending accept and a client that sends no token. Require socket and reader cleanup within two seconds. Check repeated close and verify that the recorded decoy registration remains. [Winsock close reference](https://learn.microsoft.com/en-us/windows/win32/api/winsock/nf-winsock-closesocket)

## Run Repair and Remove

Use `decoy-replacement`, `decoy-port` and `decoy-disk-user` for the temporary fixtures below. Keep their identities in the fixture ledger. Change one condition at a time. Restore the test instance by its recorded identities before the next case. Use a fresh Setup for each case that removes or replaces a recorded object. Reuse the instance number only after its previous resources are gone. Keep the journal intact. If a fixture cannot be verified, stop and report.

Before each Remove, compare the test vNIC's routes, DNS and forwarding settings with Setup. Confirm that no other interface has an address in the test NAT prefix. Stop and report any difference before Remove can affect a dependency.

1. Run Repair with the same successful instance number. Compare the VM, switch, guest adapter, management adapter and disk IDs with the Setup journal. Check that Repair stops the guard before replacement and restarts it after a failed Repair.
2. Use `decoy-replacement` on an isolated decoy switch for the duplicate guest MAC case. Use only recorded test objects for the guest and management adapter replacement cases. Require Connect, Disconnect, Repair, guard and proof to leave the replacement guest adapter alone. Require Repair to leave the replacement management adapter alone. Use the recorded management adapter on an ordinary test instance for separate DHCP and MAC change cases.
3. On the recorded test VM or management vNIC, add independent test addresses, neighbours, bindings, Allow and Deny ACLs and DVD attachments. Include an ACL with the same remote address as an owned row and a different direction or local address. Require Repair to leave each added setting in place and refuse proof.
4. Change the address filter on the recorded firewall rule. Require proof and Remove to preserve it. Use recorded throwaway fixtures for same-name NAT, task, firewall and file replacements. Give the replacements different identities and omit the run's owner token. Include the name-derived NAT ID case and a static mapping on the recorded test NAT. Require Remove to report and keep these objects. If the provider cannot supply a name-derived NAT ID, record that case as blocked. Use the PowerShell tests for the guardian dependency case.
5. Attach the powered-off `decoy-port` VM to the recorded test switch. In a separate case, add a recorded test management port. Run [t2-remove.ps1](../scripts/winvm-lab/t2-remove.ps1) in each case. Require it to report the port and keep the switch, NAT and firewall rule. Check the child exit code and the clean-host line. Use `-AssertClean` only for an instance whose inventory must be empty.
6. Attach a test differencing disk to the powered-off `decoy-disk-user` VM or to a checkpoint of that decoy. Use an alias within the test directory to refer to the recorded test VHDX as its parent. Repeat with an unreadable test chain and a different NTFS file at the test path. Require Remove to keep and report the disk in each case.
7. Compare Hyper-V Administrators membership after Remove and after a retry. Require the original members and the test account added by Setup to remain. Verify the manual removal command reports that test account's SID. Compare Enhanced Session with the baseline.
8. Restore the recorded test configuration. Run Remove twice. Collect the proof reports, task events and file IDs. Verify that the successful instance has no unexpected leftovers.

## Verify cleanup

1. After the final Remove, save `decoys-after.json` and diff it against the relevant decoy baseline. Keep the diffs from Setup and Repair with this result.
2. Remove the test fixtures using the ledger. Re-read each ID before deletion. Remove the scheduled task by its exact path and name after matching its registered XML. Match firewall rules by instance ID, the proof key by GUID and disk files by volume and NTFS file ID. Remove the recorded decoy VMs before deleting their disks. Delete disk children before their parents. Confirm the recorded ports are gone before removing each decoy switch. Stop if any recorded identity or dependency cannot be verified.
3. If Setup added a test account to Hyper-V Administrators, remove only that recorded test grant after verifying its SID and the original membership. Leave all other memberships alone.
4. Verify pktmon cleanup against the recorded state. Save a fresh `after.json` with the same fields as `before.json`. Diff both files. Stop and report any `bunker` or host setting difference. Verify that every decoy ID is absent.
5. Keep credentials, owner tokens and packet traces out of the report. Report each blocked case and retain the redacted inventory diffs with the proof results.

The scope excludes copies made by an administrator who transfers the private owner token or swaps in a byte-identical object between steps.
