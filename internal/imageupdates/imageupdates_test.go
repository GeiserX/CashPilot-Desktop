package imageupdates

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/config"
)

// fake is a Hooks double: a registry of builds, a history and a deploy that can fail.
type fake struct {
	checks   map[string]Check
	checkErr map[string]error
	blocker  map[string]string
	// undo makes the deploy of a slug fail its check period; fail makes it fail outright.
	undo, fail map[string]bool
	checked    []string
	deployed   []string
	installs   []string
	rows       []Record
	slept      []time.Duration
}

func (f *fake) hooks() Hooks {
	return Hooks{
		Check: func(_ context.Context, slug string) (Check, bool, error) {
			f.checked = append(f.checked, slug)
			if err := f.checkErr[slug]; err != nil {
				return Check{}, false, err
			}
			c, ok := f.checks[slug]
			return c, ok, nil
		},
		Blocker: func(_ context.Context, slug, _ string) string { return f.blocker[slug] },
		Deploy: func(_ context.Context, slug, containerID, newImage string, checkedAt time.Time) (DeployResult, bool, error) {
			f.deployed = append(f.deployed, slug+"@"+containerID)
			f.installs = append(f.installs, newImage)
			if checkedAt.IsZero() {
				return DeployResult{}, false, errors.New("no check time")
			}
			switch {
			case f.undo[slug]:
				return DeployResult{}, true, errors.New("it stopped within 120 seconds")
			case f.fail[slug]:
				return DeployResult{}, false, errors.New("the registry did not answer")
			}
			c := f.checks[slug]
			// The container now runs the new build.
			c.RunningID, c.RunningDigest = c.LatestID, c.LatestDigest
			f.checks[slug] = c
			return DeployResult{NewImage: c.LatestID}, false, nil
		},
		Record: func(r Record) error { f.rows = append(f.rows, r); return nil },
		Seen: func(slug, image string, results ...string) (bool, error) {
			for _, r := range f.rows {
				if r.Slug == slug && r.NewImage == image && slices.Contains(results, r.Result) {
					return true, nil
				}
			}
			return false, nil
		},
		Sleep: func(_ context.Context, d time.Duration) { f.slept = append(f.slept, d) },
	}
}

func moved(slug string) Check {
	return Check{Floating: true, LatestID: "sha256:new-" + slug, LatestDigest: "sha256:dnew", RunningID: "sha256:old-" + slug, RunningDigest: "sha256:dold", ContainerID: "c-" + slug, Status: "running"}
}

func newFake(checks map[string]Check) *fake {
	return &fake{checks: checks, checkErr: map[string]error{}, blocker: map[string]string{}, undo: map[string]bool{}, fail: map[string]bool{}}
}

func update(t *testing.T, f *fake, slug, mode string) string {
	t.Helper()
	got, err := UpdateService(context.Background(), f.hooks(), slug, mode)
	if err != nil {
		t.Fatalf("UpdateService(%s, %s): %v", slug, mode, err)
	}
	return got
}

func TestOffChecksNothing(t *testing.T) {
	f := newFake(map[string]Check{"a": moved("a")})
	if got := update(t, f, "a", Off); got != "off" || len(f.checked) != 0 {
		t.Fatalf("off = %q, checked %v; want no check at all", got, f.checked)
	}
}

func TestAnUnchangedBuildChangesNothing(t *testing.T) {
	c := moved("a")
	c.RunningID, c.RunningDigest = c.LatestID, c.LatestDigest
	f := newFake(map[string]Check{"a": c})
	for range 3 {
		if got := update(t, f, "a", Auto); got != "current" {
			t.Fatalf("got %q, want current", got)
		}
	}
	if len(f.deployed) != 0 || len(f.rows) != 0 {
		t.Fatalf("an unchanged build deployed %v and recorded %+v", f.deployed, f.rows)
	}
}

func TestAPinnedImageIsLeftAlone(t *testing.T) {
	f := newFake(map[string]Check{"a": {Floating: false, ContainerID: "c", Status: "running"}})
	if got := update(t, f, "a", Auto); got != "pinned" || len(f.deployed) != 0 {
		t.Fatalf("got %q, deployed %v", got, f.deployed)
	}
}

