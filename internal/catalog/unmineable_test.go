package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// unMineableWebSHA256 is the sha256 of services/mining/unmineable.yml in the web
// repository after GeiserX/CashPilot #907, which moved the referral split under
// referral.bonus. #850 before it made the CPU cap wrapper (#847) hold on x86 hosts
// with no L3 of at least 2048K in sysfs, in the entry #844 created. If the web entry changes, vendor it with catalogsync, check the referral
// tests below still describe it, and update this sum in the same PR.
const unMineableWebSHA256 = "6f4a5a583fbdc41f70780dc9d7a76c2f40829716656eda5302ba855b72f0cc6f"

// unMineableReferral is CashPilot's unMineable referral code. unMineable reads it
// from the "#code" after the worker name, so every worker must carry it.
const unMineableReferral = "awk6-j1z0"

func TestUnMineableIsTheWebEntry(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "services", "mining", "unmineable.yml"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != unMineableWebSHA256 {
		t.Fatalf("services/mining/unmineable.yml is not the web file after #907 (sha256 %s, want %s). "+
			"Vendor it with catalogsync and check the referral code before updating the sum", got, unMineableWebSHA256)
	}
}

func TestEveryUnMineableWorkerCarriesTheReferralCode(t *testing.T) {
	cat := realCatalog(t)
	svc := mustGet(t, cat, "unmineable")
	if svc.Referral.Code != unMineableReferral || svc.Referral.Program == nil || !*svc.Referral.Program {
		t.Fatalf("referral = %+v, want program true with code %s", svc.Referral, unMineableReferral)
	}
	if svc.Referral.SignupURL != "https://unmineable.com/?ref="+unMineableReferral {
		t.Errorf("signup URL %q lost the referral code", svc.Referral.SignupURL)
	}
	user := "--user=${PAYOUT_COIN}:${WALLET}.${WORKER_NAME}#" + unMineableReferral
	if !slices.Contains(strings.Fields(svc.Docker.Command), user) {
		t.Errorf("command %q does not log in as %s", svc.Docker.Command, user)
	}
	if !strings.Contains(svc.Docker.Command, "--url=stratum+ssl://rx.unmineable.com:443") || !strings.Contains(svc.Docker.Command, "--donate-level=1") {
		t.Errorf("command %q lost the fixed pool or the miner fee", svc.Docker.Command)
	}
	if svc.Mining == nil || !strings.Contains(svc.Mining.PoolFeeNote, "#"+unMineableReferral) {
		t.Errorf("the fee note does not disclose the referral code: %+v", svc.Mining)
	}
	if svc.Referral.Bonus.Referrer == "" || svc.Referral.Bonus.Referee == "" {
		t.Errorf("the referral split was not read: %+v", svc.Referral)
	}
	if svc.Docker.Image != mustGet(t, cat, "xmrig").Docker.Image {
		t.Errorf("image %q is not the XMRig entry's", svc.Docker.Image)
	}
}

// No field may carry a second "#code", a second worker or another pool, which
// would replace or drop the referral the command appends.
func TestNoUnMineableFieldCanReplaceTheReferralCode(t *testing.T) {
	svc := mustGet(t, realCatalog(t), "unmineable")
	field := func(key string) EnvVar {
		for _, v := range svc.Docker.Env {
			if v.Key == key {
				return v
			}
		}
		t.Fatalf("unmineable has no %s field", key)
		return EnvVar{}
	}
	const wallet = "44AFFq5kSiGBoZ4NMDwYtN18obc8AemS33DBLWs3H7otXft3XjrpDtQGv7SqSsaBYBb98uNbr2VBBEt7f2wfn3RVGQBEP3A"
	for _, tc := range []struct{ key, value string }{
		{"WALLET", wallet + ".other#someone-else"},
		{"WALLET", wallet + "#someone-else"},
		{"WALLET", "XMR:" + wallet},
		{"WALLET", wallet + " --url=stratum+tcp://evil.example:3333"},
		{"WORKER_NAME", "rig#someone-else"},
		{"WORKER_NAME", "rig.two"},
		{"PAYOUT_COIN", "XMR:other"},
		{"PAYOUT_COIN", "X"},
	} {
		if field(tc.key).AcceptsValue(tc.value) {
			t.Errorf("%s accepts %q", tc.key, tc.value)
		}
	}
	for key, value := range map[string]string{"WALLET": wallet, "WORKER_NAME": "rig-1", "PAYOUT_COIN": "SHIB"} {
		if !field(key).AcceptsValue(value) {
			t.Errorf("%s refuses the valid value %q", key, value)
		}
	}
}
