package catalog

import (
	"os"
	"path/filepath"
	"testing"
)

// realCatalog loads the catalog actually shipped in services/, so these tests fail
// when the loader stops parsing a field the vendored entries really carry — not when
// a fixture written alongside the loader stops matching it.
func realCatalog(t *testing.T) *Catalog {
	t.Helper()
	cat, err := LoadEmbedded(os.DirFS(filepath.Join("..", "..")))
	if err != nil {
		t.Fatalf("load real services catalog: %v", err)
	}
	if len(cat.List()) < 20 {
		t.Fatalf("loaded only %d services; the real catalog did not load, so these checks "+
			"would pass against nothing", len(cat.List()))
	}
	return cat
}

func mustGet(t *testing.T, cat *Catalog, slug string) Service {
	t.Helper()
	svc, ok := cat.Get(slug)
	if !ok {
		t.Fatalf("%s is missing from the catalog", slug)
	}
	return svc
}

// TestMysteriumCapabilitiesAndDeviceParse guards the fields whose absence is
// invisible: a Mysterium node without SETUID/SETGID registers, shows healthy and
// fails every session at setup, and one without /dev/net/tun advertises itself and
// carries no traffic. Both earn nothing while looking fine.
func TestMysteriumCapabilitiesAndDeviceParse(t *testing.T) {
	svc := mustGet(t, realCatalog(t), "mysterium")

	caps := map[string]bool{}
	for _, c := range svc.Docker.CapAdd {
		caps[c] = true
	}
	for _, want := range []string{"NET_ADMIN", "SETUID", "SETGID"} {
		if !caps[want] {
			t.Errorf("mysterium must request the %s capability, got %v", want, svc.Docker.CapAdd)
		}
	}

	devices := map[string]bool{}
	for _, d := range svc.Docker.Devices {
		devices[d] = true
	}
	if !devices["/dev/net/tun"] {
		t.Errorf("mysterium must map /dev/net/tun, got %v", svc.Docker.Devices)
	}

	if len(svc.Docker.CriticalVolumes) == 0 {
		t.Fatal("mysterium must declare its identity keystore as a critical volume")
	}
	found := false
	for _, cv := range svc.Docker.CriticalVolumes {
		if cv.Target == "/var/lib/mysterium-node" {
			found = true
			if cv.Holds == "" {
				t.Error("a critical volume with no `holds` text tells the operator nothing about what they are destroying")
			}
		}
	}
	if !found {
		t.Errorf("mysterium critical volume /var/lib/mysterium-node not parsed, got %+v", svc.Docker.CriticalVolumes)
	}

	if len(svc.Docker.HealthSignals) == 0 {
		t.Fatal("mysterium must carry its health signals; they are the only evidence that a healthy-looking node is not earning")
	}
	for _, sig := range svc.Docker.HealthSignals {
		if sig.Pattern == "" || sig.Means == "" {
			t.Errorf("health signal parsed with an empty field: %+v", sig)
		}
	}

	if !svc.Collector.PerNodeEarnings {
		t.Error("mysterium reports earnings per node; collector.per_node_earnings must parse as true")
	}
	if svc.Collector.CredentialHint == "" {
		t.Error("mysterium declares a credential_hint; it must parse")
	}
	if svc.Disclosure.ThirdPartyTraffic == "" || svc.Disclosure.Sells == "" {
		t.Errorf("mysterium declares a disclosure block; it must parse, got %+v", svc.Disclosure)
	}
	if svc.Payout.Model != "external" || svc.Payout.Chain != "polygon" || svc.Payout.AddressSource != "manual" {
		t.Errorf("mysterium payout = %+v, want model=external chain=polygon address_source=manual", svc.Payout)
	}
	// devices_per_ip: 0 is a verified "no per-IP limit", not an omission.
	if svc.Requirements.DevicesPerIP == nil || *svc.Requirements.DevicesPerIP != 0 {
		t.Errorf("mysterium devices_per_ip = %v, want an explicit 0", svc.Requirements.DevicesPerIP)
	}
	// Desktop-only: the native block must survive the catalog sync.
	if !svc.HasNative() {
		t.Error("mysterium must keep its Desktop-only native: block through the catalog sync")
	}
}