func TestAnAppThatCannotBeCheckedIsUnknown(t *testing.T) {
	f := newFake(map[string]Check{})
	if got := update(t, f, "native", Auto); got != "unknown" || len(f.deployed) != 0 {
		t.Fatalf("got %q, deployed %v", got, f.deployed)
	}
}

func TestNotifyRecordsANewBuildOnce(t *testing.T) {
	f := newFake(map[string]Check{"a": moved("a")})
	for range 2 {
		if got := update(t, f, "a", Notify); got != "available" {
			t.Fatalf("got %q", got)
		}
	}
	if len(f.deployed) != 0 {
		t.Fatalf("notify deployed %v", f.deployed)
	}
	if len(f.rows) != 1 || f.rows[0].Result != Available || f.rows[0].NewImage != "sha256:new-a" || f.rows[0].OldImage != "sha256:old-a" {
		t.Fatalf("rows = %+v; want one available row", f.rows)
	}
}

func TestAutoInstallsANewBuild(t *testing.T) {
	f := newFake(map[string]Check{"a": moved("a")})
	if got := update(t, f, "a", Auto); got != Updated {
		t.Fatalf("got %q", got)
	}
	if !slices.Equal(f.deployed, []string{"a@c-a"}) {
		t.Fatalf("deployed %v; want the container the check saw", f.deployed)
	}
	if !slices.Equal(f.installs, []string{"sha256:new-a"}) {
		t.Fatalf("installed %v; want only the build the check found", f.installs)
	}
	if len(f.rows) != 1 || f.rows[0].Result != Updated || f.rows[0].OldDigest != "sha256:dold" || f.rows[0].NewDigest != "sha256:dnew" {
		t.Fatalf("rows = %+v", f.rows)
	}
	// The next run finds the new build running and does nothing.
	if got := update(t, f, "a", Auto); got != "current" || len(f.deployed) != 1 {
		t.Fatalf("second run %q, deployed %v", got, f.deployed)
	}
}

func TestAFailingBuildIsUndoneOnceAndNeverRetried(t *testing.T) {
	f := newFake(map[string]Check{"a": moved("a")})
	f.undo["a"] = true
	if got := update(t, f, "a", Auto); got != RolledBack {
		t.Fatalf("got %q", got)
	}
	for range 5 {
		if got := update(t, f, "a", Auto); got != "held" {
			t.Fatalf("got %q, want held", got)
		}
	}
	if len(f.deployed) != 1 {
		t.Fatalf("a build that failed was deployed %d times: %v", len(f.deployed), f.deployed)
	}
	if len(f.rows) != 1 || f.rows[0].Result != RolledBack || f.rows[0].Detail == "" {
		t.Fatalf("rows = %+v", f.rows)
	}
	// A newer build is tried.
	c := f.checks["a"]
	c.LatestID = "sha256:newer"
	f.checks["a"], f.undo["a"] = c, false
	if got := update(t, f, "a", Auto); got != Updated || len(f.deployed) != 2 {
		t.Fatalf("a newer build: %q, deployed %v", got, f.deployed)
	}
}

func TestAFailedDeployIsRetriedNextRun(t *testing.T) {
	f := newFake(map[string]Check{"a": moved("a")})
	f.fail["a"] = true
	if got := update(t, f, "a", Auto); got != Failed {
		t.Fatalf("got %q", got)
	}
	f.fail["a"] = false
	if got := update(t, f, "a", Auto); got != Updated {
		t.Fatalf("got %q; a failed deploy (not undone) must be tried again", got)
	}
}

func TestABlockerSkipsOnceAndDeploysNothing(t *testing.T) {
	f := newFake(map[string]Check{"a": moved("a")})
	f.blocker["a"] = "it runs an image you chose"
	for range 3 {
		if got := update(t, f, "a", Auto); got != "skipped" {
			t.Fatalf("got %q", got)
		}
	}
	if len(f.deployed) != 0 || len(f.rows) != 1 || f.rows[0].Detail != "it runs an image you chose" {
		t.Fatalf("deployed %v, rows %+v", f.deployed, f.rows)
	}
}

