package catalog

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"testing/fstest"
)

// The deploy wizard shows a miner's fee before it runs, so the fee has to survive
// the load exactly as the web catalog states it.
func TestXMRigMiningFeeLoads(t *testing.T) {
	svc := mustGet(t, realCatalog(t), "xmrig")
	m := svc.Mining
	if m == nil {
		t.Fatal("xmrig must carry its mining: block after load")
	}
	if m.DevFeePercent == nil || *m.DevFeePercent != 1 {
		t.Errorf("xmrig dev_fee_percent = %v, want 1", m.DevFeePercent)
	}
	if !strings.HasPrefix(m.DevFeeSource, "https://github.com/xmrig/xmrig/") {
		t.Errorf("xmrig dev_fee_source = %q, want the upstream source link", m.DevFeeSource)
	}
	if !strings.Contains(m.PoolFeeNote, "MoneroOcean") {
		t.Errorf("xmrig pool_fee_note lost at load: %q", m.PoolFeeNote)
	}
}

// requirements.hosting_note is the web app's advice for a worker on a hosting
// connection. Desktop cannot see the connection type, so the note is parsed (the
// strict decode test needs that) and never sent to the frontend.
func TestHostingNoteIsParsedAndNeverSentToTheFrontend(t *testing.T) {
	svc := mustGet(t, realCatalog(t), "xmrig")
	note := svc.Requirements.HostingNote
	if !strings.Contains(note, "hosting connection") {
		t.Fatalf("xmrig requirements.hosting_note was not parsed: %q", note)
	}
	raw, err := json.Marshal(svc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hosting connection") || strings.Contains(string(raw), "hostingNote") {
		t.Errorf("the hosting note reached the frontend JSON: %s", raw)
	}
	if !strings.Contains(string(raw), `"devFeePercent":1`) {
		t.Errorf("the miner fee must reach the frontend JSON, got %s", raw)
	}
}

// A fee outside 0 to 10 percent, or a NaN, would put a number nobody checked in
// front of the user (and a NaN would fail the whole service list in encoding/json).
// It is shown as not stated. A stated 0 is a real fee and stays. A link that is not
// https:// is cleared, as support_url is.
func TestLoaderClearsAMinerFeeTheWebRejects(t *testing.T) {
	entry := func(slug, mining string) string {
		return "name: " + slug + "\nslug: " + slug + "\ncategory: mining\ndocker:\n  image: a:1\nmining:\n" + mining
	}
	fsys := fstest.MapFS{
		"services/mining/zero.yml":   {Data: []byte(entry("zero", "  dev_fee_percent: 0\n  dev_fee_source: https://example.com/fee\n"))},
		"services/mining/high.yml":   {Data: []byte(entry("high", "  dev_fee_percent: 11\n  dev_fee_source: http://example.com/fee\n"))},
		"services/mining/neg.yml":    {Data: []byte(entry("neg", "  dev_fee_percent: -1\n"))},
		"services/mining/nan.yml":    {Data: []byte(entry("nan", "  dev_fee_percent: .nan\n"))},
		"services/mining/absent.yml": {Data: []byte(entry("absent", "  pool_fee_note: pool note\n"))},
	}
	cat, err := LoadEmbedded(fsys)
	if err != nil {
		t.Fatal(err)
	}
	zero := mustGet(t, cat, "zero").Mining
	if zero.DevFeePercent == nil || *zero.DevFeePercent != 0 || zero.DevFeeSource != "https://example.com/fee" {
		t.Errorf("a stated 0%% fee with an https source must stay, got %+v", zero)
	}
	high := mustGet(t, cat, "high").Mining
	if high.DevFeePercent != nil || high.DevFeeSource != "" {
		t.Errorf("an 11%% fee and an http source must be cleared, got fee %v source %q", high.DevFeePercent, high.DevFeeSource)
	}
	for _, slug := range []string{"neg", "nan", "absent"} {
		if fee := mustGet(t, cat, slug).Mining.DevFeePercent; fee != nil {
			t.Errorf("%s: dev_fee_percent must read as not stated, got %v", slug, *fee)
		}
	}
	if _, err := json.Marshal(cat.List()); err != nil {
		t.Errorf("the service list must still encode: %v", err)
	}
}

// A custom service skips the loader, so SetCustom holds its mining: block to the
// same rules: a fee outside 0 to 10 percent or a NaN reads as not stated, and a
// link that is not https:// is cleared. A valid fee is the control. The caller's
// own block is left as it was.
func TestSetCustomClearsAMinerFeeTheWebRejects(t *testing.T) {
	fee := func(v float64) *float64 { return &v }
	custom := func(slug string, m Mining) Service {
		return Service{Name: slug, Slug: "custom-" + slug, Custom: true, Mining: &m}
	}
	given := []Service{
		custom("ok", Mining{DevFeePercent: fee(5), DevFeeSource: "https://example.com/fee"}),
		custom("neg", Mining{DevFeePercent: fee(-1), DevFeeSource: "https://example.com/fee"}),
		custom("high", Mining{DevFeePercent: fee(11), DevFeeSource: "http://example.com/fee"}),
		custom("nan", Mining{DevFeePercent: fee(math.NaN()), DevFeeSource: "javascript:alert(1)"}),
	}
	cat := newCatalog(nil)
	cat.SetCustom(given)
	ok := mustGet(t, cat, "custom-ok").Mining
	if ok.DevFeePercent == nil || *ok.DevFeePercent != 5 || ok.DevFeeSource != "https://example.com/fee" {
		t.Errorf("a 5%% fee with an https source must stay, got %+v", ok)
	}
	for _, slug := range []string{"custom-neg", "custom-high", "custom-nan"} {
		if got := mustGet(t, cat, slug).Mining.DevFeePercent; got != nil {
			t.Errorf("%s: dev_fee_percent must read as not stated, got %v", slug, *got)
		}
	}
	for _, slug := range []string{"custom-high", "custom-nan"} {
		if src := mustGet(t, cat, slug).Mining.DevFeeSource; src != "" {
			t.Errorf("%s: a source that is not https:// must be cleared, got %q", slug, src)
		}
	}
	if given[2].Mining.DevFeePercent == nil || given[2].Mining.DevFeeSource == "" {
		t.Error("SetCustom changed the caller's mining block")
	}
	if _, err := json.Marshal(cat.ListVisible()); err != nil {
		t.Errorf("the service list must still encode: %v", err)
	}
}
