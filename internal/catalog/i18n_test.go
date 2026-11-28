package catalog

import (
	"io/fs"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

const i18nService = `name: Demo
slug: demo
category: bandwidth
status: active
description: |
  Shares your bandwidth.
docker:
  image: example/demo:1.0.0
  env:
    - key: DEMO_EMAIL
      label: Email
      description: Your account email.
  health_signals:
    - pattern: "denied"
      means: The account is blocked.
obligations:
  - id: withdraw-yearly
    action: withdraw
    title: Withdraw once a year
    loss: The balance expires.
disclosure:
  sells: Your IP address.
`

func i18nFS(es string) fstest.MapFS {
	return fstest.MapFS{
		"services/bandwidth/demo.yml": {Data: []byte(i18nService)},
		"services/_i18n/en.yml": {Data: []byte(`demo.description: "Shares your bandwidth.\n"
demo.docker.env.DEMO_EMAIL.label: Email
demo.docker.env.DEMO_EMAIL.description: Your account email.
demo.docker.health_signals.0.means: The account is blocked.
demo.obligations.withdraw-yearly.title: Withdraw once a year
demo.disclosure.sells: Your IP address.
`)},
		"services/_i18n/es.yml": {Data: []byte(es)},
	}
}

func TestLocalizedTranslatesTheCatalogText(t *testing.T) {
	h := func(s string) string { return sourceHash(s) }
	es := `demo.description: {text: "Comparte tu ancho de banda.\n", source: "` + h("Shares your bandwidth.\n") + `"}
demo.docker.env.DEMO_EMAIL.label: {text: "Correo", source: "` + h("Email") + `"}
demo.docker.env.DEMO_EMAIL.description: {text: "Tu correo.", source: "000000000000"}
demo.docker.health_signals.0.means: {text: "La cuenta está bloqueada.", source: "` + h("The account is blocked.") + `"}
demo.obligations.withdraw-yearly.title: {text: "Retira una vez al año", source: "` + h("Withdraw once a year") + `"}
demo.disclosure.sells: {text: "", source: "` + h("Your IP address.") + `"}
`
	cat, err := LoadEmbedded(i18nFS(es))
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.List()) != 1 {
		t.Fatalf("the _i18n files must not load as services, got %d entries", len(cat.List()))
	}
	english, _ := cat.Get("demo")
	svc := cat.LocalizedOne(english, "es")

	if svc.Description != "Comparte tu ancho de banda.\n" {
		t.Errorf("description = %q", svc.Description)
	}
	if got := svc.Docker.Env[0].Label; got != "Correo" {
		t.Errorf("env label = %q", got)
	}
	if got := svc.Docker.Env[0].Description; got != "Your account email." {
		t.Errorf("a stale translation must show English, got %q", got)
	}
	if got := svc.Docker.HealthSignals[0].Means; got != "La cuenta está bloqueada." {
		t.Errorf("health signal = %q", got)
	}
	if got := svc.Obligations[0].Title; got != "Retira una vez al año" {
		t.Errorf("obligation title = %q", got)
	}
	if got := svc.Disclosure.Sells; got != "Your IP address." {
		t.Errorf("an empty translation must show English, got %q", got)
	}

	// The catalog's own entry, and the value we started from, are still English.
	again, _ := cat.Get("demo")
	for _, s := range []Service{english, again} {
		if s.Docker.Env[0].Label != "Email" || s.Obligations[0].Title != "Withdraw once a year" || s.Docker.HealthSignals[0].Means != "The account is blocked." {
			t.Fatalf("Localized changed the English entry: %+v", s.Docker.Env)
		}
	}
	if got := cat.Localized([]Service{english}, "de"); got[0].Description != english.Description {
		t.Errorf("a locale with no file must show English")
	}
}

// openLog records every _i18n file a catalog opens.
type openLog struct {
	fs.FS
	mu     sync.Mutex
	opened []string
}

func (o *openLog) Open(name string) (fs.File, error) {
	if strings.Contains(name, I18nDir+"/") {
		o.mu.Lock()
		o.opened = append(o.opened, name)
		o.mu.Unlock()
	}
	return o.FS.Open(name)
}

func (o *openLog) files() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.opened)
}

// Loading the catalog must not parse the translations: the 19 locale files took
// about a second under the race detector, on every Load, and pushed the test
// suite past its 10 minute limit. A locale's file is read when a screen first
// asks for it, and only once.
func TestTranslationsAreReadOnFirstUseOnly(t *testing.T) {
	h := sourceHash("Shares your bandwidth.\n")
	log := &openLog{FS: i18nFS(`demo.description: {text: "Comparte.", source: "` + h + `"}` + "\n")}
	log.FS.(fstest.MapFS)["services/_i18n/de.yml"] = &fstest.MapFile{Data: []byte("{}")}

	cat, err := LoadEmbedded(log)
	if err != nil {
		t.Fatal(err)
	}
	if got := log.files(); len(got) != 0 {
		t.Fatalf("Load read translation files %v; it must read none", got)
	}

	english, _ := cat.Get("demo")
	if got := cat.LocalizedOne(english, "es").Description; got != "Comparte." {
		t.Fatalf("description = %q", got)
	}
	cat.LocalizedOne(english, "es")
	cat.Localized([]Service{english}, "en")
	want := []string{"services/_i18n/en.yml", "services/_i18n/es.yml"}
	if got := log.files(); !slices.Equal(got, want) {
		t.Fatalf("read %v; want only %v, each once", got, want)
	}
}

// The loader drops a health signal the web catalog rejects, so the signals after
// it move up one index. A translation keyed by the old index must not land on the
// signal that now sits there: a field takes a translation only while it holds the
// English that the translation was made from.
func TestATranslationNeverLandsOnAnotherItemAfterTheLoaderDropsOne(t *testing.T) {
	svc := `name: Demo
slug: demo
category: bandwidth
status: active
description: Shares your bandwidth.
docker:
  image: example/demo:1.0.0
  health_signals:
    - pattern: "stuck"
      means: The node is stuck.
      restart: true
    - pattern: "denied"
      means: The account is blocked.
`
	h := sourceHash
	fsys := fstest.MapFS{
		"services/bandwidth/demo.yml": {Data: []byte(svc)},
		"services/_i18n/en.yml": {Data: []byte(`demo.docker.health_signals.0.means: The node is stuck.
demo.docker.health_signals.1.means: The account is blocked.
`)},
		"services/_i18n/es.yml": {Data: []byte(`demo.docker.health_signals.0.means: {text: "El nodo está atascado.", source: "` + h("The node is stuck.") + `"}
demo.docker.health_signals.1.means: {text: "La cuenta está bloqueada.", source: "` + h("The account is blocked.") + `"}
`)},
	}
	cat, err := LoadEmbedded(fsys)
	if err != nil {
		t.Fatal(err)
	}
	english, _ := cat.Get("demo")
	if len(english.Docker.HealthSignals) != 1 {
		t.Fatalf("the loader should drop the restart signal with no window, got %d signals", len(english.Docker.HealthSignals))
	}
	if got := cat.LocalizedOne(english, "es").Docker.HealthSignals[0].Means; got != "The account is blocked." {
		t.Fatalf("the remaining signal says %q; the old index 0 translation belongs to the dropped signal, so it must stay English", got)
	}
}
