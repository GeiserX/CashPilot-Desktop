package importers

// The importer's parser and its mapping onto the catalog. A port of the web app's
// tests/test_importers.py, run against the same fixtures (testdata/, copied from the
// web repository's tests/fixtures/importers): each tool's own format, fake values.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

var (
	catOnce sync.Once
	cat     *catalog.Catalog
)

func getService(t *testing.T) func(string) (catalog.Service, bool) {
	t.Helper()
	catOnce.Do(func() {
		c, err := catalog.LoadEmbedded(os.DirFS("../.."))
		if err != nil {
			panic(err)
		}
		cat = c
	})
	return cat.Get
}

func fixtures(t *testing.T, paths ...string) []File {
	t.Helper()
	var out []File
	for _, p := range paths {
		raw, err := os.ReadFile(filepath.Join("testdata", p))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, File{Name: filepath.Base(p), Content: string(raw)})
	}
	return out
}

func preview(t *testing.T, files []File, source string, extra map[string]map[string]string) Preview {
	t.Helper()
	p, err := Run(files, source, getService(t), extra)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return p
}

func rows(p Preview) map[string]Row {
	out := map[string]Row{}
	for _, r := range p.Rows {
		out[r.Slug] = r
	}
	return out
}

func field(t *testing.T, r Row, key string) Field {
	t.Helper()
	for _, f := range r.Fields {
		if f.Key == key {
			return f
		}
	}
	t.Fatalf("%s has no field %s", r.Slug, key)
	return Field{}
}

func eqEnv(t *testing.T, what string, got, want map[string]string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("%s env = %v, want %v", what, got, want)
	}
}

func skippedKeys(p Preview) map[string]string {
	out := map[string]string{}
	for _, s := range p.NotImported {
		out[s.Key] = s.Reason
	}
	return out
}

func refusedWith(t *testing.T, err error, substr string) {
	t.Helper()
	var r *Refused
	if !errors.As(err, &r) {
		t.Fatalf("want a refusal containing %q, got %v", substr, err)
	}
	if !strings.Contains(r.Error(), substr) {
		t.Fatalf("refusal %q does not contain %q", r.Error(), substr)
	}
}

// --- money4band user-config.json ---

func TestMoney4bandJSONLandsOnCatalogFields(t *testing.T) {
	rs := rows(preview(t, fixtures(t, "money4band/user-config.json"), "auto", nil))
	eqEnv(t, "honeygain", rs["honeygain"].Env, map[string]string{"HONEYGAIN_EMAIL": "hg@example.com", "HONEYGAIN_PASSWORD": "fake-pass-honeygain"})
	eqEnv(t, "earnapp", rs["earnapp"].Env, map[string]string{"EARNAPP_UUID": "sdk-node-0123456789abcdef0123456789abcdef"})
	eqEnv(t, "repocket", rs["repocket"].Env, map[string]string{"RP_EMAIL": "rp@example.com", "RP_API_KEY": "11111111-2222-4333-8444-555555555555"})
	eqEnv(t, "proxylite", rs["proxylite"].Env, map[string]string{"USER_ID": "123456"})
	if !strings.HasPrefix(rs["proxyrack"].Env["UUID"], "ABCDEF") {
		t.Errorf("proxyrack UUID = %q", rs["proxyrack"].Env["UUID"])
	}
	for _, s := range []string{"honeygain", "earnapp", "repocket", "bitping"} {
		if rs[s].State != RowReady {
			t.Errorf("%s state = %s (%s), want ready", s, rs[s].State, rs[s].Reason)
		}
	}
	// JSON is what the user typed; only a shell or Compose would expand a $.
	if got := rs["iproyal"].Env["IPROYALPAWNS_PASSWORD"]; got != "fake-pass-$iproyal" {
		t.Errorf("iproyal password = %q", got)
	}
}

func TestMoney4bandAppTurnedOffComesInUnticked(t *testing.T) {
	r := rows(preview(t, fixtures(t, "money4band/user-config.json"), "auto", nil))["earnfm"]
	if r.State != RowReady || r.Selected || !r.OffInSource {
		t.Errorf("earnfm = %+v", r)
	}
}

func TestMoney4bandMysteriumWarnsOfTheNewIdentity(t *testing.T) {
	r := rows(preview(t, fixtures(t, "money4band/user-config.json"), "auto", nil))["mysterium"]
	if r.State != RowReady || !strings.Contains(r.Reason, "new identity") {
		t.Errorf("mysterium = %s %q", r.State, r.Reason)
	}
}

