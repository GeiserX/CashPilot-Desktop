package main

// CashPilot-Desktop-xjr: handing the server the history collected before pairing.
//
// The behaviour under test is a copy that must not become a migration. Nothing
// here may delete or move a local row, because an unlinked Desktop still has to
// show exactly what it earned on its own.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/fleetnet"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
	"github.com/GeiserX/CashPilot-Desktop/internal/upstream"
)

// importStub is a CashPilot server that records every import it is handed.
type importStub struct {
	*httptest.Server
	mu      sync.Mutex
	bodies  []upstream.ImportPayload
	status  int
	skipped []string
}

func newImportStub(t *testing.T) *importStub {
	t.Helper()
	s := &importStub{status: http.StatusOK}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body upstream.ImportPayload
		_ = json.Unmarshal(raw, &body)
		s.mu.Lock()
		s.bodies = append(s.bodies, body)
		status, skipped := s.status, s.skipped
		s.mu.Unlock()
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(upstream.ImportResponse{
			Status: "ok", Imported: len(body.Readings), Skipped: skipped, Source: body.ClientID,
		})
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *importStub) received() []upstream.ImportPayload {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]upstream.ImportPayload(nil), s.bodies...)
}

func (s *importStub) readings() []upstream.ImportReading {
	var out []upstream.ImportReading
	for _, b := range s.received() {
		out = append(out, b.Readings...)
	}
	return out
}

func testImportClient(stub *importStub) *upstream.Client {
	return &upstream.Client{
		HTTP:   stub.Client(),
		Policy: fleetnet.Policy{Mode: "private", AllowedHosts: []string{"127.0.0.1"}},
	}
}

func TestHistoryIsHandedOverOnceAndOnlyOnce(t *testing.T) {
	app := newPayloadTestApp(t, nil)
	seedEarnings(t, app.store,
		store.EarningsRecord{Platform: "grass", Balance: 12.5, Currency: "GRASS"},
		store.EarningsRecord{Platform: "honeygain", Balance: 3, Currency: "USD"},
	)
	stub := newImportStub(t)

	app.pushHistoryOnce(context.Background(), testImportClient(stub), stub.URL, "own-key")
	first := len(stub.readings())
	if first != 2 {
		t.Fatalf("the server was handed %d reading(s), want 2", first)
	}

	// Every heartbeat calls this. Re-sending up to 400 days of readings once a
	// minute forever is the failure being prevented -- the import is idempotent,
	// so it would be correct and still wrong.
	app.pushHistoryOnce(context.Background(), testImportClient(stub), stub.URL, "own-key")
	if got := len(stub.readings()); got != first {
		t.Fatalf("history was re-sent: %d readings after the second call, want %d", got, first)
	}
}

func TestTheLocalRowsSurviveTheHandOver(t *testing.T) {
	// The point of the whole design. If pairing moved the rows, unlinking would
	// leave the machine with nothing to show for the months it ran alone.
	app := newPayloadTestApp(t, nil)
	seedEarnings(t, app.store, store.EarningsRecord{Platform: "grass", Balance: 12.5, Currency: "GRASS"})
	stub := newImportStub(t)

	app.pushHistoryOnce(context.Background(), testImportClient(stub), stub.URL, "own-key")

	local := app.store.ListDailyBalances(400)
	if len(local) != 1 || local[0].Platform != "grass" || local[0].Balance != 12.5 {
		t.Fatalf("the local history did not survive the push: %+v", local)
	}
}

