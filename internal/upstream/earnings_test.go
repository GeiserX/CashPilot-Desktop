package upstream

// ParseEarnings, tested in the package that owns it.
//
// These started life in package main, where they exercised the function
// perfectly and counted for NOTHING: `go test ./...` measures coverage
// per-package, so a call from another package leaves this one reading 0%. The
// coverage gate caught it, which is the gate working.

import (
	"encoding/json"
	"testing"
)

func TestParseEarnings(t *testing.T) {
	t.Run("nothing sent is UNKNOWN, not an error", func(t *testing.T) {
		// The normal case for a server too old to report, and for a worker it
		// can produce no figures for.
		for _, raw := range []string{"", "null", "  "} {
			got, err := ParseEarnings(json.RawMessage(raw))
			if err != nil || got != nil {
				t.Fatalf("ParseEarnings(%q) = %v, %v", raw, got, err)
			}
		}
	})

	t.Run("something unreadable IS an error", func(t *testing.T) {
		// Silence is normal; a server sending gibberish is not, and swallowing
		// it would hide a version mismatch behind an empty panel.
		if _, err := ParseEarnings(json.RawMessage(`{"platforms": 7}`)); err == nil {
			t.Fatal("a malformed earnings block was accepted")
		}
	})

	t.Run("a missing total stays nil", func(t *testing.T) {
		got, err := ParseEarnings(json.RawMessage(`{"window_days":30,"platforms":[]}`))
		if err != nil {
			t.Fatal(err)
		}
		if got.TotalUSD != nil {
			t.Fatalf("an absent total became %v", *got.TotalUSD)
		}
	})

	t.Run("an explicit zero is kept as zero", func(t *testing.T) {
		// The mirror of the rule: a real measured 0.00 must not be turned into
		// "unknown" either. A guard that flags everything is as useless as none.
		got, err := ParseEarnings(json.RawMessage(`{"total_usd":0,"platforms":[{"slug":"grass","usd":0}]}`))
		if err != nil {
			t.Fatal(err)
		}
		if got.TotalUSD == nil || *got.TotalUSD != 0 {
			t.Fatalf("a measured zero was lost: %v", got.TotalUSD)
		}
		if got.Platforms[0].USD == nil || *got.Platforms[0].USD != 0 {
			t.Fatalf("a measured per-platform zero was lost: %v", got.Platforms[0].USD)
		}
	})
}