func TestUnusableAndRetiredServicesAreRefusedWithAReason(t *testing.T) {
	rs := rows(preview(t, fixtures(t, "money4band/user-config.json"), "auto", nil))
	want := map[string]string{"proxybase": RowUnusable, "grass": RowUnusable, "peer2profit": RowNotDeployable}
	for slug, state := range want {
		if rs[slug].State != state || rs[slug].Selected || rs[slug].Reason == "" {
			t.Errorf("%s = %s selected=%v reason=%q, want %s unticked with a reason", slug, rs[slug].State, rs[slug].Selected, rs[slug].Reason, state)
		}
	}
}

func TestWhatIsLeftOutIsListedByName(t *testing.T) {
	p := preview(t, fixtures(t, "money4band/user-config.json"), "auto", nil)
	sk := skippedKeys(p)
	if !strings.Contains(sk["antgain"], "no such service") {
		t.Errorf("antgain: %q", sk["antgain"])
	}
	for _, k := range []string{"proxies", "device_info"} {
		if _, ok := sk[k]; !ok {
			t.Errorf("%s not listed", k)
		}
	}
	if _, ok := sk["notifications"]; ok {
		t.Error("a disabled notifications block is not news")
	}
	if !strings.Contains(strings.Join(p.Notices, "\n"), "Proxy") {
		t.Error("no proxy notice")
	}
}

// --- money4band .env ---

func TestMoney4bandGeneratedEnvMapsLikeTheJSON(t *testing.T) {
	p := preview(t, fixtures(t, "money4band/generated.env"), "auto", nil)
	rs := rows(p)
	if rs["honeygain"].Env["HONEYGAIN_PASSWORD"] != "fake-pass-honeygain" || rs["repocket"].Env["RP_API_KEY"] != "11111111-2222-4333-8444-555555555555" {
		t.Errorf("rows = %+v", rs)
	}
	if rs["mysterium"].State != RowReady {
		t.Errorf("mysterium = %s", rs["mysterium"].State)
	}
	sk := skippedKeys(p)
	if len(sk) != 1 || sk["DEVICE_NAME"] == "" {
		t.Errorf("not imported = %v, want only DEVICE_NAME", sk)
	}
	if strings.Contains(strings.Join(p.Notices, "\n"), "Proxy settings") {
		t.Error("STACK_PROXY_URL_EXAMPLE is not a proxy the user set")
	}
}

func TestMoney4bandV3Aliases(t *testing.T) {
	rs := rows(preview(t, fixtures(t, "money4band/v3.env"), "auto", nil))
	if rs["honeygain"].Env["HONEYGAIN_PASSWORD"] != "fake-pass-honeygain" ||
		rs["earnapp"].Env["EARNAPP_UUID"] != "sdk-node-0123456789abcdef0123456789abcdef" ||
		!strings.HasPrefix(rs["proxyrack"].Env["UUID"], "ABCDEF") ||
		rs["bitping"].Env["BITPING_PASSWORD"] != "fake-pass-bitping" {
		t.Errorf("rows = %+v", rs)
	}
	// A command substitution is never imported.
	ip := rs["iproyal"]
	if field(t, ip, "IPROYALPAWNS_PASSWORD").State != FieldRetype || ip.Env["IPROYALPAWNS_PASSWORD"] != "" || ip.State != RowMissingFields {
		t.Errorf("iproyal = %+v", ip)
	}
	// money4band 3.x's .env.template always holds MYSTNODE_DASHBOARD_PORT=4449.
	if _, ok := rs["mysterium"]; ok {
		t.Error("the v3 dashboard port turned mysterium on")
	}
}

func TestUntouchedTemplatesImportNothing(t *testing.T) {
	for _, p := range []string{"money4band/template.env", "money4band/template-user-config.json", "cashfactory/template.env"} {
		if got := preview(t, fixtures(t, p), "auto", nil).Rows; len(got) != 0 {
			t.Errorf("%s gave %d rows", p, len(got))
		}
	}
	tpl := preview(t, fixtures(t, "internetincome/template.conf"), "internetincome", nil)
	if len(tpl.Rows) != 0 {
		t.Errorf("internetincome template gave %d rows", len(tpl.Rows))
	}
	if sk := skippedKeys(tpl); len(sk) != 1 || sk["DEVICE_NAME"] == "" {
		t.Errorf("blank values and false switches are off; not imported = %v", sk)
	}
}