func TestAStoppedAppIsNotStartedByAnUpdate(t *testing.T) {
	c := moved("a")
	c.Status = "exited"
	f := newFake(map[string]Check{"a": c})
	if got := update(t, f, "a", Auto); got != "skipped" || len(f.deployed) != 0 || f.rows[0].Detail == "" {
		t.Fatalf("got %q, deployed %v, rows %+v", got, f.deployed, f.rows)
	}
}

func TestRunGoesOnAfterAnErrorAndPausesAfterAnUpdate(t *testing.T) {
	f := newFake(map[string]Check{"a": moved("a"), "c": moved("c")})
	f.checkErr["b"] = errors.New("docker did not answer")
	s := config.ImageUpdates{Mode: Auto, Services: map[string]string{"c": Notify}}
	got := Run(context.Background(), f.hooks(), s, []string{"a", "b", "c"})
	want := map[string]string{"a": Updated, "b": "error", "c": "available"}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("outcomes %v; want %v", got, want)
		}
	}
	if len(f.slept) != 1 || f.slept[0] != Gap {
		t.Fatalf("slept %v; want one gap, after the one update", f.slept)
	}
}

// The pause follows each deploy, before the next one, even when the history
// write failed; the error keeps the deploy's reason as well as the write's.
func TestRunPausesAfterARecreateWhoseRowCannotBeWritten(t *testing.T) {
	f := newFake(map[string]Check{"a": moved("a"), "b": moved("b"), "c": moved("c")})
	f.undo["b"] = true
	var events []string
	h := f.hooks()
	deploy := h.Deploy
	h.Deploy = func(ctx context.Context, slug, containerID, newImage string, checkedAt time.Time) (DeployResult, bool, error) {
		events = append(events, "deploy "+slug)
		return deploy(ctx, slug, containerID, newImage, checkedAt)
	}
	h.Sleep = func(_ context.Context, d time.Duration) { events = append(events, "gap") }
	h.Record = func(Record) error { return errors.New("the disk is full") }
	got := Run(context.Background(), h, config.ImageUpdates{Mode: Auto}, []string{"a", "b", "c"})
	if got["a"] != "error" || got["b"] != "error" || got["c"] != "error" {
		t.Fatalf("outcomes %v; want the write errors reported", got)
	}
	want := []string{"deploy a", "gap", "deploy b", "gap", "deploy c", "gap"}
	if !slices.Equal(events, want) {
		t.Fatalf("events %v; want %v", events, want)
	}
	_, err := UpdateService(context.Background(), h, "b", Auto)
	if err == nil || !strings.Contains(err.Error(), "stopped within") || !strings.Contains(err.Error(), "disk is full") {
		t.Fatalf("the error of an undone update whose row failed: %v; want both reasons", err)
	}
}

// A deploy that failed may still have replaced the container before a later
// step failed, so the next app waits too.
func TestRunPausesAfterAFailedDeploy(t *testing.T) {
	f := newFake(map[string]Check{"a": moved("a"), "b": moved("b")})
	f.fail["a"] = true
	got := Run(context.Background(), f.hooks(), config.ImageUpdates{Mode: Auto}, []string{"a", "b"})
	if got["a"] != Failed || got["b"] != Updated || len(f.slept) != 2 {
		t.Fatalf("outcomes %v, slept %v; want a gap after each deploy", got, f.slept)
	}
}

