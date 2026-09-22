package e2e

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// dbFile is the database the app keeps its state in, inside the data directory.
const dbFile = "cashpilot-desktop.db"

// TestTheAppRefusesADatabaseItCannotUse covers the disk going wrong underneath
// the app: a half-written file after a power cut, a sync tool replacing the
// database with a placeholder, a backup restored on top of it.
//
// The requirement is narrow and important: fail with an error the app can show.
// Starting anyway on an empty database is the worst outcome, because the user
// then sees "no services" and the next write makes that permanent.
func TestTheAppRefusesADatabaseItCannotUse(t *testing.T) {
	t.Run("a corrupted database is reported, not ignored", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, dbFile), []byte("this is not a database, it is a note"), 0o600); err != nil {
			t.Fatalf("writing the corrupted file: %v", err)
		}
		st, err := store.Open(dir)
		if err == nil {
			_ = st.Close()
			t.Fatal("the app opened a corrupted database as if it were fine")
		}
	})

	t.Run("a database path that is not a file is reported", func(t *testing.T) {
		dir := t.TempDir()
		// A directory where the database belongs: what a botched restore or a
		// mount point left in the wrong place looks like.
		if err := os.MkdirAll(filepath.Join(dir, dbFile), 0o700); err != nil {
			t.Fatalf("creating the blocking directory: %v", err)
		}
		st, err := store.Open(dir)
		if err == nil {
			_ = st.Close()
			t.Fatal("the app reported success with a directory where its database should be")
		}
	})

	// Positive control: the same code opens a healthy directory, so the two
	// failures above are the corruption being caught and not store.Open being
	// broken for everyone.
	t.Run("a healthy database opens", func(t *testing.T) {
		st, err := store.Open(t.TempDir())
		if err != nil {
			t.Fatalf("store.Open on a fresh directory: %v", err)
		}
		_ = st.Close()
	})
}