func TestOnlyTheAppFilledInOnAJSONTemplateComesIn(t *testing.T) {
	f := fixtures(t, "money4band/template-user-config.json")[0]
	f.Content = strings.Replace(f.Content, `"enabled": false, "email": "yourHONEYGAINMail"`, `"enabled": true, "email": "hg@example.com"`, 1)
	rs := rows(preview(t, []File{f}, "auto", nil))
	if _, ok := rs["honeygain"]; !ok || len(rs) != 1 {
		t.Errorf("rows = %v, want honeygain only", rs)
	}
}

func TestOnlyTheServiceFilledInOnATemplateComesIn(t *testing.T) {
	m4b := fixtures(t, "money4band/template.env")[0]
	m4b.Content = strings.NewReplacer("yourHONEYGAINMail", "hg@example.com", "yourHONEYGAINPw", "fake-pass-x").Replace(m4b.Content)
	rs := rows(preview(t, []File{m4b}, "auto", nil))
	if len(rs) != 1 || !rs["honeygain"].Selected || rs["honeygain"].State != RowReady {
		t.Errorf("money4band rows = %+v", rs)
	}
	cf := fixtures(t, "cashfactory/template.env")[0]
	cf.Content = strings.NewReplacer("HONEYGAIN_EMAIL=<email>", "HONEYGAIN_EMAIL=hg@example.com", "HONEYGAIN_PASSWD=<password>", "HONEYGAIN_PASSWD=fake-pass-x").Replace(cf.Content)
	p := preview(t, []File{cf}, "auto", nil)
	var picked []string
	for _, r := range p.Rows {
		if r.Selected {
			picked = append(picked, r.Slug)
		}
	}
	if fmt.Sprint(picked) != "[honeygain]" || rows(p)["honeygain"].State != RowReady {
		t.Errorf("cashfactory picked = %v", picked)
	}
}

func TestPartlyFilledServiceComesInToBeCompleted(t *testing.T) {
	content := "COMPOSE_PROJECT_NAME=money4band\nHONEYGAIN_EMAIL=hg@example.com\nHONEYGAIN_PASSWD=yourHONEYGAINPw\n"
	r := rows(preview(t, []File{{Name: ".env", Content: content}}, "auto", nil))["honeygain"]
	if r.State != RowMissingFields || field(t, r, "HONEYGAIN_PASSWORD").State != FieldPlaceholder {
		t.Errorf("honeygain = %+v", r)
	}
}

// --- CashFactory ---

func TestCashFactoryEnvMapsWithItsOwnKeyNames(t *testing.T) {
	rs := rows(preview(t, fixtures(t, "cashfactory/.env.sample"), "auto", nil))
	checks := []struct{ slug, key, want string }{
		{"earnapp", "EARNAPP_UUID", "sdk-node-0123456789abcdef0123456789abcdef"},
		{"honeygain", "HONEYGAIN_PASSWORD", "fake-pass-honeygain"},
		{"earnfm", "EARNFM_TOKEN", "fake-token-earnfm"},
		{"repocket", "RP_EMAIL", "rp@example.com"},
		{"traffmonetizer", "TRAFFMONETIZER_TOKEN", "fake-token-traffmonetizer"},
		{"iproyal", "IPROYALPAWNS_PASSWORD", "fake-pass-iproyal"},
	}
	for _, c := range checks {
		if got := rs[c.slug].Env[c.key]; got != c.want {
			t.Errorf("%s %s = %q, want %q", c.slug, c.key, got, c.want)
		}
	}
	if !strings.HasPrefix(rs["proxyrack"].Env["UUID"], "ABCDEF") {
		t.Errorf("proxyrack = %v", rs["proxyrack"].Env)
	}
}

// --- InternetIncome ---

var iiFiles = []string{"internetincome/properties.conf", "internetincome/earnapp.txt", "internetincome/proxyrack.txt"}

