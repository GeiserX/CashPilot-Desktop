package catalog

import (
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
