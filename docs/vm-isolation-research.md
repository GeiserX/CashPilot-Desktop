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