func TestInternetIncomePropertiesAndStateFilesMap(t *testing.T) {
	p := preview(t, fixtures(t, iiFiles...), "auto", nil)
	rs := rows(p)
	eqEnv(t, "earnapp", rs["earnapp"].Env, map[string]string{"EARNAPP_UUID": "sdk-node-0123456789abcdef0123456789abcdef"})
	eqEnv(t, "proxyrack", rs["proxyrack"].Env, map[string]string{"API_KEY": "fake-key-proxyrack", "UUID": "ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789"})
	eqEnv(t, "proxybase", rs["proxybase"].Env, map[string]string{"ID": "fake-token-proxybase"})
	eqEnv(t, "earnfm", rs["earnfm"].Env, map[string]string{"EARNFM_TOKEN": "fake-token-earnfm"})
	if rs["mysterium"].State != RowReady {
		t.Errorf("mysterium = %s", rs["mysterium"].State)
	}
	// Single quotes keep a literal dollar.
	if got := rs["iproyal"].Env["IPROYALPAWNS_PASSWORD"]; got != "fake-pass-$dollar" {
		t.Errorf("iproyal password = %q", got)
	}
	// An unquoted variable is marked for retyping.
	hg := rs["honeygain"]
	if field(t, hg, "HONEYGAIN_PASSWORD").State != FieldRetype || hg.Env["HONEYGAIN_PASSWORD"] != "" {
		t.Errorf("honeygain = %+v", hg)
	}
	// A required field the tool never had is asked for.
	if rp := rs["repocket"]; field(t, rp, "RP_EMAIL").State != FieldMissing || rp.State != RowMissingFields {
		t.Errorf("repocket = %+v", rp)
	}
	// Extra earnapp lines are reported, never imported.
	if !strings.Contains(strings.Join(p.Notices, "\n"), "1 more device") {
		t.Errorf("notices = %v", p.Notices)
	}
	raw, _ := json.Marshal(p.Rows)
	if strings.Contains(string(raw), "fedcba") {
		t.Error("a proxy copy's device id reached the rows")
	}
}

func TestProxiesListIsRecognisedAndCarriesNoValues(t *testing.T) {
	got := stateFile("proxies.txt", "socks5://user:fake-pass@192.0.2.1:1080\nhttp://192.0.2.2:8080\n")
	if got == nil || got.kind != "proxies" || len(got.env.keys) != 0 || got.extraLines != 2 {
		t.Fatalf("got %+v", got)
	}
	for _, line := range []string{strings.Repeat("0", 20000), "socks5://", "://192.0.2.1:1080", "HTTP://192.0.2.1", "http://a b"} {
		if stateFile("proxies.txt", line+"\n") != nil {
			t.Errorf("%.30q read as a proxies list", line)
		}
	}
}

func TestUnmarkedFileIsReadLikeInternetIncomeWhenAnotherFileSaysSo(t *testing.T) {
	files := append([]File{{Name: "properties.conf", Content: "HONEYGAIN_EMAIL=a@example.com\nHONEYGAIN_PASSWORD=p\\@ss~x\n"}},
		fixtures(t, "internetincome/earnapp.txt")...)
	for _, source := range []string{"auto", "internetincome"} {
		hg := rows(preview(t, files, source, nil))["honeygain"]
		if field(t, hg, "HONEYGAIN_PASSWORD").State != FieldRetype || hg.Env["HONEYGAIN_PASSWORD"] != "" {
			t.Errorf("%s: honeygain = %+v", source, hg)
		}
	}
}

func TestEarnAppOnWithoutItsFileAsksForTheDevice(t *testing.T) {
	r := rows(preview(t, []File{{Name: "properties.conf", Content: "EARNAPP=true\nREPOCKET_API=\n"}}, "internetincome", nil))["earnapp"]
	if r.State != RowMissingFields || !strings.Contains(r.Reason, "earnapp.txt") {
		t.Errorf("earnapp = %s %q", r.State, r.Reason)
	}
}

// --- Values ---

func TestReadEnvValue(t *testing.T) {
	cases := []struct {
		raw   string
		shell bool
		want  string
	}{
		{"plain", false, "plain"},
		{"'sin$gle'", false, "sin$gle"},
		{"'sin$gle'", true, "sin$gle"},
		{`"dou ble"`, false, "dou ble"},
		{"value # comment", false, "value"},
		{"pa#ss", false, "pa#ss"},
		{"", false, ""},
	}
	for _, c := range cases {
		if got := readEnvValue(c.raw, c.shell); got != (Value{Text: c.want}) {
			t.Errorf("readEnvValue(%q, %v) = %+v, want %q", c.raw, c.shell, got, c.want)
		}
	}
}

