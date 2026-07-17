package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProxyBaseContainerContract is a regression guard for the ProxyBase migration
// (CashPilot web issue #103). ProxyBase retired its Docker Hub image and old GHCR org
// and shipped a new client whose env contract is ID + NAME (verified against the
// image's own --help: "ProxyBase-Peer [<ID> [<NAME>]]", "set env var ID" / "set env
// var NAME"). The retired USER_ID/DEVICE_NAME are ignored by the new client, so a
// container built on those silently fails with "Missing ID and NAME". Pin by digest,
// never the retired proxybase/proxybase image or a floating tag.
func TestProxyBaseContainerContract(t *testing.T) {
	cat, err := LoadEmbedded(os.DirFS(filepath.Join("..", "..")))
	if err != nil {
		t.Fatalf("load real services catalog: %v", err)
	}

	svc, ok := cat.Get("proxybase")
	if !ok {
		t.Fatal("proxybase service not found in catalog")
	}

	if !strings.HasPrefix(svc.Docker.Image, "ghcr.io/proxybaseorg/peer-cli@sha256:") {
		t.Errorf("ProxyBase must use the digest-pinned GHCR peer-cli image, got %q", svc.Docker.Image)
	}

	arm64 := false
	for _, p := range svc.Docker.Platforms {
		if p == "linux/arm64" {
			arm64 = true
		}
	}
	if !arm64 {
		t.Errorf("ProxyBase must keep linux/arm64 (multi-arch image; Raspberry Pi support), got %v", svc.Docker.Platforms)
	}

	byKey := map[string]EnvVar{}
	for _, e := range svc.Docker.Env {
		byKey[e.Key] = e
	}
	if len(byKey) != 2 || byKey["ID"].Key == "" || byKey["NAME"].Key == "" {
		keys := make([]string, 0, len(byKey))
		for k := range byKey {
			keys = append(keys, k)
		}
		t.Fatalf("ProxyBase container env must be exactly ID + NAME (the peer-cli contract), got %v", keys)
	}
	if !byKey["ID"].Required {
		t.Error("ID (Access Token) must be required")
	}
	if !byKey["ID"].Secret {
		t.Error("ID (Access Token) must be marked secret (masked in the UI)")
	}
	if !byKey["NAME"].Required {
		t.Error("NAME (Device Name) must be required")
	}

	// Referral revenue guard: the signup URL must keep the referral code.
	if svc.Referral.SignupURL != "https://peer.proxybase.org?referral=nXzS3c6iTO" {
		t.Errorf("signup_url must keep the referral code, got %q", svc.Referral.SignupURL)
	}

	// Datacenter/VPS IPs are accepted (per the ProxyBase team); drives the UI badge.
	if svc.Requirements.ResidentialIP {
		t.Error("ProxyBase no longer requires a residential IP")
	}
	if !svc.Requirements.VPSIP {
		t.Error("ProxyBase must mark VPS/datacenter IPs as supported")
	}

	// Domain migrated proxybase.io -> proxybase.org across every user-facing URL.
	if strings.Contains(svc.Website, "proxybase.io") {
		t.Errorf("website must use proxybase.org, got %q", svc.Website)
	}
	for _, e := range svc.Docker.Env {
		if strings.Contains(e.Description, "proxybase.io") {
			t.Errorf("env %s description still links proxybase.io", e.Key)
		}
	}
}