// TestTraffmonetizerImageByArchParses guards the per-architecture image overrides.
// Docker Hub labels every tag of this image linux/amd64, so an ARM host that ignores
// image_by_arch pulls an amd64 build and gets "exec format error".
func TestTraffmonetizerImageByArchParses(t *testing.T) {
	svc := mustGet(t, realCatalog(t), "traffmonetizer")

	if got := svc.Docker.ImageByArch["arm64"]; got != "traffmonetizer/cli_v2:arm64v8" {
		t.Errorf("image_by_arch[arm64] = %q, want traffmonetizer/cli_v2:arm64v8", got)
	}
	if got := svc.Docker.ImageByArch["arm"]; got != "traffmonetizer/cli_v2:arm32v7" {
		t.Errorf("image_by_arch[arm] = %q, want traffmonetizer/cli_v2:arm32v7", got)
	}

	platforms := map[string]bool{}
	for _, p := range svc.Docker.Platforms {
		platforms[p] = true
	}
	for _, want := range []string{"linux/amd64", "linux/arm64", "linux/arm/v7"} {
		if !platforms[want] {
			t.Errorf("traffmonetizer must publish %s, got %v", want, svc.Docker.Platforms)
		}
	}
	if svc.Payment.CryptoToken != "USDT" {
		t.Errorf("payment.crypto_token = %q, want USDT", svc.Payment.CryptoToken)
	}
}

// TestStorjStopTimeoutAndAdvertisedAddressParse guards the two fields that make a
// Storj node's failure modes visible: 300 s to shut down (a node SIGKILLed after the
// default 30 s loses in-flight pieces and audit score) and the env var carrying the
// address the network dials back at.
func TestStorjStopTimeoutAndAdvertisedAddressParse(t *testing.T) {
	svc := mustGet(t, realCatalog(t), "storj")

	if svc.Docker.StopTimeout != 300 {
		t.Errorf("storj stop_timeout = %d, want 300", svc.Docker.StopTimeout)
	}
	if svc.Docker.AdvertisedAddressEnv != "ADDRESS" {
		t.Errorf("storj advertised_address_env = %q, want ADDRESS", svc.Docker.AdvertisedAddressEnv)
	}
	if svc.Docker.Resources.CPUShares != 4096 {
		t.Errorf("storj cpu_shares = %d, want 4096", svc.Docker.Resources.CPUShares)
	}
	if len(svc.Docker.CriticalVolumes) == 0 {
		t.Error("storj must declare its identity as a critical volume")
	}
	if svc.Payout.Model != "external" || svc.Payout.AddressEnv != "WALLET" {
		t.Errorf("storj payout = %+v, want model=external address_env=WALLET", svc.Payout)
	}
}

// TestReferralCodesParse guards revenue. The code is carried next to the URL so that
// a provider domain migration which drops the code from signup_url is detectable
// instead of silently costing attribution.
func TestReferralCodesParse(t *testing.T) {
	cat := realCatalog(t)

	if got := mustGet(t, cat, "traffmonetizer").Referral.Code; got != "2111758" {
		t.Errorf("traffmonetizer referral.code = %q, want 2111758", got)
	}
	if got := mustGet(t, cat, "mysterium").Referral.Code; got != "do7v7YOoBBpbOstKQovX2pUvZYKia4ZhH3QIdNtE" {
		t.Errorf("mysterium referral.code = %q, want the code embedded in its signup_url", got)
	}

	// referral.program is three-valued: absent means nobody has checked, and must not
	// read as a verified "no".
	repocket := mustGet(t, cat, "repocket")
	if repocket.Referral.Program == nil || !*repocket.Referral.Program {
		t.Errorf("repocket referral.program = %v, want an explicit true", repocket.Referral.Program)
	}
	anyone := mustGet(t, cat, "anyone-protocol")
	if anyone.Referral.Program == nil || *anyone.Referral.Program {
		t.Errorf("anyone-protocol referral.program = %v, want an explicit false", anyone.Referral.Program)
	}
	if mustGet(t, cat, "storj").Referral.Program != nil {
		t.Error("storj declares no referral.program; it must stay nil (unchecked), not become false")
	}

	// Every code that is declared must still be ANCHORED in the URL that carries it:
	// a query value, a bare query key or a whole path segment, the placements
	// services/_schema.yml documents. A substring test would pass on a coincidence
	// ("grass" inside app.grass.io) and so would report a link as attributed after a
	// migration had dropped the code — the exact failure this field exists to catch.
	for _, svc := range cat.List() {
		if svc.Referral.Code == "" {
			continue
		}
		if !CodeAttributesURL(svc.Referral.Code, svc.Referral.SignupURL) {
			t.Errorf("%s: referral.code %q is not anchored in signup_url %q; a provider reading that URL sees no attribution",
				svc.Slug, svc.Referral.Code, svc.Referral.SignupURL)
		}
	}
}

