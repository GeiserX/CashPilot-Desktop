package catalog

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"gopkg.in/yaml.v3"
)

// The web catalog rejects a health signal whose state it does not know; the loader
// here drops it instead of refusing the entry, so the app still starts. This test
// holds the vendored files to the web's rule, before normalize can hide a bad one.
func TestEveryVendoredSignalStateIsOneTheReadersKnow(t *testing.T) {
	root := filepath.Join("..", "..", "services")
	var signals, disabled int
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || !strings.HasSuffix(path, ".yml") || strings.HasPrefix(entry.Name(), "_") {
			return walkErr
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var svc Service
		if err := yaml.NewDecoder(bytes.NewReader(raw)).Decode(&svc); err != nil {
			return err
		}
		if svc.SupportURL != "" && !strings.HasPrefix(svc.SupportURL, "https://") {
			t.Errorf("%s: support_url must be an https:// link, got %q", svc.Slug, svc.SupportURL)
		}
		for i, s := range svc.Docker.HealthSignals {
			signals++
			s.State = strings.ToLower(strings.TrimSpace(s.State))
			if s.State == SignalAccountDisabled {
				disabled++
			}
			if !ValidSignal(s) {
				t.Errorf("%s: docker.health_signals[%d] is not one the web catalog accepts: %+v", svc.Slug, i, s)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if signals == 0 || disabled == 0 {
		t.Fatalf("checked %d signals, %d account_disabled; this test would pass by checking nothing", signals, disabled)
	}
}

func TestIPRoyalDeclaresAccountDisabledAndItsSupportPage(t *testing.T) {
	svc := mustGet(t, realCatalog(t), "iproyal")
	if !strings.HasPrefix(svc.SupportURL, "https://") {
		t.Errorf("iproyal support_url = %q, want an https:// link", svc.SupportURL)
	}
	var found bool
	for _, s := range svc.Docker.HealthSignals {
		found = found || s.State == SignalAccountDisabled
	}
	if !found {
		t.Errorf("iproyal must keep its account_disabled signal, got %+v", svc.Docker.HealthSignals)
	}
}

func TestMysteriumNodeReadParses(t *testing.T) {
	nr := mustGet(t, realCatalog(t), "mysterium").Collector.NodeRead
	if nr == nil {
		t.Fatal("mysterium must declare collector.node_read")
	}
	if len(nr.Command) == 0 || nr.Node == "" || nr.Balance == "" || nr.Lifetime == "" || nr.SettledNet == "" || nr.SettledFees == "" {
		t.Errorf("mysterium node_read lost a field at load: %+v", nr)
	}
}

// TestLoaderDropsSignalsTheWebRejects. An unknown state used to be read as failing,
// accusing a service of a problem its entry never declared.
func TestLoaderDropsSignalsTheWebRejects(t *testing.T) {
	fsys := fstest.MapFS{"services/x/a.yml": {Data: []byte(`
name: A
slug: a
support_url: http://example.com/help
docker:
  image: a:1
  health_signals:
    - {pattern: "kept failing", state: Failing}
    - {pattern: "kept default"}
    - {pattern: "kept disabled", state: " ACCOUNT_DISABLED "}
    - {pattern: "dropped unknown", state: broken}
    - {pattern: "", state: account_disabled}
    - {pattern: "dropped windowed", state: account_disabled, window_hours: 2}
    - {pattern: "(", state: account_disabled}
`)}}
	cat, err := LoadEmbedded(fsys)
	if err != nil {
		t.Fatal(err)
	}
	svc := mustGet(t, cat, "a")
	if svc.SupportURL != "" {
		t.Errorf("a non-https support_url must be cleared, got %q", svc.SupportURL)
	}
	var got []string
	for _, s := range svc.Docker.HealthSignals {
		got = append(got, s.Pattern+"="+s.State)
	}
	want := "kept failing=failing,kept default=,kept disabled=account_disabled"
	if strings.Join(got, ",") != want {
		t.Errorf("signals after load = %v, want %s", got, want)
	}
}
