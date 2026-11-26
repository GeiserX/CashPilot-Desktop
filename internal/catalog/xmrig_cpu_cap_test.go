package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// xmrigWebSHA256 is the sha256 of services/mining/xmrig.yml in the web repository at
// GeiserX/CashPilot 6d22f6a (#850). #847 made "Max CPU threads (%)" a real cap: the
// entry passed only --cpu-max-threads-hint, which XMRig's arm64 build ignores and its
// x86 build rounds, so 17% of a 6-thread machine still started 6 threads. #850 made
// it hold on x86 hosts where XMRig ignores the hint too: no cache size in sysfs, as
// on some virtual machines, or no L3 of at least 2048K. If the web entry changes
// again, vendor it with catalogsync, check that the wrapper tests below still
// describe it, and update this sum in the same PR.
const xmrigWebSHA256 = "d0dc6f1ebc9b09ac5bb6f90cae18d48d6094872f1e954024f67189c6812cd429"

// xmrigBinary is the image's own ENTRYPOINT (images/xmrig/Dockerfile in the web repo),
// which the catalog's wrapper must exec.
const xmrigBinary = "/usr/local/bin/xmrig"

// sysfsCPU is where the wrapper reads cache sizes, the files hwloc 2.x reads
// (topology-linux.c, look_sysfscpu). The tests point it at a stand-in.
const sysfsCPU = "/sys/devices/system/cpu"

// cacheFiles is a stand-in for sysfsCPU: per cache index ("cpu0/cache/index3"),
// the contents of its level and size files. "" leaves that file out.
type cacheFiles map[string][2]string

// cachesShown is what sysfs shows for cpu0 of an i5-14500.
var cachesShown = cacheFiles{
	"cpu0/cache/index0": {"1", "48K"},
	"cpu0/cache/index1": {"1", "32K"},
	"cpu0/cache/index2": {"2", "1280K"},
	"cpu0/cache/index3": {"3", "24576K"},
}

func TestXMRigIsTheWebEntryThatCapsCPUThreads(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "services", "mining", "xmrig.yml"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != xmrigWebSHA256 {
		t.Fatalf("services/mining/xmrig.yml is not the web file at 6d22f6a (sha256 %s, want %s). "+
			"Vendor it with catalogsync and check the CPU cap wrapper before updating the sum", got, xmrigWebSHA256)
	}
}

func TestXMRigWrapperExecsTheImagesOwnEntrypoint(t *testing.T) {
	svc := mustGet(t, realCatalog(t), "xmrig")
	ep := svc.Docker.Entrypoint
	if len(ep) != 4 || ep[0] != "/bin/sh" || ep[1] != "-c" || ep[3] != "--" {
		t.Fatalf("xmrig entrypoint = %q, want /bin/sh -c <script> --", ep)
	}
	if !strings.HasSuffix(strings.TrimSpace(ep[2]), `exec `+xmrigBinary+` "$@"`) {
		t.Errorf("the wrapper does not end by running the image's XMRig: %q", ep[2])
	}
	if n := strings.Count(ep[2], sysfsCPU+"/cpu[0-9]*/cache/index[0-9]*/size"); n != 1 {
		t.Errorf("the wrapper reads the cache sizes hwloc reads %d times, want once: %q", n, ep[2])
	}
	// The hint stays on the command, so XMRig still gets a limit if the wrapper adds nothing.
	if !strings.Contains(svc.Docker.Command, "--cpu-max-threads-hint=${CPU_PERCENT}") {
		t.Errorf("the command lost its hint: %q", svc.Docker.Command)
	}
}

// A new entry on the same image must not bring back the ignored hint on its own.
func TestEveryXMRigImageEntryWithACPUShareHasTheWrapper(t *testing.T) {
	cat := realCatalog(t)
	wrapper := mustGet(t, cat, "xmrig").Docker.Entrypoint
	for _, svc := range cat.List() {
		if !strings.Contains(svc.Docker.Image, "cashpilot-xmrig") {
			continue
		}
		for _, v := range svc.Docker.Env {
			if v.Key == "CPU_PERCENT" && !slices.Equal(svc.Docker.Entrypoint, wrapper) {
				t.Errorf("%s runs the XMRig image with CPU_PERCENT but without the CPU cap wrapper", svc.Slug)
			}
		}
	}
}

