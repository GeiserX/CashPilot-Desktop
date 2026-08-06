package upstream

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/fleetnet"
)

// testPolicy allows the loopback stub servers these tests dial. The SSRF policy
// exists to stop a user-typed URL reaching somewhere it should not; it is not
// what is under test here.
func testPolicy() fleetnet.Policy {
	return fleetnet.Policy{Mode: "private", AllowedHosts: []string{"127.0.0.1"}}
}

func TestConfirmed(t *testing.T) {
	// The whole point: an import is refused by the server unless this worker is
	// past enrolment, and "past enrolment" is a pair of keys, not a flag.
	cases := []struct {
		name           string
		sent, received string
		want           bool
	}{
		{"own key sent, nothing returned -- confirmed", "own-key", "", true},
		{"own key sent but the server re-delivered one -- still enrolling", "own-key", "own-key", false},
		{"the server issued a DIFFERENT key -- re-issue in flight", "old-key", "new-key", false},
		{"nothing sent: we authenticated with the shared key", "", "", false},
		{"shared key sent, key issued back: first contact", "", "issued", false},
		{"whitespace is not a key", "   ", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Confirmed(tc.sent, tc.received); got != tc.want {
				t.Fatalf("Confirmed(%q, %q) = %v, want %v", tc.sent, tc.received, got, tc.want)
			}
		})
	}
}

func TestChunkReadings(t *testing.T) {
	mk := func(n int) []ImportReading {
		out := make([]ImportReading, n)
		for i := range out {
			out[i] = ImportReading{Slug: "grass", Date: "2026-01-01"}
		}
		return out
	}

	t.Run("an empty history produces no requests at all", func(t *testing.T) {
		// Not one empty request: a Desktop that has never collected anything
		// must not POST a body just to say so.
		if got := ChunkReadings(nil, 10); len(got) != 0 {
			t.Fatalf("got %d batches, want 0", len(got))
		}
	})

	t.Run("an exact multiple has no trailing empty batch", func(t *testing.T) {
		got := ChunkReadings(mk(20), 10)
		if len(got) != 2 {
			t.Fatalf("got %d batches, want 2", len(got))
		}
		for i, b := range got {
			if len(b) != 10 {
				t.Fatalf("batch %d has %d readings, want 10", i, len(b))
			}
		}
	})

	t.Run("a remainder becomes a short final batch", func(t *testing.T) {
		got := ChunkReadings(mk(25), 10)
		if len(got) != 3 || len(got[2]) != 5 {
			t.Fatalf("got %d batches, last of %d", len(got), len(got[len(got)-1]))
		}
	})

	t.Run("every reading survives the split", func(t *testing.T) {
		total := 0
		for _, b := range ChunkReadings(mk(2501), 1000) {
			total += len(b)
		}
		if total != 2501 {
			t.Fatalf("chunking lost readings: %d of 2501", total)
		}
	})

	t.Run("a nonsensical size falls back rather than looping forever", func(t *testing.T) {
		if got := ChunkReadings(mk(3), 0); len(got) != 1 {
			t.Fatalf("got %d batches, want 1", len(got))
		}
	})

	t.Run("the chunk stays under the server's cap", func(t *testing.T) {
		// The server refuses a body over 2000 readings. Sitting exactly on a
		// limit breaks the moment either side's idea of it moves by one.
		if ImportChunk >= 2000 {
			t.Fatalf("ImportChunk = %d leaves no headroom under the server's 2000", ImportChunk)
		}
	})
}

// stubServer records the last import it received.
type stubServer struct {
	*httptest.Server
	path  string
	auth  string
	body  ImportPayload
	calls int
}

