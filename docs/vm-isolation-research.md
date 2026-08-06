# VM isolation and GPU earning: what is actually true

Every service this app runs is a closed-source binary from a company whose
business is selling access to your network connection. On a server that is a
calculated risk. On the laptop you do your banking on, it deserves a boundary.

The obvious objection is that a VM costs you the GPU, and therefore the
GPU-earning services. **That objection is wrong on both platforms, and this page
records the measurements rather than the assumption.**

Everything below was run on real hardware. Nothing here is quoted from vendor
documentation.

## macOS — a Tart VM keeps 92% of the GPU

Measured on an Apple M4 (macOS 26.5.2 host, Tart 2.32.1, guest macOS 15.7.3
arm64), running an identical Metal compute kernel on the host and inside the
guest:

| | Metal device | Throughput |
|---|---|---|
| Host, bare metal | `Apple M4` | **740.9 GFLOPS** |
| Inside the Tart VM | `Apple Paravirtual device` | **683.1 GFLOPS** |

That is **92% of native**, which settles the question that mattered: it is real
GPU execution, not a software fallback. A CPU fallback would be slower by orders
of magnitude, not by 8%.

The guest reports unified memory and a full `1024×1024×1024` max threadgroup,
and a compute kernel dispatched through it returned the correct result. So
Apple's Virtualization.framework paravirtual device is not display-only — it
carries compute.

> This was expected to fail. The working assumption when this was filed was that
> Virtualization.framework provided paravirtualized *graphics* for display only,
> and that GPU compute would be unavailable to a guest — which would have made
> VM isolation and GPU earning mutually exclusive on macOS. Measuring it took
> twenty minutes and produced the opposite answer.

### …but the GPU services still cannot run on macOS, VM or not

All four GPU-earning services in the catalog are **NVIDIA/CUDA-specific**:

| Service | Requires |
|---|---|
| Salad | NVIDIA |
| Nosana | RTX 30/40/50 |
| io.net | 8 GB+ VRAM (CUDA) |
| Vast.ai | NVIDIA |

None of them runs on Apple Silicon **at all** — on the host or in a VM. Metal is
not CUDA.

So the practical conclusion for macOS is the opposite of the worry:

**A Tart VM costs you nothing in GPU earnings, because there are no GPU earnings
to lose on Apple Silicon.** The trade-off everyone expects here does not exist.

## Windows — CUDA reaches the guest, and Salad already does this

Measured on a Windows 11 Pro machine with an **NVIDIA GeForce RTX 4070 Ti
SUPER** (driver 610.62) and WSL2:

```
$ wsl -d Ubuntu -- nvidia-smi --query-gpu=name,driver_version,memory.total --format=csv,noheader
NVIDIA GeForce RTX 4070 Ti SUPER, 610.62, 16376 MiB

$ wsl -d Ubuntu -- ls /usr/lib/wsl/lib/libcuda*
/usr/lib/wsl/lib/libcuda.so
/usr/lib/wsl/lib/libcuda.so.1
/usr/lib/wsl/lib/libcudadebugger.so.1
```

The guest sees the full card and the full 16 GB, and the CUDA driver library is
present. **An isolated Linux guest on Windows can run the CUDA earning
services.**

The strongest evidence is not the benchmark, though. It is this:

```
$ wsl -l -v
  NAME                      STATE       VERSION
* Ubuntu                    Stopped     2
  salad-enterprise-linux    Stopped     2
```

**Salad ships its own WSL2 distribution.** The vendor already runs its GPU
workload inside a VM on Windows and reaches CUDA through it. This is not a
theoretical architecture — it is what the service does today, unprompted.

## What this means for the design

| Platform | GPU in a VM | GPU services usable | So isolation costs |
|---|---|---|---|
| macOS (Tart) | **yes**, ~92% native Metal | no — they need CUDA, which Apple Silicon lacks entirely | **nothing** |
| Windows (WSL2) | **yes**, full CUDA | yes | **nothing** |
| Linux | n/a — containers already namespace-isolate | yes | n/a |

The feature is therefore worth building on its merits, and the objection that
blocked it does not survive contact with a measurement.

Two things follow that are worth stating before anyone designs it:

- **On Windows, some isolation already exists and should not be rebuilt.**
  Docker Desktop runs containers inside a WSL2 VM, and Salad brings its own.
  The useful work there is making that boundary *visible and deliberate*, not
  inventing a second one.
