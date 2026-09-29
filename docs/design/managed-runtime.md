# Managed Runtime Roadmap

CashPilot Desktop starts with external Docker-compatible runtimes because that gets the product usable quickly. The longer-term goal is a CashPilot-managed runtime appliance so passive-income and DePIN containers run inside an environment we control.

## Provider Interface

The Go backend uses a runtime provider boundary:

- `existing-docker`: connect to Docker Desktop, Docker Engine, Colima/Lima Docker contexts, or compatible Podman API.
- `podman-api`: future provider for Podman-specific behavior.
- `cashpilot-vm-docker`: future VM appliance exposing a private Docker daemon.
- `cashpilot-vm-containerd`: future VM appliance using containerd, nerdctl, and BuildKit.

## Platform Plan

### macOS

Use a Lima-style Linux VM, preferably through Apple Virtualization on supported systems. This gives us a controllable Linux kernel, private daemon socket, predictable volumes, and a resettable disk.

### Windows

Use a WSL2 distro appliance first. Hyper-V can remain a later fallback for machines where WSL2 is not appropriate.

### Linux

Prefer rootless Docker/containerd when the kernel and user namespace setup allow it. Offer rootful opt-in for services that need host networking, capabilities, or privileged mode.

## Security Model

- Keep daemon sockets private to the app/user.
- Do not expose Docker API over broad TCP.
- Mount only explicit per-service directories.
- Surface CPU, RAM, disk, and network usage in the UI.
- Add reset/reclaim-disk flows.
- Maintain a compatibility matrix for host networking, capabilities, privileged mode, and rootless incompatibilities.

## Maintenance Burden

The managed VM is effectively a second product: VM image build, runtime patching, disk upgrades, networking, DNS, proxies, port forwarding, diagnostics, and notarized/signed distribution. It is feasible, but should follow the external-runtime MVP.