// TestEarnAppContainerProhibitedParses guards the strongest verdict in the catalog.
// EarnApp forbids containers, VMs and servers; the penalty is a terminated account
// with the pending balance cancelled. A field that fails to parse turns that into
// silence at exactly the moment the tool is about to cause the breach.
func TestEarnAppContainerProhibitedParses(t *testing.T) {
	svc := mustGet(t, realCatalog(t), "earnapp")
	if !svc.Requirements.ContainerProhibited {
		t.Error("earnapp requirements.container_prohibited must parse as true")
	}
}

// TestDevicesPerIPDistinguishesOmittedFromZero is the reverse check of the pointer
// type: an entry that documents no per-IP limit must stay nil, because nil and 0 are
// told to the user as different things.
func TestDevicesPerIPDistinguishesOmittedFromZero(t *testing.T) {
	cat := realCatalog(t)

	if got := mustGet(t, cat, "repocket").Requirements.DevicesPerIP; got != nil {
		t.Errorf("repocket documents no per-IP limit; devices_per_ip = %v, want nil", *got)
	}
	zero := mustGet(t, cat, "traffmonetizer").Requirements.DevicesPerIP
	if zero == nil || *zero != 0 {
		t.Errorf("traffmonetizer devices_per_ip = %v, want an explicit 0", zero)
	}
}

// TestDroppedServicesAreHidden covers the point of the `dropped` status: an entry
// that was evaluated and then removed must not be offered, for the same reason a
// dead one is not. Offering it invites a signup that will never pay.
func TestDroppedServicesAreHidden(t *testing.T) {
	cat := realCatalog(t)

	dropped := map[string]bool{}
	for _, svc := range cat.List() {
		if svc.Status == "dropped" {
			dropped[svc.Slug] = true
		}
	}
	if len(dropped) == 0 {
		t.Fatal("no dropped entries in the catalog, so this check proves nothing; " +
			"if the last one was retired differently, retarget it at a status that is still present")
	}

	for _, svc := range cat.ListVisible() {
		if dropped[svc.Slug] {
			t.Errorf("%s has status dropped but is still listed as visible", svc.Slug)
		}
		if IsRetired(svc.Status) {
			t.Errorf("%s has retired status %q but is still listed as visible", svc.Slug, svc.Status)
		}
	}
}

// TestResurrectionCheckedParses: a dead entry whose site still answers carries the
// date a human confirmed the programme really is gone, so the reason it stays dead
// travels with the entry.
func TestResurrectionCheckedParses(t *testing.T) {
	cat := realCatalog(t)

	var checked []string
	for _, svc := range cat.List() {
		if svc.ResurrectionChecked != "" {
			checked = append(checked, svc.Slug)
			if svc.Status != "dead" {
				t.Errorf("%s carries resurrection_checked but its status is %q, not dead", svc.Slug, svc.Status)
			}
		}
	}
	if len(checked) == 0 {
		t.Fatal("no entry carries resurrection_checked, so this check proves nothing")
	}

	presearch := mustGet(t, cat, "presearch")
	if presearch.Status != "dead" {
		t.Errorf("presearch status = %q, want dead", presearch.Status)
	}
	if presearch.ResurrectionChecked == "" {
		t.Error("presearch is dead with a live site; it must carry the date that was confirmed")
	}
}