- **On macOS the honest pitch is security, not performance.** A VM buys a real
  boundary between a dozen third-party binaries and the user's keychain, files
  and network. It does not unlock any earning that was unavailable before, and
  it should not be sold as if it does.

## The recommendation

Two options were on the table: **Desktop spawns a VM per service**, or **document
"run Desktop itself inside a VM."** One is a new subsystem to build, ship and
support on three platforms; the other is a documentation page. Nothing measured
here supports either, and that is worth saying plainly.

**Do not build a VM orchestrator. On macOS and Windows, the boundary already
exists — the work is making it visible.**

### Why

Docker Desktop does not run containers on the host. It runs them inside a Linux
VM: a LinuxKit guest under Virtualization.framework on macOS, and a WSL2 guest on
Windows. The section above already records the Windows half of this, and notes
that Salad ships its own WSL2 distribution.

So on the two platforms where anyone actually runs CashPilot Desktop, **every
third-party earning binary is already inside a virtual machine.** A second VM
layer would be rebuilding, at considerable cost, a boundary the platform hands
you for free.

That reframes the whole feature. The gap is not isolation. The gap is that
**nothing tells the user which boundary they are behind**, and the answer differs
per platform in a way nobody could reasonably guess:

| Platform | What actually contains the binaries | Shares the user's kernel? |
|---|---|---|
| macOS + Docker Desktop | a LinuxKit VM | no |
| macOS + Colima / Lima | a Lima VM | no |
| Windows + Docker Desktop | a WSL2 VM | no |
| Windows + Salad | its own WSL2 distribution | no |
| **Linux + Docker/Podman** | **namespaces and cgroups only** | **yes** |

Linux is the one row that is different, and it is different in the direction
that matters: a container escape there lands on the user's own kernel.

### So what to build, in order

1. **Say which boundary is in force.** The runtime detection already knows which
   provider is in use (`internal/runtime`, which distinguishes
   docker-desktop-macos, docker-desktop-windows, colima and the rest). Turning
   that into an honest sentence on the dashboard — "your services run inside a
   Linux VM" versus "your services share this machine's kernel" — is a small
   change on top of code that already exists, and it is the entire remaining
   value on macOS and Windows.

2. **On Linux, document the VM option rather than automating it.** This is the
   only platform where a VM adds a boundary that is not already there. But it is
   also where CashPilot most often runs on a server that does nothing else, which
   is the "calculated risk" this page opens by acknowledging. A page describing
   how to run the whole stack inside a VM — and being honest that it costs
   nothing in GPU earnings, per the measurements above — serves the users who
   want it without committing the project to a hypervisor abstraction.

3. **Only then, if anyone asks for it, automate.** And when they do, it is a
   Linux-only feature, which is a far smaller thing than the cross-platform VM
   manager the bead originally imagined.

### What this avoids

Per-platform hypervisor backends, guest image lifecycle and updates, VM
networking that has to preserve each service's egress IP (which providers cap
per IP — see `CashPilot-5qc`), GPU passthrough plumbing, and a UI for all of it.
That is the two-orders-of-magnitude option, and it would buy almost nothing on
the two platforms where Desktop is actually used.

### The one claim here that was not measured in this session

That Docker Desktop on **macOS** runs containers in a LinuxKit VM. It is its
documented architecture and it matches the Windows behaviour measured above, but
unlike everything else on this page it was not verified on the hardware. Confirm
with `docker info` on a Mac running Docker Desktop before this reasoning is relied
on — it should report a Linux kernel, not Darwin, which settles it in one line.

## Reproducing this

The Metal probe and benchmark are small enough to re-run whenever the host OS,
Tart, or Virtualization.framework changes — and they should be re-run, because
this is exactly the kind of platform behaviour that shifts between releases.

```bash
# macOS: clone a throwaway guest so the base image is untouched
tart clone <base-image> gpu-probe
tart run gpu-probe --no-graphics &
tart ip gpu-probe

# in the guest: create a Metal device, dispatch a compute kernel, compare
# throughput against the same program on the host
swift metalbench.swift
```

```powershell
# Windows: the guest either sees the card or it does not
wsl -d Ubuntu -- nvidia-smi
```