func TestAFailedHandOverIsRetriedRatherThanRecorded(t *testing.T) {
	app := newPayloadTestApp(t, nil)
	seedEarnings(t, app.store, store.EarningsRecord{Platform: "grass", Balance: 1, Currency: "GRASS"})
	stub := newImportStub(t)
	stub.mu.Lock()
	stub.status = http.StatusForbidden // still enrolling, as far as the server is concerned
	stub.mu.Unlock()

	app.pushHistoryOnce(context.Background(), testImportClient(stub), stub.URL, "own-key")
	if got := app.cfg.Config().UpstreamHistoryPushedTo; got != "" {
		t.Fatalf("a refused push was recorded as done: %q", got)
	}

	stub.mu.Lock()
	stub.status = http.StatusOK
	stub.mu.Unlock()
	app.pushHistoryOnce(context.Background(), testImportClient(stub), stub.URL, "own-key")
	if len(stub.readings()) == 0 {
		t.Fatal("the retry sent nothing")
	}
	if app.cfg.Config().UpstreamHistoryPushedTo == "" {
		t.Fatal("the successful retry was not recorded")
	}
}

func TestPairingWithADifferentServerHandsItTheHistoryToo(t *testing.T) {
	// Storing the URL rather than a bare boolean is what makes this work: a
	// second server has never seen these readings, and a flag would tell it so.
	app := newPayloadTestApp(t, nil)
	seedEarnings(t, app.store, store.EarningsRecord{Platform: "grass", Balance: 1, Currency: "GRASS"})

	first, second := newImportStub(t), newImportStub(t)
	app.pushHistoryOnce(context.Background(), testImportClient(first), first.URL, "own-key")
	app.pushHistoryOnce(context.Background(), testImportClient(second), second.URL, "own-key")

	if len(second.readings()) != 1 {
		t.Fatalf("the second server was handed %d reading(s), want 1", len(second.readings()))
	}
}

func TestATrailingSlashIsNotADifferentServer(t *testing.T) {
	// Otherwise the marker never matches what the user typed and the history is
	// re-sent on every heartbeat, which is the exact failure the marker exists
	// to prevent.
	app := newPayloadTestApp(t, nil)
	seedEarnings(t, app.store, store.EarningsRecord{Platform: "grass", Balance: 1, Currency: "GRASS"})
	stub := newImportStub(t)

	app.pushHistoryOnce(context.Background(), testImportClient(stub), stub.URL, "own-key")
	sent := len(stub.readings())
	app.pushHistoryOnce(context.Background(), testImportClient(stub), stub.URL+"/", "own-key")
	if got := len(stub.readings()); got != sent {
		t.Fatalf("a trailing slash re-sent the history: %d readings, want %d", got, sent)
	}
}

func TestAnEmptyHistoryIsRecordedWithoutPostingAnything(t *testing.T) {
	// A Desktop paired before it ever collected anything has nothing to hand
	// over. It must not POST an empty body, and it must not re-ask every minute
	// forever.
	app := newPayloadTestApp(t, nil)
	stub := newImportStub(t)

	app.pushHistoryOnce(context.Background(), testImportClient(stub), stub.URL, "own-key")
	if got := len(stub.received()); got != 0 {
		t.Fatalf("posted %d request(s) with no history to send", got)
	}
	if app.cfg.Config().UpstreamHistoryPushedTo == "" {
		t.Fatal("an empty history was not recorded, so it will be re-checked forever")
	}
}

func TestTheHandOverUsesTheStableClientID(t *testing.T) {
	// The server keys a worker's series on client_id. If this disagreed with the
	// heartbeat's, the imported history would land under a second identity and
	// the machine's readings would be split in two.
	app := newPayloadTestApp(t, nil)
	seedEarnings(t, app.store, store.EarningsRecord{Platform: "grass", Balance: 1, Currency: "GRASS"})
	stub := newImportStub(t)

	app.pushHistoryOnce(context.Background(), testImportClient(stub), stub.URL, "own-key")

	got := stub.received()
	if len(got) != 1 {
		t.Fatalf("got %d request(s)", len(got))
	}
	if want := app.upstreamPayload().ClientID; got[0].ClientID != want {
		t.Fatalf("imported as %q but heartbeats as %q", got[0].ClientID, want)
	}
}