var xmrigTestArgs = []string{"--url=pool:1", "--no-color"}

// xmrigArgs runs the catalog's real wrapper with stand-ins: nproc reports cpus ("" =
// nproc fails), uname -m reports machine ("" = it fails), the image's XMRig is a
// script that prints its arguments, and sysfs shows the caches of an i5-14500.
// cpuPercent nil leaves CPU_PERCENT unset.
func xmrigArgs(t *testing.T, entrypoint []string, cpuPercent *string, cpus, machine string) []string {
	t.Helper()
	return xmrigArgsWithCaches(t, entrypoint, cpuPercent, cpus, machine, cachesShown)
}

// xmrigArgsWithCaches is xmrigArgs with sysfs showing caches instead, so no test
// reads the host's own /sys (macOS has none, and each Linux host shows its own).
func xmrigArgsWithCaches(t *testing.T, entrypoint []string, cpuPercent *string, cpus, machine string, caches cacheFiles) []string {
	t.Helper()
	if len(entrypoint) != 4 {
		t.Fatalf("xmrig entrypoint = %q, want the /bin/sh -c <script> -- wrapper", entrypoint)
	}
	if runtime.GOOS == "windows" {
		t.Skip("the wrapper is a POSIX shell script")
	}
	if _, err := os.Stat(entrypoint[0]); err != nil {
		t.Skipf("no %s here: %v", entrypoint[0], err)
	}
	dir := t.TempDir()
	// The stand-in paths are pasted into the script unquoted.
	if strings.Trim(dir, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-") != "" {
		t.Fatalf("temp dir %q has characters the wrapper script cannot take as they are", dir)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	sysfs := filepath.Join(dir, "sysfs-cpu")
	for index, files := range caches {
		if err := os.MkdirAll(filepath.Join(sysfs, index), 0o755); err != nil {
			t.Fatal(err)
		}
		for i, name := range []string{"level", "size"} {
			if files[i] == "" {
				continue
			}
			if err := os.WriteFile(filepath.Join(sysfs, index, name), []byte(files[i]+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	standIn := func(out string) string {
		if out == "" {
			return "exit 1"
		}
		return "echo " + out
	}
	scripts := map[string]string{
		filepath.Join(bin, "nproc"): standIn(cpus),
		filepath.Join(bin, "uname"): standIn(machine),
		filepath.Join(dir, "xmrig"): `printf '%s\n' "$@"`,
	}
	for path, body := range scripts {
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := entrypoint[2]
	if n := strings.Count(script, xmrigBinary); n != 1 {
		t.Fatalf("the wrapper names %s %d times, want once", xmrigBinary, n)
	}
	script = strings.ReplaceAll(strings.Replace(script, xmrigBinary, filepath.Join(dir, "xmrig"), 1), sysfsCPU, sysfs)
	if strings.Contains(script, "/sys/") {
		t.Fatalf("the wrapper reads the host's /sys outside %s: %q", sysfsCPU, script)
	}
	argv := append([]string{entrypoint[1], script}, entrypoint[3:]...)
	argv = append(argv, xmrigTestArgs...)
	cmd := exec.Command(entrypoint[0], argv...)
	cmd.Env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")}
	if cpuPercent != nil {
		cmd.Env = append(cmd.Env, "CPU_PERCENT="+*cpuPercent)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("wrapper failed: %v", err)
	}
	return strings.Split(strings.TrimRight(string(out), "\n"), "\n")
}

// xmrigX86Threads is the thread count XMRig v6.27.0 on x86 derives from a hint,
// before its CPU cache limit (HwlocCpuInfo::threads): round(threads*hint/100) with
// C's round, at least 1. A hint of 100 or more sets no limit.
func xmrigX86Threads(cpus, hint int) int {
	if hint >= 100 {
		return cpus
	}
	return max(int(math.Floor(float64(cpus)*(float64(hint)/100.0)+0.5)), 1)
}

func ptr(s string) *string { return &s }

func TestXMRigCPUShareSetsTheThreadsOffX86(t *testing.T) {
	ep := mustGet(t, realCatalog(t), "xmrig").Docker.Entrypoint
	cases := []struct {
		percent string
		cpus    int
		threads int
	}{
		// XMRig's arm64 build ignores --cpu-max-threads-hint, so 17% of a 6-core
		// machine started all 6 threads before the wrapper set --threads.
		{"17", 6, 1}, {"1", 6, 1}, {"50", 6, 3}, {"99", 6, 5}, {"17", 12, 2}, {"50", 1, 1}, {"33", 64, 21},
	}
	for _, caches := range []cacheFiles{cachesShown, {}} {
		for _, machine := range []string{"aarch64", "armv8l", ""} {
			for _, tc := range cases {
				got := xmrigArgsWithCaches(t, ep, ptr(tc.percent), fmt.Sprint(tc.cpus), machine, caches)
				want := append(slices.Clone(xmrigTestArgs), fmt.Sprintf("--threads=%d", tc.threads))
				if !slices.Equal(got, want) {
					t.Errorf("machine %q, %d cache files, %s%% of %d: args %q, want %q", machine, len(caches), tc.percent, tc.cpus, got, want)
				}
			}
		}
	}
}

// On x86 the hint stays, so XMRig keeps its CPU cache limit, but it lands on the
// rounded-down share. --threads there would start more threads than fit the cache.
func TestXMRigCPUShareOnX86IsAHintThatRoundsToTheCap(t *testing.T) {
	ep := mustGet(t, realCatalog(t), "xmrig").Docker.Entrypoint
	for _, cpus := range []int{1, 2, 6, 12, 16, 24, 32, 64, 99, 100} {
		for _, percent := range []int{1, 17, 25, 33, 50, 75, 99} {
			got := xmrigArgs(t, ep, ptr(fmt.Sprint(percent)), fmt.Sprint(cpus), "x86_64")
			if len(got) != len(xmrigTestArgs)+1 || !slices.Equal(got[:len(xmrigTestArgs)], xmrigTestArgs) {
				t.Fatalf("%d%% of %d: args %q", percent, cpus, got)
			}
			flag, value, _ := strings.Cut(got[len(got)-1], "=")
			var hint int
			if _, err := fmt.Sscan(value, &hint); flag != "--cpu-max-threads-hint" || err != nil {
				t.Fatalf("%d%% of %d: last arg %q, want a corrected hint", percent, cpus, got[len(got)-1])
			}
			// XMRig keeps the last value of a repeated option.
			if threads, want := xmrigX86Threads(cpus, hint), max(cpus*percent/100, 1); threads != want {
				t.Errorf("%d%% of %d: hint %d starts %d threads, want %d", percent, cpus, hint, threads, want)
			}
		}
	}
}

func TestXMRigOnX86AboveAHundredThreadsSetsThreads(t *testing.T) {
	if xmrigX86Threads(101, 50) != 51 {
		t.Fatal("the model of XMRig's rounding changed") // 1% steps cannot say 50 of 101
	}
	ep := mustGet(t, realCatalog(t), "xmrig").Docker.Entrypoint
	got := xmrigArgs(t, ep, ptr("50"), "101", "x86_64")
	if want := append(slices.Clone(xmrigTestArgs), "--threads=50"); !slices.Equal(got, want) {
		t.Errorf("args %q, want %q", got, want)
	}
}

func TestXMRigWrapperOtherwiseRunsXMRigUnchanged(t *testing.T) {
	ep := mustGet(t, realCatalog(t), "xmrig").Docker.Entrypoint
	cases := []struct {
		percent *string
		cpus    string
	}{
		{ptr("100"), "6"}, // no cap: XMRig picks the count itself
		{nil, "6"},
		{ptr(""), "6"},
		{ptr("0"), "6"},
		{ptr("abc"), "6"},
		{ptr("50"), ""}, // nproc failed: run exactly what the image runs
		{ptr("50"), "x"},
	}
	for _, caches := range []cacheFiles{cachesShown, {}} {
		for _, machine := range []string{"aarch64", "x86_64"} {
			for _, tc := range cases {
				if got := xmrigArgsWithCaches(t, ep, tc.percent, tc.cpus, machine, caches); !slices.Equal(got, xmrigTestArgs) {
					t.Errorf("machine %s, %d cache files, CPU_PERCENT %v, nproc %q: args %q, want them unchanged",
						machine, len(caches), tc.percent, tc.cpus, got)
				}
			}
		}
	}
}

// Where XMRig v6.27.0 on x86 finds no cache room for a hash, it ignores the hint.
// HwlocCpuInfo::threads falls back to BasicCpuInfo::threads when hwloc finds no L2
// or L3 size, or when processTopLevelCache adds no thread: a top cache below half a
// scratchpad (rx/0: 2 MiB, cn-heavy: 4 MiB) holds no hash. The fallback starts half
// the threads for RandomX: 4 of 8 at 17%. hwloc's Linux backend reads the sizes from
// cpuN/cache/indexM/{level,size}, so without an L3 of at least 2048K there the cap
// must come from --threads.
func TestXMRigOnX86WithoutAnL3Of2048KInSysfsSetsThreads(t *testing.T) {
	ep := mustGet(t, realCatalog(t), "xmrig").Docker.Entrypoint
	cachesCases := []struct {
		name   string
		caches cacheFiles
	}{
		{"no cache folder", cacheFiles{}},
		{"L1 only", cacheFiles{"cpu0/cache/index0": {"1", "48K"}, "cpu0/cache/index1": {"1", "32K"}}},
		{"L2 size zero", cacheFiles{"cpu0/cache/index2": {"2", "0K"}}},
		{"L2 with no size", cacheFiles{"cpu0/cache/index2": {"2", ""}}},
		{"no level", cacheFiles{"cpu0/cache/index3": {"", "24576K"}}},
		// With no L3, XMRig counts the L2. A 4096K L2 holds one cn-heavy hash, so
		// XMRig would follow the hint here. The wrapper checks only the L3 and errs
		// toward --threads, which still caps.
		{"L2 only", cacheFiles{"cpu0/cache/index2": {"2", "4096K"}}},
		// The top cache must hold one hash of every algorithm (cn-heavy: 4 MiB,
		// counted from half), or the cache pass starts no thread and XMRig falls
		// back the same way.
		{"L3 size zero", cacheFiles{"cpu0/cache/index3": {"3", "0K"}}},
		{"small L3", cacheFiles{"cpu0/cache/index2": {"2", "4096K"}, "cpu0/cache/index3": {"3", "1024K"}}},
		{"L3 just under 2048K", cacheFiles{"cpu0/cache/index3": {"3", "2047K"}}},
		{"L3 size not in K", cacheFiles{"cpu0/cache/index3": {"3", "2M"}}},
	}
	shares := []struct {
		percent string
		cpus    int
		threads int
	}{{"17", 8, 1}, {"50", 8, 4}, {"75", 20, 15}}
	for _, cc := range cachesCases {
		for _, tc := range shares {
			got := xmrigArgsWithCaches(t, ep, ptr(tc.percent), fmt.Sprint(tc.cpus), "x86_64", cc.caches)
			want := append(slices.Clone(xmrigTestArgs), fmt.Sprintf("--threads=%d", tc.threads))
			if !slices.Equal(got, want) {
				t.Errorf("%s, %s%% of %d: args %q, want %q", cc.name, tc.percent, tc.cpus, got, want)
			}
		}
	}
}

func TestXMRigOnX86AnL3Of2048KOnAnyCPUKeepsTheHint(t *testing.T) {
	ep := mustGet(t, realCatalog(t), "xmrig").Docker.Entrypoint
	for name, caches := range map[string]cacheFiles{
		"L3 of 2048K": {"cpu0/cache/index3": {"3", "2048K"}},
		"L3 on cpu11": {"cpu0/cache/index0": {"1", "48K"}, "cpu11/cache/index3": {"3", "24576K"}},
	} {
		got := xmrigArgsWithCaches(t, ep, ptr("17"), "8", "x86_64", caches)
		if len(got) != len(xmrigTestArgs)+1 || !slices.Equal(got[:len(xmrigTestArgs)], xmrigTestArgs) {
			t.Fatalf("%s: args %q", name, got)
		}
		value, ok := strings.CutPrefix(got[len(got)-1], "--cpu-max-threads-hint=")
		var hint int
		if _, err := fmt.Sscan(value, &hint); !ok || err != nil {
			t.Errorf("%s: last arg %q, want the hint", name, got[len(got)-1])
			continue
		}
		if threads := xmrigX86Threads(8, hint); threads != 1 {
			t.Errorf("%s: hint %d starts %d threads of 8, want 1", name, hint, threads)
		}
	}
}