func TestSettings(t *testing.T) {
	n := Normalize(&config.ImageUpdates{Mode: "sometimes", Hour: 30, Services: map[string]string{"a": Notify, "b": "bad", "": Auto}})
	if n.Mode != Off || n.Hour != config.ImageUpdatesDefaultHour || len(n.Services) != 1 || n.Services["a"] != Notify {
		t.Fatalf("Normalize = %+v", n)
	}
	if got := Normalize(nil); got.Mode != Off {
		t.Fatalf("Normalize(nil) = %+v", got)
	}
	s := config.ImageUpdates{Mode: Auto, Hour: 4, Services: map[string]string{"a": Off}}
	if EffectiveMode(s, "a") != Off || EffectiveMode(s, "b") != Auto {
		t.Fatal("an app's own choice must win over the one for every app")
	}
	if !AnythingOn(config.ImageUpdates{Mode: Off, Services: map[string]string{"a": Notify}}) || AnythingOn(config.ImageUpdates{Mode: Off}) {
		t.Fatal("AnythingOn")
	}
	at := func(h int) time.Time { return time.Date(2026, 11, 28, h, 30, 0, 0, time.Local) }
	if Due(s, at(3)) || !Due(s, at(4)) || !Due(s, at(23)) {
		t.Fatal("Due must start at the hour")
	}
	s.LastRun = "2026-11-28"
	if Due(s, at(5)) {
		t.Fatal("one run a day")
	}
	saved, err := Save(config.ImageUpdates{Mode: Off, Offer: true, LastRun: "2026-11-27"}, Notify, 6, map[string]string{"a": Auto, "b": ""})
	if err != nil || saved.Mode != Notify || saved.Hour != 6 || saved.Offer || saved.LastRun != "2026-11-27" || len(saved.Services) != 1 {
		t.Fatalf("Save = %+v, %v", saved, err)
	}
	if _, err := Save(config.ImageUpdates{}, "always", 4, nil); err == nil {
		t.Fatal("Save took an unknown mode")
	}
	if _, err := Save(config.ImageUpdates{}, Auto, 24, nil); err == nil {
		t.Fatal("Save took hour 24")
	}
	if Short("sha256:0123456789abcdef") != "0123456789ab" {
		t.Fatal("Short")
	}
}

// An undone build whose history row could not be written stays held while the
// app runs: the next check writes the row and redeploys nothing.
func TestAnUndoneBuildStaysHeldWhenItsRowCannotBeWritten(t *testing.T) {
	f := newFake(map[string]Check{"a": moved("a")})
	f.undo["a"] = true
	holds := &Holds{}
	h := f.hooks()
	h.Holds = holds
	broken := true
	record := h.Record
	h.Record = func(r Record) error {
		if broken {
			return errors.New("disk full")
		}
		return record(r)
	}
	if got, err := UpdateService(context.Background(), h, "a", Auto); got != RolledBack || err == nil {
		t.Fatalf("first run = %q, %v; want rolled_back with the write error", got, err)
	}
	broken = false
	for range 2 {
		if got, err := UpdateService(context.Background(), h, "a", Auto); got != "held" || err != nil {
			t.Fatalf("later run = %q, %v; want held", got, err)
		}
	}
	if len(f.deployed) != 1 {
		t.Fatalf("deploys = %v; want the first one only", f.deployed)
	}
	if len(f.rows) != 1 || f.rows[0].Result != RolledBack || f.rows[0].NewImage != "sha256:new-a" {
		t.Fatalf("rows = %+v; want the undo written once", f.rows)
	}
	// Control: a newer build is tried.
	c := f.checks["a"]
	c.LatestID = "sha256:newer-a"
	f.checks["a"] = c
	f.undo["a"] = false
	if got, err := UpdateService(context.Background(), h, "a", Auto); got != Updated || err != nil {
		t.Fatalf("newer build = %q, %v; want updated", got, err)
	}
}

// A deploy that put the new build in place and then failed a later step records
// the build as installed, so the row matches what runs.
func TestABuildThatRunsIsRecordedAsInstalledWhenALaterStepFails(t *testing.T) {
	f := newFake(map[string]Check{"a": moved("a")})
	h := f.hooks()
	h.Deploy = func(_ context.Context, slug, containerID, newImage string, _ time.Time) (DeployResult, bool, error) {
		f.deployed = append(f.deployed, slug+"@"+containerID)
		return DeployResult{NewImage: newImage}, false, errors.New("the deployment record could not be written")
	}
	got, err := UpdateService(context.Background(), h, "a", Auto)
	if got != Updated || err == nil {
		t.Fatalf("got %q, %v; want updated, with the later error", got, err)
	}
	if len(f.rows) != 1 || f.rows[0].Result != Updated || f.rows[0].NewImage != "sha256:new-a" || !strings.Contains(f.rows[0].Detail, "could not be written") {
		t.Fatalf("rows = %+v; want one updated row that names the later error", f.rows)
	}
	// Control: a deploy that installed nothing is still a failure.
	f = newFake(map[string]Check{"a": moved("a")})
	f.fail["a"] = true
	if got, _ := UpdateService(context.Background(), f.hooks(), "a", Auto); got != Failed {
		t.Fatalf("a deploy that installed nothing = %q; want failed", got)
	}
}