func TestAnythingAShellOrComposeWouldChangeNeedsRetyping(t *testing.T) {
	cases := []struct {
		raw   string
		shell bool
	}{
		{"$(id)", false}, {"pre$VAR", false}, {"`id`", false}, {`"has $VAR"`, false},
		{`"back\slash"`, false}, {"'unterminated", false}, {`'it'\''s'`, true},
		{"a*b", true}, {"semi;colon", true}, {`back\slash`, true},
	}
	for _, c := range cases {
		if got := readEnvValue(c.raw, c.shell); got != (Value{Retype: true}) {
			t.Errorf("readEnvValue(%q, %v) = %+v, want retype", c.raw, c.shell, got)
		}
	}
}

func TestPlaceholders(t *testing.T) {
	for _, s := range []string{"", "  ", "<email>", "sdk-node-<md5sum>", "yourHONEYGAINMail", "sdk-node-yourEARNAPPDeviceUUID"} {
		if !IsPlaceholder(s) {
			t.Errorf("%q is a placeholder", s)
		}
	}
	for _, s := range []string{"hg@example.com", "your-own-pass", "123456"} {
		if IsPlaceholder(s) {
			t.Errorf("%q is a real value", s)
		}
	}
}

func TestBadEarnAppIDIsInvalidAndShown(t *testing.T) {
	files := []File{{Name: ".env", Content: "_COMPOSE_PROJECT_NAME=cashfactory\nEARNAPP_DEVICE_ID=sdk-node-XYZ\n"}}
	r := rows(preview(t, files, "auto", nil))["earnapp"]
	f := field(t, r, "EARNAPP_UUID")
	if r.State != RowInvalid || f.State != FieldInvalid || f.Value != "sdk-node-XYZ" || len(r.Env) != 0 {
		t.Errorf("earnapp = %+v", r)
	}
}

func TestTypedValueFillsAMissingFieldAndIsChecked(t *testing.T) {
	files := fixtures(t, "internetincome/properties.conf")
	good := rows(preview(t, files, "auto", map[string]map[string]string{"repocket": {"RP_EMAIL": "rp@example.com"}}))["repocket"]
	if good.State != RowReady || good.Env["RP_EMAIL"] != "rp@example.com" {
		t.Errorf("repocket = %+v", good)
	}
	bad := rows(preview(t, files, "auto", map[string]map[string]string{"earnapp": {"EARNAPP_UUID": "nope"}}))["earnapp"]
	if bad.State != RowInvalid {
		t.Errorf("earnapp = %s", bad.State)
	}
}

// THE RULE: a secret's value is in no field entry and in no JSON the frontend gets.
// The control is Env: the secret is there, so its absence elsewhere is the masking.
func TestASecretIsNeverInAFieldOrTheJSON(t *testing.T) {
	p := preview(t, fixtures(t, "money4band/user-config.json"), "auto", nil)
	if rows(p)["honeygain"].Env["HONEYGAIN_PASSWORD"] != "fake-pass-honeygain" {
		t.Fatal("control: the secret was never read, so the checks below prove nothing")
	}
	for _, r := range p.Rows {
		for _, f := range r.Fields {
			if f.Secret && f.Value != "" {
				t.Errorf("%s.%s carries its value", r.Slug, f.Key)
			}
		}
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"fake-pass-honeygain", "fake-pass-$iproyal", "fake-pass-bitping", "fake-pass-grass"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("the preview JSON holds %q", secret)
		}
	}
}

// --- Which tool ---

func TestEachToolIsRecognised(t *testing.T) {
	cases := map[string]string{
		"money4band/user-config.json":    "money4band",
		"money4band/generated.env":       "money4band",
		"money4band/v3.env":              "money4band",
		"cashfactory/.env.sample":        "cashfactory",
		"internetincome/properties.conf": "internetincome",
		"internetincome/earnapp.txt":     "internetincome",
	}
	for path, tool := range cases {
		read, err := ReadFiles(fixtures(t, path), "auto")
		if err != nil {
			t.Fatal(err)
		}
		if got, err := DetectSource(read, "auto"); err != nil || got != tool {
			t.Errorf("%s: %q %v, want %s", path, got, err, tool)
		}
	}
}

