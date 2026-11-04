package catalog

import (
	"bytes"
	"testing"

	"gopkg.in/yaml.v3"
)

// The web catalog carries a `rating:` block (the evidence behind each provider's
// grade). Desktop does not show grades yet, so the strict decoder must accept the
// block without Desktop modelling it, or the next catalog sync fails on every
// rated service.
func TestARatingBlockDecodesStrictlyAndIsIgnored(t *testing.T) {
	raw := []byte(`name: X
slug: x
rating:
  reviewed_on: "2026-11-04"
  verdicts:
    payout_record: good
  cons:
  - text: A cited fact.
    criterion: payout_record
`)
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var svc Service
	if err := decoder.Decode(&svc); err != nil {
		t.Fatalf("a rating block broke strict decoding: %v", err)
	}
	if svc.Slug != "x" {
		t.Fatalf("decoding stopped early: slug %q", svc.Slug)
	}
}
