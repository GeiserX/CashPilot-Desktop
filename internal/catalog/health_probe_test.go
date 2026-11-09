package catalog

import (
	"encoding/json"
	"testing"
	"testing/fstest"
)

// TestHealthProbeMarshalsToJSON. yaml.v3 decodes a nested mapping with an unquoted
// numeric key into map[any]any, which encoding/json refuses; the service list then
// cannot reach the UI and the app shows nothing.
func TestHealthProbeMarshalsToJSON(t *testing.T) {
	fsys := fstest.MapFS{"services/x/a.yml": {Data: []byte(`
name: A
slug: a
docker:
  image: a:1
  health_probe:
    command: [myst, status]
    checks:
      - expect:
          200: ok
          state: {1: up}
`)}}
	cat, err := LoadEmbedded(fsys)
	if err != nil {
		t.Fatal(err)
	}
	svc := mustGet(t, cat, "a")
	out, err := json.Marshal(svc)
	if err != nil {
		t.Fatalf("service with a numeric-keyed health_probe must marshal to JSON: %v", err)
	}
	var back struct {
		Docker struct {
			HealthProbe map[string]any `json:"healthProbe"`
		} `json:"docker"`
	}
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	expect := back.Docker.HealthProbe["checks"].([]any)[0].(map[string]any)["expect"].(map[string]any)
	if expect["200"] != "ok" || expect["state"].(map[string]any)["1"] != "up" {
		t.Errorf("probe keys lost on the way through: %v", expect)
	}
}

// TestHealthProbeNonFiniteFloatMarshals. YAML .nan, .inf and -.inf decode to
// float64 NaN and Inf, which encoding/json refuses just like map[any]any.
func TestHealthProbeNonFiniteFloatMarshals(t *testing.T) {
	fsys := fstest.MapFS{"services/x/a.yml": {Data: []byte(`
name: A
slug: a
docker:
  image: a:1
  health_probe:
    threshold: .nan
    checks:
      - {max: .inf, min: -.inf, ok: 1.5}
`)}}
	cat, err := LoadEmbedded(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(cat.ListVisible()); err != nil {
		t.Fatalf("service list with a non-finite health_probe float must marshal to JSON: %v", err)
	}
	p := mustGet(t, cat, "a").Docker.HealthProbe
	c := p["checks"].([]any)[0].(map[string]any)
	if p["threshold"] != "NaN" || c["max"] != "+Inf" || c["min"] != "-Inf" || c["ok"] != 1.5 {
		t.Errorf("probe floats not normalised as expected: %v", p)
	}
}
