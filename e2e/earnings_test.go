package e2e

import (
	"math"
	"strings"
	"testing"
)

// TestEarningsReachTheStore drives the real collectors over real HTTP against the
// stand-in platforms and checks what ends up in the database — which is what the
// dashboard reads. Collectors are the part of the app that talks to the outside
// world, so this is where a wrong unit (cents read as dollars) or a lost token
// shows up.
func TestEarningsReachTheStore(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()

	e.Providers.SetBalance("honeygain", 12.50)
	e.Providers.SetBalance("iproyal", 8.75)
	e.Providers.SetBalance("traffmonetizer", 7.25)
	e.Providers.SetBalance("mysterium", 40)

	creds := map[string]map[string]string{
		"honeygain":      honeygainCreds,
		"iproyal":        {"IPROYALPAWNS_EMAIL": "someone@example.test", "IPROYALPAWNS_PASSWORD": "not-a-real-password"},
		"traffmonetizer": {"TRAFFMONETIZER_TOKEN": "not-a-real-token"},
		"mysterium":      {"MYSTNODES_EMAIL": "someone@example.test", "MYSTNODES_PASSWORD": "not-a-real-password"},
	}
	want := map[string]struct {
		balance  float64
		currency string
	}{
		"honeygain":      {12.50, "USD"},
		"iproyal":        {8.75, "USD"},
		"traffmonetizer": {7.25, "USD"},
		"mysterium":      {40, "MYST"},
	}

	for slug, values := range creds {
		if !e.Collector.Supports(slug) {
			t.Fatalf("%s has no collector, so this test is asserting nothing", slug)
		}
		record, err := e.Collector.Collect(ctx, slug, values)
		if err != nil {
			t.Fatalf("Collect(%s): %v", slug, err)
		}
		if record.Error != "" {
			t.Fatalf("Collect(%s) recorded an error: %s", slug, record.Error)
		}
		if e.Providers.Hits(slug) == 0 {
			t.Errorf("%s answered without being asked, so nothing was really collected", slug)
		}
	}

	latest := map[string]struct {
		balance  float64
		currency string
	}{}
	for _, row := range e.Store.ListLatestEarnings() {
		latest[row.Platform] = struct {
			balance  float64
			currency string
		}{row.Balance, row.Currency}
	}
	for slug, expected := range want {
		got, ok := latest[slug]
		if !ok {
			t.Errorf("%s never reached the store", slug)
			continue
		}
		if !approx(got.balance, expected.balance) || got.currency != expected.currency {
			t.Errorf("%s stored %v %s, want %v %s", slug, got.balance, got.currency, expected.balance, expected.currency)
		}
	}

	// Mysterium's per-node breakdown is a second request on the same token; it
	// lands in the service-detail store next to the flat balance.
	if detail, err := e.Store.GetServiceDetail("mysterium"); err != nil || !strings.Contains(detail, "fake-node") {
		t.Errorf("the per-node breakdown did not reach the store (err=%v, detail=%q)", err, detail)
	}

	// A later reading replaces what the dashboard shows.
	e.Providers.SetBalance("honeygain", 13.75)
	if _, err := e.Collector.Collect(ctx, "honeygain", honeygainCreds); err != nil {
		t.Fatalf("second Collect: %v", err)
	}
	for _, row := range e.Store.ListLatestEarnings() {
		if row.Platform == "honeygain" && !approx(row.Balance, 13.75) {
			t.Errorf("the dashboard still shows %v after a new reading of 13.75", row.Balance)
		}
	}
}

// TestABrokenPlatformIsRecordedAsAFailure pins what happens on the bad day: the
// platform is down, so the reading fails. The failure has to be recorded and
// reach the store, because the user needs to know the number on screen is not
// fresh — a silent failure leaves yesterday's balance looking like today's.
//
// The password check below is a floor, not the proof that secrets are scrubbed:
// honeygain sends its password in the request body, so the error it fails with
// never had the password in it to begin with. The test that can actually catch a
// leak is TestAFailureNeverStoresASecretFromTheRequestURL.
func TestABrokenPlatformIsRecordedAsAFailure(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()

	const password = "not-a-real-password"
	e.Providers.SetFailing("honeygain", true)

	record, err := e.Collector.Collect(ctx, "honeygain", map[string]string{
		"HONEYGAIN_EMAIL":    "someone@example.test",
		"HONEYGAIN_PASSWORD": password,
	})
	if err != nil {
		t.Fatalf("Collect returned a hard error instead of recording the failure: %v", err)
	}
	if record.Error == "" {
		t.Fatal("a failed reading was recorded as a success")
	}
	if record.Balance != 0 {
		t.Errorf("a failed reading reported a balance of %v", record.Balance)
	}
	if strings.Contains(record.Error, password) {
		t.Errorf("the stored error carries the password: %q", record.Error)
	}

	stored := ""
	for _, row := range e.Store.ListLatestEarnings() {
		if row.Platform == "honeygain" {
			stored = row.Error
		}
	}
	if stored == "" {
		t.Error("the failure never reached the store, so the dashboard would show a stale number as current")
	}

	// Positive control: with the platform back, the same call succeeds and the
	// error clears, so the assertions above cannot pass against a collector that
	// always fails.
	e.Providers.SetFailing("honeygain", false)
	e.Providers.SetBalance("honeygain", 5)
	recovered, err := e.Collector.Collect(ctx, "honeygain", honeygainCreds)
	if err != nil || recovered.Error != "" || !approx(recovered.Balance, 5) {
		t.Fatalf("the collector did not recover: %+v (err=%v)", recovered, err)
	}
}