func TestTheHandOverIsAuthenticatedWithThisMachinesOwnKey(t *testing.T) {
	// The shared enrolment key is refused for this by design: every worker holds
	// it, so it cannot prove who is writing.
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(upstream.ImportResponse{Status: "ok"})
	}))
	defer srv.Close()

	app := newPayloadTestApp(t, nil)
	seedEarnings(t, app.store, store.EarningsRecord{Platform: "grass", Balance: 1, Currency: "GRASS"})
	client := &upstream.Client{HTTP: srv.Client(), Policy: fleetnet.Policy{Mode: "private", AllowedHosts: []string{"127.0.0.1"}}}

	app.pushHistoryOnce(context.Background(), client, srv.URL, "own-key")
	if auth != "Bearer own-key" {
		t.Fatalf("Authorization = %q", auth)
	}
}

// pairingStub answers both a heartbeat and an import, so the WIRING between
// them is exercised rather than each half in isolation. workerKey is what the
// heartbeat re-delivers: a non-empty value means the server has not yet seen
// this worker authenticate with its own key.
type pairingStub struct {
	*httptest.Server
	mu        sync.Mutex
	workerKey string
	imports   int
}

func newPairingStub(t *testing.T, reDeliveredKey string) *pairingStub {
	t.Helper()
	s := &pairingStub{workerKey: reDeliveredKey}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch r.URL.Path {
		case "/api/workers/heartbeat":
			_ = json.NewEncoder(w).Encode(upstream.Response{Status: "ok", WorkerID: 1, WorkerKey: s.workerKey})
		case "/api/workers/earnings-import":
			s.imports++
			_ = json.NewEncoder(w).Encode(upstream.ImportResponse{Status: "ok"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *pairingStub) importCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.imports
}

func TestAnUnconfirmedWorkerDoesNotTryToImport(t *testing.T) {
	// The server refuses an import from a worker still presenting the shared
	// enrolment key -- every worker on the fleet holds it, so it cannot prove
	// who is writing. Attempting it anyway would log a 403 every minute and
	// teach the user to ignore the log.
	//
	// Both unconfirmed shapes are covered, and the SECOND is the one that
	// matters. The first (no key held yet) also fails to import when the gate is
	// removed entirely, because there is no credential to import WITH -- so on
	// its own it would pass against a build with no gate at all, which is
	// exactly the failure a negative control exists to expose. The second holds
	// a real key, so only the gate itself stops it.
	cases := []struct {
		name           string
		heldKey        string
		reDelivered    string
		enrolmentToken string
	}{
		{
			name:           "first contact: we hold nothing and the server issues a key",
			heldKey:        "",
			reDelivered:    "issued-key",
			enrolmentToken: "shared-enrolment-key",
		},
		{
			name:           "we hold a key but the server is still re-delivering it",
			heldKey:        "own-key",
			reDelivered:    "own-key", // receipt not yet proven
			enrolmentToken: "shared-enrolment-key",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := newPayloadTestApp(t, nil)
			seedEarnings(t, app.store, store.EarningsRecord{Platform: "grass", Balance: 1, Currency: "GRASS"})
			app.upstream.mu.Lock()
			app.upstream.workerKey = tc.heldKey
			app.upstream.mu.Unlock()

			stub := newPairingStub(t, tc.reDelivered)
			client := &upstream.Client{HTTP: stub.Client(), Policy: fleetnet.Policy{Mode: "private", AllowedHosts: []string{"127.0.0.1"}}}

			app.sendUpstream(context.Background(), client, stub.URL, tc.enrolmentToken)
			if got := stub.importCount(); got != 0 {
				t.Fatalf("an unconfirmed worker sent %d import(s)", got)
			}
			if got := app.cfg.Config().UpstreamHistoryPushedTo; got != "" {
				t.Fatalf("an unconfirmed worker recorded the history as handed over: %q", got)
			}
		})
	}
}

func TestAConfirmedWorkerImportsOnItsNextHeartbeat(t *testing.T) {
	// The other half of the same rule -- without this, the test above passes for
	// a build that never imports at all.
	app := newPayloadTestApp(t, nil)
	seedEarnings(t, app.store, store.EarningsRecord{Platform: "grass", Balance: 1, Currency: "GRASS"})

	stub := newPairingStub(t, "") // no key returned == this worker is confirmed
	client := &upstream.Client{HTTP: stub.Client(), Policy: fleetnet.Policy{Mode: "private", AllowedHosts: []string{"127.0.0.1"}}}

	// sendUpstream reads the cached worker key, so seed it as a confirmed worker
	// would already hold it.
	app.upstream.mu.Lock()
	app.upstream.workerKey = "own-key"
	app.upstream.mu.Unlock()

	app.sendUpstream(context.Background(), client, stub.URL, "shared-enrolment-key")
	if got := stub.importCount(); got != 1 {
		t.Fatalf("a confirmed worker sent %d import(s), want 1", got)
	}
}

func TestASettingsChangeDuringTheUploadIsNotClobbered(t *testing.T) {
	// pushHistoryOnce reads the config, then uploads over the network, then
	// records where it sent the history. Saving the WHOLE config it read at the
	// start would silently discard anything the user changed on the settings
	// screen while the upload was in flight -- and the upload is the slowest
	// thing this app does unprompted. (CodeRabbit, PR #115.)
	app := newPayloadTestApp(t, nil)
	seedEarnings(t, app.store, store.EarningsRecord{Platform: "grass", Balance: 1, Currency: "GRASS"})

	// The change lands mid-upload: the stub server makes it from inside the
	// request handler, which is exactly the window that was open.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next := app.cfg.Config()
		next.DisplayCurrency = "EUR"
		if err := app.cfg.Save(next); err != nil {
			t.Errorf("the concurrent settings save failed: %v", err)
		}
		_ = json.NewEncoder(w).Encode(upstream.ImportResponse{Status: "ok", Imported: 1})
	}))
	defer srv.Close()

	client := &upstream.Client{HTTP: srv.Client(), Policy: fleetnet.Policy{Mode: "private", AllowedHosts: []string{"127.0.0.1"}}}
	app.pushHistoryOnce(context.Background(), client, srv.URL, "own-key")

	got := app.cfg.Config()
	if got.DisplayCurrency != "EUR" {
		t.Fatalf("the user's settings change was discarded: DisplayCurrency = %q", got.DisplayCurrency)
	}
	if got.UpstreamHistoryPushedTo == "" {
		t.Fatal("the hand-over was not recorded")
	}
}