func TestDetectionRefusals(t *testing.T) {
	detect := func(files []File, source string) error {
		read, err := ReadFiles(files, source)
		if err != nil {
			return err
		}
		_, err = DetectSource(read, source)
		return err
	}
	refusedWith(t, detect(fixtures(t, "money4band/user-config.json", "cashfactory/.env.sample"), "auto"), "different tools")
	refusedWith(t, detect([]File{{Name: ".env", Content: "COMPOSE_PROJECT_NAME=money4band\n_COMPOSE_PROJECT_NAME=cashfactory\nPACKETSTREAM_CID=FakeCid1\n"}}, "auto"), "more than one tool")
	refusedWith(t, detect([]File{{Name: "x", Content: "HONEYGAIN_EMAIL=a@example.com\n"}}, "auto"), "Pick the tool")
	refusedWith(t, detect(fixtures(t, "cashfactory/.env.sample"), "money4band"), "CashFactory")
	if err := detect([]File{{Name: "x", Content: "HONEYGAIN_EMAIL=a@example.com\nHONEYGAIN_PASSWD=fake-pass-x\n"}}, "cashfactory"); err != nil {
		t.Errorf("the owner can pick for an unmarked file: %v", err)
	}
	if _, err := Run(fixtures(t, "cashfactory/.env.sample"), "nope", getService(t), nil); err == nil {
		t.Error("an unknown tool was accepted")
	}
}

func TestNoTwoToolsShareAMarker(t *testing.T) {
	seen := map[string]string{}
	for _, name := range sources.Tools.Keys {
		for _, key := range sources.Tools.Values[name].Markers.Keys {
			if other, ok := seen[key]; ok {
				t.Errorf("%s marks both %s and %s", key, other, name)
			}
			seen[key] = name
		}
	}
}

// --- Hostile input ---

func TestHostileInputIsRefused(t *testing.T) {
	many := make([]File, 5)
	for i := range many {
		many[i] = File{Name: fmt.Sprint(i), Content: "A=1"}
	}
	keys := map[string]int{}
	for i := 0; i < 1200; i++ {
		keys[fmt.Sprintf("k%d", i)] = 1
	}
	manyKeys, _ := json.MarshalIndent(keys, "", "")
	cases := []struct {
		name   string
		files  []File
		substr string
	}{
		{"no files", nil, "at least one"},
		{"too many files", many, "At most"},
		{"too big", []File{{Name: "big", Content: "A=" + strings.Repeat("x", MaxTotalBytes)}}, "KiB"},
		{"too many lines", []File{{Name: "f", Content: strings.Repeat("\n", 6000)}}, "lines"},
		{"a long line", []File{{Name: "f", Content: "A=" + strings.Repeat("x", 9000)}}, "line longer"},
		{"a NUL byte", []File{{Name: "f", Content: "A=1\x00"}}, "not a text file"},
		{"invalid UTF-8", []File{{Name: "f", Content: "A=\xff"}}, "UTF-8"},
		{"json 25 deep", []File{{Name: "f.json", Content: strings.Repeat(`{"a":`, 25) + "1" + strings.Repeat("}", 25)}}, "nested"},
		{"json 5000 deep", []File{{Name: "f.json", Content: strings.Repeat(`{"a":`, 5000) + "1" + strings.Repeat("}", 5000)}}, ""}, // the line cap or the depth cap, either refuses
		{"too many json keys", []File{{Name: "f.json", Content: string(manyKeys)}}, "keys"},
		{"broken json", []File{{Name: "f.json", Content: "{"}}, "not valid JSON"},
		{"json with a tail", []File{{Name: "f.json", Content: `{"a":1} {"b":2}`}}, "not valid JSON"},
	}
	for _, c := range cases {
		_, err := ReadFiles(c.files, "auto")
		if err == nil {
			t.Errorf("%s: accepted", c.name)
			continue
		}
		var r *Refused
		if !errors.As(err, &r) || !strings.Contains(err.Error(), c.substr) {
			t.Errorf("%s: %v, want a refusal with %q", c.name, err, c.substr)
		}
		if strings.Contains(err.Error(), "xxxx") {
			t.Errorf("%s: the refusal quotes the file", c.name)
		}
	}
	// A depth of 20 is still fine.
	if _, err := ReadFiles([]File{{Name: "f.json", Content: strings.Repeat(`{"a":`, 20) + "1" + strings.Repeat("}", 20)}}, "auto"); err != nil {
		t.Errorf("depth 20 refused: %v", err)
	}
}

