package noderead

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const nodeID = "0x1111111111111111111111111111111111111111"

// tequilAPI serves a node's three answers. historyStatus 0 means 200.
func tequilAPI(t *testing.T, identitiesStatus, historyStatus int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/identities", func(w http.ResponseWriter, _ *http.Request) {
		if identitiesStatus != 0 {
			w.WriteHeader(identitiesStatus)
			return
		}
		w.Write([]byte(`{"identities":[{"id":"` + nodeID + `"}]}`))
	})
	mux.HandleFunc("/identities/"+nodeID, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"id":"` + nodeID + `","earnings_tokens":{"wei":"1250000000000000000","ether":"1.25"},` +
			`"earnings_total_tokens":{"wei":"9500000000000000000","ether":"9.5"}}`))
	})
	mux.HandleFunc("/transactor/settle/history", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page_size") != "1000" {
			t.Errorf("history asked with %q, want page_size=1000", r.URL.RawQuery)
		}
		if historyStatus != 0 {
			w.WriteHeader(historyStatus)
			return
		}
		w.Write([]byte(`{"items":[{"amount":4000,"fees":1000},{"amount":2000,"fees":500}]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func nativeRead(srv *httptest.Server) NativeOutput {
	return func(ctx context.Context) (string, error) { return TequilAPIOutput(ctx, srv.Client(), srv.URL) }
}

// The native read prints what the container command prints, so the catalog's own
// regexes read it: one parser for both paths.
func TestNativeReadGoesThroughTheCatalogsParser(t *testing.T) {
	got := ReadNative(context.Background(), nativeRead(tequilAPI(t, 0, 0)), mysteriumReader(t))
	if len(got) != 1 || !got[0].OK() {
		t.Fatalf("got %+v, want one reading", got)
	}
	r := got[0]
	if r.Instance != 1 || r.Node != nodeID || r.Balance != 1.25 || r.Lifetime != 9.5 {
		t.Errorf("got %+v, want node %s, balance 1.25, lifetime 9.5", r, nodeID)
	}
	if r.SettledNet == nil || *r.SettledNet != 6000 || *r.SettledFees != 1500 {
		t.Errorf("settled sums = %v/%v, want 6000/1500", r.SettledNet, r.SettledFees)
	}
}

func TestNativeReadWithoutHistoryKeepsTheFigures(t *testing.T) {
	got := ReadNative(context.Background(), nativeRead(tequilAPI(t, 0, http.StatusInternalServerError)), mysteriumReader(t))
	if !got[0].OK() || got[0].Balance != 1.25 || got[0].SettledNet != nil {
		t.Errorf("got %+v, want the figures without settled sums", got[0])
	}
}

func TestNativeReadThatFailsIsAnErrorReading(t *testing.T) {
	got := ReadNative(context.Background(), nativeRead(tequilAPI(t, http.StatusServiceUnavailable, 0)), mysteriumReader(t))
	if len(got) != 1 || got[0].OK() || got[0].Error == "" {
		t.Errorf("got %+v, want one reading with an error", got)
	}
	// Nothing listening at all: the node is not running.
	srv := tequilAPI(t, 0, 0)
	srv.Close()
	if got := ReadNative(context.Background(), nativeRead(srv), mysteriumReader(t)); got[0].OK() {
		t.Errorf("a node that does not answer gave a reading: %+v", got[0])
	}
}

func TestMysteriumHasANativeReader(t *testing.T) {
	if NativeReaders["mysterium"] == nil {
		t.Fatal("Mysterium ships native binaries, so it must have a native reader")
	}
}