func TestAServerTooOldToImportIsAskedOnce(t *testing.T) {
	// A CashPilot older than v1.16.0 has no such endpoint and answers 404 for as
	// long as it runs. Retrying once a minute would fill the log with an error
	// the user cannot act on except by upgrading -- which they will not do
	// because of a log line they have learned to scroll past.
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/workers/earnings-import" {
			calls++
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(upstream.Response{Status: "ok", WorkerID: 1})
	}))
	defer srv.Close()

	app := newPayloadTestApp(t, nil)
	seedEarnings(t, app.store, store.EarningsRecord{Platform: "grass", Balance: 1, Currency: "GRASS"})
	client := &upstream.Client{HTTP: srv.Client(), Policy: fleetnet.Policy{Mode: "private", AllowedHosts: []string{"127.0.0.1"}}}

	for range 3 {
		app.pushHistoryOnce(context.Background(), client, srv.URL, "own-key")
	}
	if calls != 1 {
		t.Fatalf("asked an old server %d times, want 1", calls)
	}

	// And it must NOT be recorded as handed over: the history has not been
	// delivered, so an upgraded server still has to receive it.
	if got := app.cfg.Config().UpstreamHistoryPushedTo; got != "" {
		t.Fatalf("a 404 was recorded as a successful hand-over: %q", got)
	}
}