// TestStateSurvivesARestart is the promise behind a local-first app: close it,
// open it again, and everything is where it was. It also checks the one thing
// that must NOT survive in readable form — the password.
func TestStateSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	const password = "correct-horse-battery-staple"

	first, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if err := first.SaveCredentials("honeygain", map[string]string{
		"HONEYGAIN_EMAIL":    "someone@example.test",
		"HONEYGAIN_PASSWORD": password,
	}); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	if err := first.UpsertDeployment(store.Deployment{
		Slug: "honeygain", ContainerID: "ctr1", Name: "cashpilot-honeygain", Status: "running",
	}); err != nil {
		t.Fatalf("UpsertDeployment: %v", err)
	}
	if _, err := first.SaveEarnings(store.EarningsRecord{Platform: "honeygain", Balance: 3.25, Currency: "USD"}); err != nil {
		t.Fatalf("SaveEarnings: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The password is encrypted at rest: reading the database file must not turn
	// it up. The master key lives in the OS keychain, not in this file.
	raw, err := os.ReadFile(filepath.Join(dir, dbFile))
	if err != nil {
		t.Fatalf("reading the database file: %v", err)
	}
	if bytes.Contains(raw, []byte(password)) {
		t.Error("the password is sitting in the database file in the clear")
	}

	second, err := store.Open(dir)
	if err != nil {
		t.Fatalf("reopening the store: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	creds, err := second.GetCredentials("honeygain")
	if err != nil {
		t.Fatalf("GetCredentials after restart: %v", err)
	}
	if creds["HONEYGAIN_PASSWORD"] != password {
		t.Errorf("the password did not survive the restart: %q", creds["HONEYGAIN_PASSWORD"])
	}
	dep, ok, err := second.GetDeployment("honeygain")
	if err != nil || !ok || dep.ContainerID != "ctr1" {
		t.Errorf("the deployment did not survive the restart: %+v (ok=%v err=%v)", dep, ok, err)
	}
	earnings := second.ListLatestEarnings()
	if len(earnings) != 1 || !approx(earnings[0].Balance, 3.25) {
		t.Errorf("the earnings did not survive the restart: %+v", earnings)
	}
}

// TestTwoWritersDoNotLoseRows covers the shape a user creates without meaning to:
// the app already running and a second copy started (the background daemon, a
// second launch from the installer, a synced folder opened twice). They share one
// SQLite file, and SQLite lets only one of them write at a time.
//
// The two writers run at the same time on purpose. Taking turns proves nothing:
// the lock is only contended when both are inside it, and that is exactly when
// the app used to throw rows away — the loser got "database is locked" back and
// the collected balance was gone, with nothing on screen to say so. So the bar
// here is every write landing, not merely the file surviving.
func TestTwoWritersDoNotLoseRows(t *testing.T) {
	dir := t.TempDir()

	one, err := store.Open(dir)
	if err != nil {
		t.Fatalf("first store.Open: %v", err)
	}
	t.Cleanup(func() { _ = one.Close() })

	two, err := store.Open(dir)
	if err != nil {
		t.Fatalf("second store.Open on the same directory: %v", err)
	}
	t.Cleanup(func() { _ = two.Close() })

	const rowsEach = 200

	var mu sync.Mutex
	var failures []error
	// start releases both writers together, so they overlap instead of one
	// finishing while the other is still warming up.
	start := make(chan struct{})
	var wg sync.WaitGroup
	write := func(s *store.Store, platform string) {
		defer wg.Done()
		<-start
		for i := 0; i < rowsEach; i++ {
			if _, err := s.SaveEarnings(store.EarningsRecord{Platform: platform, Balance: float64(i), Currency: "USD"}); err != nil {
				mu.Lock()
				failures = append(failures, err)
				mu.Unlock()
			}
		}
	}
	wg.Add(2)
	go write(one, "honeygain")
	go write(two, "iproyal")
	close(start)
	wg.Wait()

	if len(failures) > 0 {
		t.Errorf("%d of %d writes were lost to the other writer; first: %v", len(failures), 2*rowsEach, failures[0])
	}

	// And the last thing each writer stored is what a fresh reader sees, so a
	// write that reported success cannot have been rolled back underneath.
	third, err := store.Open(dir)
	if err != nil {
		t.Fatalf("reopening the store: %v", err)
	}
	t.Cleanup(func() { _ = third.Close() })
	latest := map[string]float64{}
	for _, row := range third.ListLatestEarnings() {
		latest[row.Platform] = row.Balance
	}
	for _, platform := range []string{"honeygain", "iproyal"} {
		got, ok := latest[platform]
		if !ok {
			t.Errorf("%s wrote %d rows and none of them are in the database", platform, rowsEach)
			continue
		}
		if !approx(got, float64(rowsEach-1)) {
			t.Errorf("%s shows %v, want its last written balance %v", platform, got, float64(rowsEach-1))
		}
	}
}

// TestAReadOnlyDataDirectoryIsReported checks the case a locked-down machine
// creates: the data directory exists but the app may not write to it. Whether the
// operating system enforces that for this user is probed first, and both outcomes
// are asserted — a refusal must come with a message, and a platform that allows
// the write must give a working store rather than a half-open one.
func TestAReadOnlyDataDirectoryIsReported(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "locked")
	if err := os.MkdirAll(dir, 0o500); err != nil {
		t.Fatalf("creating the read-only directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	probe := filepath.Join(dir, "probe")
	enforced := os.WriteFile(probe, []byte("x"), 0o600) != nil
	if !enforced {
		_ = os.Remove(probe)
	}

	st, err := store.Open(dir)
	if enforced {
		if err == nil {
			_ = st.Close()
			t.Fatal("the app opened a database in a directory it cannot write to")
		}
		if strings.TrimSpace(err.Error()) == "" {
			t.Error("the failure carries no message for the user")
		}
		return
	}
	// This platform lets the user write anyway (Windows does not enforce the
	// read-only bit on a directory), so the app must simply work.
	if err != nil {
		t.Fatalf("store.Open failed although this platform allows the write: %v", err)
	}
	if _, err := st.SaveEarnings(store.EarningsRecord{Platform: "honeygain", Balance: 1, Currency: "USD"}); err != nil {
		t.Errorf("the store opened but cannot write: %v", err)
	}
	_ = st.Close()
}
