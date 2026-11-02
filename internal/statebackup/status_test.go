package statebackup

import (
	"testing"
	"time"
)

func TestJudge(t *testing.T) {
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	recent := now.Add(-24 * time.Hour)
	old := now.Add(-MaxAge - time.Hour)
	conf := func(id string, at time.Time) *Confirmation {
		return &Confirmation{Slug: "mysterium", StateID: id, Method: "manual", ConfirmedAt: at}
	}
	for _, tc := range []struct {
		name  string
		found *Confirmation
		live  string
		want  string
	}{
		{"never confirmed", nil, "aaaa", StateNone},
		{"same state, recent", conf("aaaa", recent), "aaaa", StateConfirmed},
		{"volume recreated", conf("aaaa", recent), "bbbb", StateChanged},
		{"older than six months", conf("aaaa", old), "aaaa", StateExpired},
		{"confirmed blind, now readable", conf("", recent), "aaaa", StateRecheck},
		{"unreadable now, judged on age", conf("aaaa", recent), "", StateConfirmed},
		{"unreadable now and old", conf("aaaa", old), "", StateExpired},
		{"unreadable time", conf("aaaa", time.Time{}), "aaaa", StateExpired},
	} {
		if got := Judge(tc.found, tc.live, now); got != tc.want {
			t.Errorf("%s: Judge = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The fingerprint follows where the state lives, not the container that uses it:
// recreating the container over the same volume (every image update) keeps it, a
// volume removed and made again changes it.
func TestFingerprint(t *testing.T) {
	vol := func(created string) map[string]MountFact {
		return map[string]MountFact{"/var/lib/mysterium-node": {Mounted: true, Type: "volume", Name: "myst-data", Created: created}}
	}
	a := Fingerprint(vol("2026-01-01T00:00:00Z"), "container-1")
	if len(a) != 16 {
		t.Fatalf("fingerprint %q is not 16 hex characters", a)
	}
	if b := Fingerprint(vol("2026-01-01T00:00:00Z"), "container-2"); b != a {
		t.Errorf("a new container over the same volume changed the fingerprint")
	}
	if b := Fingerprint(vol("2026-06-01T00:00:00Z"), "container-1"); b == a {
		t.Errorf("a recreated volume kept the fingerprint")
	}
	bind := func(src string) map[string]MountFact {
		return map[string]MountFact{"/app/identity": {Mounted: true, Type: "bind", Source: src}}
	}
	if Fingerprint(bind("/srv/a"), "c") == Fingerprint(bind("/srv/b"), "c") {
		t.Errorf("a moved bind folder kept the fingerprint")
	}
	layer := map[string]MountFact{"/app/identity": {}}
	if Fingerprint(layer, "c1") == Fingerprint(layer, "c2") {
		t.Errorf("state in the container layer must follow the container")
	}
	if Fingerprint(nil, "c") != "" {
		t.Errorf("no critical targets must give no fingerprint")
	}
	// The web's recipe, computed by hand: sha256("<target>|volume|<name>|<created>")[:16].
	if got, want := Fingerprint(vol("X"), "c"), "a798e9ba2996252c"; got != want {
		t.Errorf("fingerprint = %s, want %s", got, want)
	}
}