func TestRestartingTheLoopRetriesAnUpgradedServer(t *testing.T) {
	// The flag lives in memory precisely so this works. Persisting it would make
	// one old server a permanent verdict on that URL.
	app := newPayloadTestApp(t, nil)
	app.upstream.mu.Lock()
	app.upstream.historyUnsupported = true
	app.upstream.mu.Unlock()

	// No UpstreamURL configured, so startUpstream returns before heartbeating --
	// but it must still have cleared the flag on the way in.
	app.startUpstream(context.Background())

	app.upstream.mu.Lock()
	still := app.upstream.historyUnsupported
	app.upstream.mu.Unlock()
	if still {
		t.Fatal("restarting the pairing loop did not re-arm the import")
	}
}

func TestHistoryReadingsConversion(t *testing.T) {
	t.Run("the date is the server's YYYY-MM-DD", func(t *testing.T) {
		// The server refuses anything else, and both delta readers ORDER BY it.
		got := historyReadings([]store.DailyBalance{{Platform: "grass", Day: "2026-01-02", Balance: 1, Currency: "GRASS"}})
		if len(got) != 1 || got[0].Date != "2026-01-02" {
			t.Fatalf("got %+v", got)
		}
		if _, err := time.Parse("2006-01-02", got[0].Date); err != nil {
			t.Fatalf("the date the server will receive is not a calendar day: %v", err)
		}
	})

	t.Run("the FX rate is left unknown rather than filled in", func(t *testing.T) {
		// Desktop does not record what a currency was worth on a past day.
		// Stamping today's rate onto a year-old reading would misprice it
		// confidently; a zero would price it at nothing.
		got := historyReadings([]store.DailyBalance{{Platform: "mysterium", Day: "2026-01-02", Balance: 9, Currency: "MYST"}})
		if got[0].FXRateUSD != nil {
			t.Fatalf("an FX rate was invented: %v", *got[0].FXRateUSD)
		}
	})

	t.Run("the currency survives", func(t *testing.T) {
		// Without it the server cannot difference the series: a delta is only
		// taken between readings in the SAME currency.
		got := historyReadings([]store.DailyBalance{{Platform: "mysterium", Day: "2026-01-02", Balance: 9, Currency: "MYST"}})
		if got[0].Currency != "MYST" {
			t.Fatalf("Currency = %q", got[0].Currency)
		}
	})

	t.Run("a row with no platform or no day is dropped", func(t *testing.T) {
		// Both would be refused by the server, and one bad row must not fail the
		// whole batch it happens to share.
		got := historyReadings([]store.DailyBalance{
			{Platform: "  ", Day: "2026-01-02"},
			{Platform: "grass", Day: "  "},
			{Platform: "grass", Day: "2026-01-02", Balance: 1},
		})
		if len(got) != 1 || got[0].Slug != "grass" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("an empty history converts to no readings", func(t *testing.T) {
		if got := historyReadings(nil); len(got) != 0 {
			t.Fatalf("got %d readings from nothing", len(got))
		}
	})
}

func TestSkippedSlugsAreNotTreatedAsAFailure(t *testing.T) {
	// A slug the server's catalog does not know is reported and the rest still
	// lands. Treating it as an error would re-send the whole history forever.
	app := newPayloadTestApp(t, nil)
	seedEarnings(t, app.store, store.EarningsRecord{Platform: "grass", Balance: 1, Currency: "GRASS"})
	stub := newImportStub(t)
	stub.mu.Lock()
	stub.skipped = []string{"a-service-this-server-does-not-have"}
	stub.mu.Unlock()

	app.pushHistoryOnce(context.Background(), testImportClient(stub), stub.URL, "own-key")
	if got := app.cfg.Config().UpstreamHistoryPushedTo; !strings.HasPrefix(got, "http://127.0.0.1") {
		t.Fatalf("skips were treated as a failure: UpstreamHistoryPushedTo = %q", got)
	}
}