// TestAFailureNeverStoresASecretFromTheRequestURL is the sharper half of the rule
// above. Most platforms take their secret in a header or a request body, so a
// failure message never had it to begin with. Repocket is the one that does not:
// it signs in through Google, and the key is part of the address the app calls.
// When that call is refused, the app builds its error out of that address — so
// without scrubbing, the key lands in the database and on screen, where a support
// screenshot or an exported log carries it straight out of the machine.
func TestAFailureNeverStoresASecretFromTheRequestURL(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()

	const firebaseKey = "AIzaSy-not-a-real-firebase-key"
	t.Setenv("CASHPILOT_REPOCKET_FIREBASE_KEY", firebaseKey)
	repocketCreds := map[string]string{
		"REPOCKET_EMAIL":    "someone@example.test",
		"REPOCKET_PASSWORD": "not-a-real-password",
	}

	e.Providers.SetFailing("repocket", true)
	record, err := e.Collector.Collect(ctx, "repocket", repocketCreds)
	if err != nil {
		t.Fatalf("Collect returned a hard error instead of recording the failure: %v", err)
	}
	if record.Error == "" {
		t.Fatal("a refused sign-in was recorded as a success")
	}
	if strings.Contains(record.Error, firebaseKey) {
		t.Errorf("the recorded failure carries the sign-in key: %q", record.Error)
	}

	stored := ""
	for _, row := range e.Store.ListLatestEarnings() {
		if row.Platform == "repocket" {
			stored = row.Error
		}
	}
	if stored == "" {
		t.Fatal("the failure never reached the store")
	}
	if strings.Contains(stored, firebaseKey) {
		t.Errorf("the key is sitting in the database: %q", stored)
	}

	// Positive control: with the sign-in accepted the same call succeeds, so the
	// assertions above cannot be passing against a collector that never ran. This
	// is also what proves the failing run really reached the sign-in URL: the key
	// is what the fake requires to answer at all.
	e.Providers.SetFailing("repocket", false)
	e.Providers.SetBalance("repocket", 3.40)
	recovered, err := e.Collector.Collect(ctx, "repocket", repocketCreds)
	if err != nil || recovered.Error != "" || !approx(recovered.Balance, 3.40) {
		t.Fatalf("the collector did not recover: %+v (err=%v)", recovered, err)
	}
}

// TestBalancesConvertToTheDisplayCurrency covers the other half of the earnings
// path: a token balance is meaningless on a dashboard until it is priced. The
// numbers here are deliberately round so a wrong direction (multiplying where the
// code should divide) cannot hide behind rounding.
func TestBalancesConvertToTheDisplayCurrency(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()

	e.Providers.SetCryptoUSD("mysterium", 0.25) // 1 MYST = $0.25
	e.Providers.SetFiat("EUR", 0.90)            // $1 = 0.90 EUR

	if err := e.Exchange.Refresh(ctx); err != nil {
		t.Fatalf("refreshing rates: %v", err)
	}

	usd, ok := e.Exchange.ToUSD(40, "MYST")
	if !ok || !approx(usd, 10) {
		t.Errorf("40 MYST converted to %v USD (ok=%v), want 10", usd, ok)
	}
	eur, ok := e.Exchange.ToDisplay(40, "MYST", "EUR")
	if !ok || !approx(eur, 9) {
		t.Errorf("40 MYST converted to %v EUR (ok=%v), want 9", eur, ok)
	}
	plain, ok := e.Exchange.ToDisplay(12.50, "USD", "EUR")
	if !ok || !approx(plain, 11.25) {
		t.Errorf("12.50 USD converted to %v EUR (ok=%v), want 11.25", plain, ok)
	}

	// Reward points have no market price. Converting them would invent money, so
	// they must stay unconvertible rather than pass through at 1:1.
	if e.Exchange.Convertible("GRASS") {
		t.Error("reward points are being treated as a convertible currency")
	}
	if _, ok := e.Exchange.ToUSD(100, "GRASS"); ok {
		t.Error("reward points were converted to dollars")
	}
}

func approx(got, want float64) bool { return math.Abs(got-want) <= 1e-6 }
