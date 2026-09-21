package collectors

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// countingTransport answers every request with an empty JSON body and counts the
// calls. It never touches the network, and 200 keeps the shared retry/backoff path
// out of the way — what matters here is only WHETHER a collector got far enough to
// make its request.
type countingTransport struct{ calls atomic.Int64 }

func (t *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       http.NoBody,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Request:    req,
	}, nil
}

// wizardFormCredentials builds exactly what the user can type into a service's
// wizard card: every catalog docker.env variable plus every collector-only Field the
// backend declares for that slug (frontend/src/render/fields.ts composes the same two
// lists). A URL-shaped placeholder goes into *_URL keys so the request still builds.
func wizardFormCredentials(svc catalog.Service) map[string]string {
	creds := map[string]string{}
	put := func(key string) {
		if strings.HasSuffix(key, "_URL") {
			creds[key] = "http://collector.invalid/"
			return
		}
		creds[key] = "wizard-value"
	}
	for _, env := range svc.Docker.Env {
		put(env.Key)
	}
	for _, field := range Fields(svc.Slug) {
		put(field.Key)
	}
	return creds
}

// THE RULE: a collector may only require credentials the user can actually give it.
//
// The wizard form is the catalog's docker.env plus the collector fields the backend
// declares, and nothing else. A collector that reads a key neither half offers is
// unusable by construction: it answers "<X> is required" forever, no matter what the
// user types, and nothing in the UI hints at what is missing.
//
// That is not hypothetical. The web catalog renamed Repocket's container contract to
// RP_EMAIL/RP_API_KEY; the collector kept signing in to Firebase with
// REPOCKET_EMAIL/REPOCKET_PASSWORD, the frontend's hand-kept field table gained no
// entry, and Repocket earnings collection could only fail.
//
// "Reachable" is asserted behaviourally: filled from the form's keys alone, the
// collector must get past its credential gate and issue its HTTP request. A missing
// key returns before any request, so the call count is the observable difference.
func TestEveryCollectorCanReachItsCredentialsFromTheWizardForm(t *testing.T) {
	cat, err := catalog.LoadEmbedded(os.DirFS(filepath.Join("..", "..")))
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	// Two collectors need an app-level API key beyond the user's own credentials
	// (build-time configuration, not something the form asks for). Without them they
	// stop before the request for a reason that is not about the form.
	t.Setenv("CASHPILOT_REPOCKET_FIREBASE_KEY", "test-firebase-key")
	t.Setenv("CASHPILOT_EARNFM_SUPABASE_ANON_KEY", "test-anon-key")

	slugs := make([]string, 0, len(collectorDispatch))
	for slug := range collectorDispatch {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)

	for _, slug := range slugs {
		t.Run(slug, func(t *testing.T) {
			svc, ok := cat.Get(slug)
			if !ok {
				t.Fatalf("%s has a collector but no catalog entry", slug)
			}
			transport := &countingTransport{}
			registry := &Registry{http: &http.Client{Transport: transport}}
			creds := wizardFormCredentials(svc)

			_, _ = collectorDispatch[slug](registry, context.Background(), creds)

			if transport.calls.Load() == 0 {
				keys := make([]string, 0, len(creds))
				for key := range creds {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				_, gateErr := collectorDispatch[slug](registry, context.Background(), map[string]string{})
				t.Fatalf("the %s collector never issued a request when filled from the wizard form.\n"+
					"form keys: %v\nwith no credentials at all it says: %v\n"+
					"add the credentials it reads to collectorFields in internal/collectors/fields.go",
					slug, keys, gateErr)
			}
		})
	}
}

// A collector field for a slug with no collector is an input nobody reads: the user
// types a password into a box whose only consumer does not exist.
func TestEveryCollectorFieldBelongsToACollector(t *testing.T) {
	for _, slug := range FieldSlugs() {
		if _, ok := collectorDispatch[slug]; !ok {
			t.Errorf("collectorFields declares inputs for %q, which has no collector", slug)
		}
	}
}

// Fields hands out copies: a caller that mutates what it got (the frontend's map goes
// out through AppState, and app_test builds on it) must not edit the table itself.
func TestFieldsReturnsACopy(t *testing.T) {
	first := Fields("repocket")
	if len(first) == 0 {
		t.Fatal("repocket declares no collector fields")
	}
	first[0].Key = "MUTATED"
	if Fields("repocket")[0].Key == "MUTATED" {
		t.Error("Fields handed out the table itself; a caller can rewrite every wizard form")
	}
}
