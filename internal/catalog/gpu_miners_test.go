package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The two GPU miners are the web files byte for byte: SRBMiner-Multi at
// GeiserX/CashPilot 6111922 (#843) and KRig on Kryptex Pool at 9b13204 (#841). If a
// web entry changes, vendor it with catalogsync, check the tests below still
// describe it, and update its sum in the same PR.
var gpuMinerWebSHA256 = map[string]string{
	"srbminer-multi": "b34d0e7f87920017eb3052e65d07c51912bf3dfca93b704502cb563766c4f5b4",
	"kryptex-pearl":  "c2f6590c05062c5ed63c4476572edf9abc050bc4c94dad11a089af5670f5b231",
}

func TestGPUMinersAreTheWebFiles(t *testing.T) {
	for slug, want := range gpuMinerWebSHA256 {
		raw, err := os.ReadFile(filepath.Join("..", "..", "services", "mining", slug+".yml"))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("services/mining/%s.yml is not the web file (sha256 %s, want %s). Vendor it with catalogsync and recheck these tests before updating the sum", slug, got, want)
		}
	}
}

// What the desktop app reads from each entry: one NVIDIA GPU under runc, the fee
// before deploy, and where the money waits.
func TestGPUMinersLoadWithTheirGrantFeeAndPayout(t *testing.T) {
	cat := realCatalog(t)
	for slug, want := range map[string]struct {
		fee   float64
		model string
	}{
		"srbminer-multi": {2, "external"},
		"kryptex-pearl":  {0, "internal"},
	} {
		svc := mustGet(t, cat, slug)
		if problems := GPUProblems(svc); len(problems) > 0 {
			t.Errorf("%s: GPU problems %v", slug, problems)
		}
		grant, ok := svc.Docker.GPU.Grant()
		if !ok || grant != (GPUGrant{Vendor: "nvidia"}) || svc.Docker.Runtime != "runc" || !svc.Requirements.GPU {
			t.Errorf("%s: grant %+v (%v), runtime %q, gpu %v; want one NVIDIA GPU under runc", slug, grant, ok, svc.Docker.Runtime, svc.Requirements.GPU)
		}
		if svc.Mining == nil || svc.Mining.DevFeePercent == nil || *svc.Mining.DevFeePercent != want.fee ||
			!strings.HasPrefix(svc.Mining.DevFeeSource, "https://") || strings.TrimSpace(svc.Mining.PoolFeeNote) == "" {
			t.Errorf("%s: mining %+v, want a %v%% fee with its source and pool note", slug, svc.Mining, want.fee)
		}
		if svc.Payout.Model != want.model {
			t.Errorf("%s: payout model %q, want %q", slug, svc.Payout.Model, want.model)
		}
	}
	kryptex := mustGet(t, cat, "kryptex-pearl")
	if kryptex.Referral.SignupURL != "https://pool.kryptex.com/?ref=2444cca8" || kryptex.Referral.Code != "2444cca8" {
		t.Errorf("kryptex-pearl signup %q code %q: the referral link must stay", kryptex.Referral.SignupURL, kryptex.Referral.Code)
	}
	if !strings.Contains(kryptex.Requirements.Note, "Volta (sm_70) or newer") {
		t.Errorf("kryptex-pearl requirements note %q lost KRig's GPU limit", kryptex.Requirements.Note)
	}
}

// The deploy form puts each env pattern on its input, and the browser compiles an
// input pattern with the v flag. Under v a pattern the old u flag took, such as a
// literal "-" at the end of a class, is invalid, and the browser then checks
// nothing. Web #841 fixed two of these; this compiles every catalog pattern the way
// the browser does (as ^(?:pattern)$ with v), as web tests/test_catalog_html_patterns.py.
func TestEveryEnvPatternCompilesAsAnHTMLPattern(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		// CI runners ship node; a skip there would hide a pattern the browser ignores.
		if os.Getenv("CI") != "" {
			t.Fatal("node is not on PATH in CI, so the browser's pattern rules were not checked")
		}
		t.Skip("node is not installed, so the browser's pattern rules cannot be checked here")
	}
	patterns := map[string]string{}
	for _, svc := range realCatalog(t).List() {
		for _, item := range append(append([]EnvVar(nil), svc.Docker.Env...), svc.Native.Env...) {
			if item.Pattern != "" {
				patterns[svc.Slug+" "+item.Key] = item.Pattern
			}
		}
	}
	if len(patterns) < 10 {
		t.Fatalf("found only %d patterns; the catalog shape changed, so this check proves nothing", len(patterns))
	}
	// The control: an unescaped "-" at the end of a class, which web #841 removed.
	patterns["control"] = `[A-Za-z0-9_-]{1,32}`
	bad := runPatternCheck(t, node, patterns, nil)
	if _, ok := bad["control"]; !ok {
		t.Fatalf("node compiled the control pattern under v, so it does not check what a browser checks: %v", bad)
	}
	delete(bad, "control")
	for key, why := range bad {
		t.Errorf("%s: pattern %q does not compile as an HTML pattern: %s", key, patterns[key], why)
	}

	// The two fields fixed in #841, and the two other SRBMiner fields with a "-",
	// still take and refuse what they should.
	samples := map[string][2]string{
		"kryptex-pearl KRYPTEX_EMAIL": {"first.last+rig@mail-host.example.com", "not an email"},
		"kryptex-pearl WORKER_NAME":   {"rig-2_b", "bad!"},
		"srbminer-multi WORKER_NAME":  {"rig-2_b", "bad!"},
		"srbminer-multi POOL_URL":     {"pearl.herominers.com:1200", "pool:1"},
	}
	for key, sample := range samples {
		got := runPatternCheck(t, node, map[string]string{key: patterns[key]}, map[string]string{key: sample[0]})
		if len(got) > 0 {
			t.Errorf("%s refuses %q: %v", key, sample[0], got)
		}
		got = runPatternCheck(t, node, map[string]string{key: patterns[key]}, map[string]string{key: sample[1]})
		if _, ok := got[key]; !ok {
			t.Errorf("%s takes %q", key, sample[1])
		}
	}
}

// runPatternCheck compiles each pattern as ^(?:p)$ with the v flag and, when a value
// is given for it, tests the value. It returns the keys that failed and why.
func runPatternCheck(t *testing.T, node string, patterns, values map[string]string) map[string]string {
	t.Helper()
	in, err := json.Marshal(map[string]any{"patterns": patterns, "values": values})
	if err != nil {
		t.Fatal(err)
	}
	const script = `let s="";process.stdin.on("data",c=>s+=c).on("end",()=>{const {patterns,values}=JSON.parse(s);const bad={};
for(const [k,p] of Object.entries(patterns)){let re;try{re=new RegExp("^(?:"+p+")$","v")}catch(e){bad[k]=e.message;continue}
if(values&&k in values&&!re.test(values[k]))bad[k]="does not match "+JSON.stringify(values[k])}
process.stdout.write(JSON.stringify(bad))})`
	cmd := exec.Command(node, "-e", script)
	cmd.Stdin = strings.NewReader(string(in))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	bad := map[string]string{}
	if err := json.Unmarshal(out, &bad); err != nil {
		t.Fatalf("node output %q: %v", out, err)
	}
	return bad
}