func newStub(t *testing.T, status int, response any) *stubServer {
	t.Helper()
	s := &stubServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.calls++
		s.path = r.URL.Path
		s.auth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &s.body)
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(response)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestImportSendsTheServersShape(t *testing.T) {
	stub := newStub(t, http.StatusOK, ImportResponse{Status: "ok", Imported: 2, Source: "mac"})
	client := &Client{HTTP: stub.Client(), Policy: testPolicy()}

	rate := 0.4
	resp, err := client.Import(context.Background(), stub.URL, "own-key", ImportPayload{
		ClientID: "mac",
		Readings: []ImportReading{
			{Slug: "grass", Balance: 1.5, Date: "2026-01-01", Currency: "GRASS"},
			{Slug: "mysterium", Balance: 9, Date: "2026-01-02", Currency: "MYST", FXRateUSD: &rate},
		},
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if resp.Imported != 2 {
		t.Fatalf("Imported = %d, want 2", resp.Imported)
	}
	if stub.path != "/api/workers/earnings-import" {
		t.Fatalf("posted to %q", stub.path)
	}
	if stub.auth != "Bearer own-key" {
		t.Fatalf("Authorization = %q", stub.auth)
	}
	if stub.body.ClientID != "mac" || len(stub.body.Readings) != 2 {
		t.Fatalf("server saw %+v", stub.body)
	}
	if stub.body.Readings[1].FXRateUSD == nil || *stub.body.Readings[1].FXRateUSD != 0.4 {
		t.Fatalf("the FX rate did not survive: %+v", stub.body.Readings[1])
	}
}

func TestImportOmitsAnUnknownFXRateRatherThanSendingZero(t *testing.T) {
	// A 0.0 rate would price the whole reading at nothing, and it would do it
	// confidently. Absent means unknown, which is the truth for a past day.
	var seen struct {
		Readings []map[string]any `json:"readings"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &seen)
		_ = json.NewEncoder(w).Encode(ImportResponse{Status: "ok"})
	}))
	defer srv.Close()

	client := &Client{HTTP: srv.Client(), Policy: testPolicy()}
	if _, err := client.Import(context.Background(), srv.URL, "k", ImportPayload{
		ClientID: "mac",
		Readings: []ImportReading{{Slug: "grass", Balance: 1, Date: "2026-01-01"}},
	}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if _, present := seen.Readings[0]["fx_rate_usd"]; present {
		t.Fatalf("an unknown rate was sent on the wire: %v", seen.Readings[0])
	}
}

func TestImportCarriesNoSourceField(t *testing.T) {
	// The server takes the source from the AUTHENTICATED worker so no client can
	// write into another's history. Sending one would not change that, but a
	// field appearing here is the first sign someone tried to make it.
	raw, err := json.Marshal(ImportPayload{ClientID: "mac", Readings: []ImportReading{{Slug: "grass"}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"source"`) {
		t.Fatalf("the payload declares a source: %s", raw)
	}
}

func TestImportRefusesWithoutATarget(t *testing.T) {
	client := &Client{Policy: testPolicy()}
	if _, err := client.Import(context.Background(), "  ", "k", ImportPayload{}); err != ErrNotPaired {
		t.Fatalf("err = %v, want ErrNotPaired", err)
	}
}

func TestImportRefusesWithoutACredential(t *testing.T) {
	// Sending an unauthenticated import would be rejected anyway; refusing here
	// surfaces a lost credential as an error the UI can show.
	client := &Client{Policy: testPolicy()}
	_, err := client.Import(context.Background(), "http://127.0.0.1:1", "  ", ImportPayload{})
	if err == nil || !strings.Contains(err.Error(), "no credential") {
		t.Fatalf("err = %v", err)
	}
}

func TestImportAppliesTheSSRFPolicy(t *testing.T) {
	// The URL is user-supplied and the request carries a bearer token. Sending
	// earnings to it is not a reason to validate it less than a heartbeat.
	client := &Client{Policy: fleetnet.Policy{Mode: "allowlist", AllowedHosts: []string{"cashpilot.example"}}}
	_, err := client.Import(context.Background(), "http://169.254.169.254", "k", ImportPayload{})
	if err == nil || !strings.Contains(err.Error(), "refusing to contact") {
		t.Fatalf("the policy was not applied: %v", err)
	}
}

func TestImportSurfacesTheServersRefusal(t *testing.T) {
	// 403 is its own diagnosis: the server still considers this worker
	// unconfirmed, so the fix is another heartbeat, not a new key.
	stub := newStub(t, http.StatusForbidden, map[string]string{"detail": "requires this worker's own key"})
	client := &Client{HTTP: stub.Client(), Policy: testPolicy()}
	_, err := client.Import(context.Background(), stub.URL, "k", ImportPayload{ClientID: "mac"})
	if err == nil {
		t.Fatal("a 403 was reported as success")
	}
	if !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "own key") {
		t.Fatalf("the refusal was not legible: %v", err)
	}
}

func TestImportReportsSkippedSlugs(t *testing.T) {
	// A silent drop looks identical to a complete import.
	stub := newStub(t, http.StatusOK, ImportResponse{Status: "ok", Imported: 1, Skipped: []string{"gone-service"}})
	client := &Client{HTTP: stub.Client(), Policy: testPolicy()}
	resp, err := client.Import(context.Background(), stub.URL, "k", ImportPayload{ClientID: "mac"})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(resp.Skipped) != 1 || resp.Skipped[0] != "gone-service" {
		t.Fatalf("Skipped = %v", resp.Skipped)
	}
}

func TestImportTrimsATrailingSlash(t *testing.T) {
	// Otherwise the path becomes //api/workers/earnings-import, which some
	// proxies route differently and some reject outright.
	stub := newStub(t, http.StatusOK, ImportResponse{Status: "ok"})
	client := &Client{HTTP: stub.Client(), Policy: testPolicy()}
	if _, err := client.Import(context.Background(), stub.URL+"/", "k", ImportPayload{ClientID: "mac"}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if stub.path != "/api/workers/earnings-import" {
		t.Fatalf("posted to %q", stub.path)
	}
}
