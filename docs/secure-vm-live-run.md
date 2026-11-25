# Secure VM live-run checklist

Run this checklist on a disposable Windows Hyper-V host with HGS tooling and NTFS. Save a rollback snapshot. Keep credentials and owner tokens out of logs.

## Prepare the host

1. Record the candidate commit, Windows build and PowerShell versions. Build the product and `winvmlab` binaries for Windows amd64.
2. Create an unrelated VM named `bunker` with its own adapter, switch and disk. Include running and stopped VMs and a checkpoint.
3. Save the VM IDs, adapter IDs, media and switch ports. Record host addresses, neighbours, bindings, NAT and the HGS certificate fingerprint. Record the group SIDs, Enhanced Session policy, tasks, firewall rules, registry keys and pktmon state.
4. Run the elevated bootstrap with a wrong hash. Repeat with pre-existing directories, junctions and foreign files. Confirm that it refuses each case.
5. Turn off Enhanced Session in Hyper-V Settings before product Setup. First try Setup with Enhanced Session on. Confirm that Setup reports the prerequisite and leaves the setting on.

## Set up the VM

1. Run [`t2-build.ps1`](../scripts/winvm-lab/t2-build.ps1) with a disposable instance number and the Windows ISO. Record the storage volume that the wrapper prints before Setup. The 128 GiB dynamic disk and VM configuration go in a private directory under the instance data directory.
2. Verify the directory ACLs and pinned ancestors before any credential or VM write. Only SYSTEM and Administrators must have access. Compare volume serial numbers and NTFS file IDs from PowerShell, Go and the journal. Check the directories, VHDX and temporary files.
3. Verify the recorded VM, guest adapter, switch and management adapter IDs. Check the live VM and switch owner tags. Match the management adapter to its Windows device identity. Record its MAC, DHCP setting, gateway, neighbour and bindings.
4. Compare the NAT provider ID and prefix with the Setup journal. Record the HGS certificate fingerprint and Hyper-V Administrators membership. If WinNAT allows only one NAT, test the foreign NAT collision in a separate run. Require refusal and verify that the original NAT remains. [WinNAT documentation](https://learn.microsoft.com/en-us/windows-server/virtualization/hyper-v/setup-nat-network)
5. Inspect the guard task XML and Event Log. Check the path under `\CashPilot\`, the SYSTEM principal, highest privilege, executable and data directory. Check startup, resume and five-minute triggers. Require `IgnoreNew` and one live guard. Check that Setup stops the guard before replacement and restarts it after a failed Repair. Confirm that the guard keeps an unproven VM disconnected.

## Prove the fence

1. Run the built-in proof and [`t2-proof.ps1`](../scripts/winvm-lab/t2-proof.ps1). Compare the expected NIC IDs and interface indexes with the ETL metadata. Check that each export has a valid interface block. [Pktmon capture options](https://learn.microsoft.com/windows-server/administration/windows-commands/pktmon-start)
2. Require a positive witness for each applicable red control. Require zero escapes and the required drop or delivery witness for each green control. Cover LAN, router, public IP, gateway, L2 and L3 alone, IPv6, physical adapters and tunnel adapters. Record the reason for each absent optional target. Verify that the functional check passes with the fence restored.
3. Repeat with missing, unreadable, empty and header-only exports. Change the NIC inventory between the list and the trace. Confirm that an export or capture failure leaves the plan unproven even when a canary arrives.
4. Repeat with pre-existing pktmon filters and a foreign running capture. Check that the proof preserves them. Cancel a proof. Require the restored fence or a disconnected VM.
5. Observe a guest hv_sock probe deliver its token to the host listener with the test GUID registered. Remove the test registration and keep the listener bound. Require an explicit guest refusal for that GUID and each baseline service. Require zero accepts after unregistering. Measure cleanup with an idle listener, a pending accept and a client that sends no token. Require socket and reader cleanup within two seconds. Check repeated close and preservation of foreign keys. [Winsock close reference](https://learn.microsoft.com/en-us/windows/win32/api/winsock/nf-winsock-closesocket)

## Repair and remove

1. Run the build wrapper again with the same instance number. Compare the VM, switch, guest adapter, management adapter and disk IDs with the original journal.
2. Test a same-name VM and switch, a duplicate MAC, and replacement guest and management adapters. Verify that Connect, Disconnect, Repair, guard and proof leave the replacement guest adapter alone. Verify that Repair leaves the replacement management adapter alone. Enable DHCP or change the management adapter MAC and verify that Repair reports the change.
3. Add independent addresses, neighbours, bindings, Allow and Deny ACLs, and DVD attachments. Include an ACL with the same remote address as an owned rule and a different direction or local address. Check that Repair leaves independent settings in place and refuses to prove the altered fence.
4. Change the address filter on the owned firewall rule. Confirm that the proof and Remove preserve the changed rule. Test same-name NAT, guardian, task, firewall and file replacements without the owner token. Include a name-derived NAT ID and another VM that uses HGS. Add a static NAT mapping and check that Remove keeps the NAT.
5. Run [`t2-remove.ps1`](../scripts/winvm-lab/t2-remove.ps1) with another VM or a host management port on the owned switch. Confirm that Remove reports the port and keeps the switch, NAT and firewall rule. Check the child exit code and the clean-host result separately. Use `-AssertClean` only when the inventory must be empty.
6. Attach a differencing disk to an offline VM or checkpoint. Make its parent refer to the recorded VHDX through an alias. Repeat with an unreadable chain and with a different NTFS file at the recorded path. Require Remove to keep and report the disk each time.
7. Check that existing and Setup-added Hyper-V Administrators memberships remain after Remove and after a retry. Verify the manual removal command for the grant that Setup recorded. Change Enhanced Session between Setup and Remove. Check that Remove preserves the selected policy.
8. Restore the fixture to the ordinary owned configuration. Run Remove twice. Reboot and compare the host inventory with the baseline. Save proof reports, task events and file IDs. Verify that the unrelated `bunker` VM and its resources stayed intact.

The scope excludes copies made by an administrator who transfers the private owner token or swaps in a byte-identical object between steps.