func TestBOMCRLFAndOddLinesAreFine(t *testing.T) {
	for _, content := range []string{
		"\ufeff_COMPOSE_PROJECT_NAME=cashfactory\r\nPACKETSTREAM_CID=FakeCid1\r\n",
		"_COMPOSE_PROJECT_NAME=cashfactory\nnot a setting\n1BAD=x\n=x\nexport PACKETSTREAM_CID=FakeCid1\n",
	} {
		r := rows(preview(t, []File{{Name: ".env", Content: content}}, "auto", nil))["packetstream"]
		eqEnv(t, "packetstream", r.Env, map[string]string{"CID": "FakeCid1"})
	}
}

func TestOddJSONValuesAreNotImported(t *testing.T) {
	huge := `{"apps": {"honeygain": {"enabled": true, "email": "a@example.com", "password": "` + strings.Repeat("x", 5000) + `"}}}`
	r := rows(preview(t, []File{{Name: "u.json", Content: huge}}, "auto", nil))["honeygain"]
	if field(t, r, "HONEYGAIN_PASSWORD").State != FieldRetype {
		t.Errorf("a huge JSON value: %+v", field(t, r, "HONEYGAIN_PASSWORD"))
	}
	odd := `{"apps": {"honeygain": {"enabled": true, "email": ["a"], "password": {"x": 1}}, "earnapp": "nope"}}`
	r = rows(preview(t, []File{{Name: "u.json", Content: odd}}, "auto", nil))["honeygain"]
	if len(r.Env) != 0 {
		t.Errorf("non-string JSON leaves were imported: %v", r.Env)
	}
}

// --- The mapping stays true to the catalog ---

// Every slug and env key sources.yml names exists in the vendored catalog, so a
// renamed field fails here instead of importing nothing.
func TestEveryMappedSlugAndFieldIsInTheCatalog(t *testing.T) {
	get := getService(t)
	for _, tool := range sources.Tools.Keys {
		spec := sources.Tools.Values[tool]
		for _, appName := range spec.Apps.Keys {
			app := spec.Apps.Values[appName]
			svc, ok := get(app.Slug)
			if !ok {
				t.Errorf("%s.%s: no catalog service %s", tool, appName, app.Slug)
				continue
			}
			env := map[string]bool{}
			for _, v := range svc.Docker.Env {
				env[v.Key] = true
			}
			for _, key := range app.Fields.Keys {
				if !env[key] {
					t.Errorf("%s.%s: %s has no env %s", tool, appName, app.Slug, key)
				}
			}
		}
	}
	if len(sources.Tools.Keys) != len(Sources) {
		t.Errorf("sources.yml has %d tools, Sources lists %d", len(sources.Tools.Keys), len(Sources))
	}
}

func TestEveryAppIsNamedInTheToolsGuide(t *testing.T) {
	for _, tool := range sources.Tools.Keys {
		page, err := os.ReadFile(filepath.Join("..", "..", "docs", "migrate", tool+".md"))
		if err != nil {
			t.Fatal(err)
		}
		for _, appName := range sources.Tools.Values[tool].Apps.Keys {
			if !strings.Contains(string(page), "`"+appName+"`") {
				t.Errorf("docs/migrate/%s.md does not mention %s", tool, appName)
			}
		}
	}
}

// A JSON "\u0000" passes the file's NUL check (the raw text has none) and decodes
// to a NUL, which no env value can carry: the field is invalid, never ready.
func TestADecodedNulIsAnInvalidValue(t *testing.T) {
	content := `{"apps": {"honeygain": {"enabled": true, "email": "hg@example.com", "password": "a\u0000b"}}}` // ggignore: fake test value
	r := rows(preview(t, []File{{Name: "user-config.json", Content: content}}, "money4band", nil))["honeygain"]
	if r.State == RowReady || field(t, r, "HONEYGAIN_PASSWORD").State != FieldInvalid {
		t.Errorf("honeygain = %+v", r)
	}
}

// Desktop deploys a saved value as it is, so a quoted value with spaces at an end
// is asked for again instead of being trimmed into a different password.
func TestAValueWithSpacesAtAnEndNeedsRetyping(t *testing.T) {
	content := "HONEYGAIN_EMAIL=hg@example.com\nHONEYGAIN_PASSWD=' padded '\n" // ggignore: fake test value
	r := rows(preview(t, []File{{Name: ".env", Content: content}}, "cashfactory", nil))["honeygain"]
	if f := field(t, r, "HONEYGAIN_PASSWORD"); f.State != FieldRetype {
		t.Errorf("password state = %q, want retype", f.State)
	}
	if r.State == RowReady {
		t.Errorf("honeygain is ready with a guessed value: %+v", r)
	}
}
