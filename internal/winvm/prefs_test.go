package winvm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goodPrefs = `{"keepOff":true,"approvedNetworks":[{"gatewayMac":"00-00-5E-00-53-01","profileId":"{6A5D2C1E-0B7F-4C3A-9E21-5F8D7C6B4A30}"}],"uploadLimitMbps":20}`

func TestParsePrefs(t *testing.T) {
	p, err := ParsePrefs([]byte(goodPrefs))
	if err != nil {
		t.Fatal(err)
	}
	if !p.KeepOff || len(p.ApprovedNetworks) != 1 || p.UploadLimitMbps != 20 {
		t.Fatalf("got %+v", p)
	}
	if p, err := ParsePrefs([]byte(`{}`)); err != nil || p.KeepOff || len(p.ApprovedNetworks) != 0 {
		t.Fatalf("empty prefs: %+v %v", p, err)
	}
}

// The user can write this file, so it is read strictly: anything it does not
// know is refused, not ignored.
func TestParsePrefsStrict(t *testing.T) {
	edit := func(old, nu string) string { return strings.Replace(goodPrefs, old, nu, 1) }
	bad := map[string]string{
		"an unknown key":               edit(`"keepOff":true`, `"keepOff":true,"allowLan":true`),
		"an unknown key in a network":  edit(`"gatewayMac"`, `"metered":false,"gatewayMac"`),
		"a network with no profile":    edit(`"profileId":"{6A5D2C1E-0B7F-4C3A-9E21-5F8D7C6B4A30}"`, `"profileId":""`),
		"a network with a bad MAC":     edit(`00-00-5E-00-53-01`, `not-a-mac`),
		"a negative upload limit":      edit(`20`, `-1`),
		"an upload limit over the cap": edit(`20`, `100001`),
		"trailing data":                goodPrefs + ` {}`,
		"a wrong type":                 edit(`"keepOff":true`, `"keepOff":"yes"`),
		"over the 16 KB cap":           `{"approvedNetworks":[` + strings.Repeat(` `, maxPrefsBytes) + `]}`,
		"not JSON":                     `keepOff=true`,
	}
	for name, b := range bad {
		if _, err := ParsePrefs([]byte(b)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// A network approved in prefs still never earns while it is metered.
func TestPrefsNeverApproveAMeteredNetwork(t *testing.T) {
	p, err := ParsePrefs([]byte(goodPrefs))
	if err != nil {
		t.Fatal(err)
	}
	cur := CurrentNetwork{ID: p.ApprovedNetworks[0], Metered: true}
	if ok, why := NetworkOK(cur, p.ApprovedNetworks); ok || why != NetworkMetered {
		t.Fatalf("an approved metered network: %v %q", ok, why)
	}
	cur.Metered = false
	if ok, _ := NetworkOK(cur, p.ApprovedNetworks); !ok {
		t.Fatal("the approved network is refused when not metered")
	}
}

// No file is the defaults: nothing approved.
func TestLoadPrefs(t *testing.T) {
	dir := t.TempDir()
	p, err := LoadPrefs(dir)
	if err != nil || p.KeepOff || len(p.ApprovedNetworks) != 0 {
		t.Fatalf("no file: %+v %v", p, err)
	}
	os.WriteFile(filepath.Join(dir, PrefsFile), []byte(goodPrefs), 0o644)
	if p, err := LoadPrefs(dir); err != nil || !p.KeepOff {
		t.Fatalf("a good file: %+v %v", p, err)
	}
	os.WriteFile(filepath.Join(dir, PrefsFile), []byte(`{"keepOff":false,"x":1}`), 0o644)
	if _, err := LoadPrefs(dir); err == nil {
		t.Fatal("a bad file was accepted")
	}
}
