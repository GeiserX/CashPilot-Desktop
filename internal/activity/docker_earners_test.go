package activity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/moby/moby/client"
)

// Earners asks the daemon for Desktop's own running containers only: a CashPilot
// worker or a compose file on the same Docker also labels its containers
// cashpilot.managed=true, and the activity rounds must not sample those.
func TestEarnersAsksForDesktopsContainersOnly(t *testing.T) {
	var mu sync.Mutex
	var filters string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", "1.47")
		w.Header().Set("Ostype", "linux")
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			_, _ = w.Write([]byte("OK"))
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			mu.Lock()
			filters = r.URL.Query().Get("filters")
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cli, err := client.NewClientWithOpts(client.WithHost("tcp://" + strings.TrimPrefix(srv.URL, "http://")))
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	out, err := (&DockerEngine{Client: cli}).Earners(context.Background())
	if err != nil || len(out) != 0 {
		t.Fatalf("Earners = %v, %v; want none and no error", out, err)
	}
	mu.Lock()
	defer mu.Unlock()
	var got map[string]map[string]bool
	if err := json.Unmarshal([]byte(filters), &got); err != nil {
		t.Fatalf("filters %q: %v", filters, err)
	}
	if !got["label"]["cashpilot.managed=true"] || !got["label"]["cashpilot.runtime=existing-docker"] || !got["status"]["running"] {
		t.Fatalf("filters = %v, want Desktop's two labels and running", got)
	}
}
