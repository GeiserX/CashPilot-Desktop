package upstream

// Handing the server the history this machine collected before it was paired.
//
// # WHY
//
// Desktop runs standalone by default and collects earnings into its own store.
// Pairing it with a CashPilot server used to mean the fleet view began on the
// day of pairing: every earlier day this machine had recorded was simply absent
// from the total, and there was no way to get it there.
//
// # WHY IT IS NOT A MERGE
//
// Both sides may have been reading the SAME provider account. Earnings are
// stored as cumulative balance READINGS, and an earned figure is the clamped
// delta between consecutive readings — so interleaving two samplers of one
// account makes every apparent drop clamp to zero, and the total comes out
// systematically understated. The server therefore files each client's readings
// under that client's own source and differences the series separately.
//
// That is also what makes unlinking coherent: nothing here deletes or moves the
// local rows, so a machine that stops being paired still holds exactly what it
// earned on its own.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/fleetnet"
)

// ImportChunk is how many readings go in one request.
//
// The server refuses a body over 2000 readings, so this leaves headroom rather
// than sitting on the limit: a client that sends exactly the maximum breaks the
// moment either side's idea of the cap moves by one.
const ImportChunk = 1000

// ImportReading is one historical balance reading, in the server's shape.
//
// FXRateUSD is a POINTER so an unknown rate is sent as absent rather than as
// 0.0. Desktop does not record the exchange rate that was live on a past day,
// and a zero rate would price that whole day's balance at nothing.
type ImportReading struct {
	Slug      string   `json:"slug"`
	Balance   float64  `json:"balance"`
	Date      string   `json:"date"`
	Currency  string   `json:"currency,omitempty"`
	FXRateUSD *float64 `json:"fx_rate_usd,omitempty"`
}

// ImportPayload is the body POST /api/workers/earnings-import expects.
//
// It carries no source field, and must not gain one: the server takes the
// source from the AUTHENTICATED worker precisely so no client can write into
// another's history.
type ImportPayload struct {
	ClientID string          `json:"client_id"`
	Readings []ImportReading `json:"readings"`
}

// ImportResponse is what the server reports back. Skipped names the readings it
// declined — a slug its catalog does not know — so they can be logged rather
// than silently dropped, which would look identical to a successful import.
type ImportResponse struct {
	Status   string   `json:"status"`
	Imported int      `json:"imported"`
	Skipped  []string `json:"skipped"`
	Source   string   `json:"source"`
}

// ErrImportUnsupported means the server has no earnings-import endpoint — it
// predates the feature. Distinct from a transient failure because the caller
// must stop asking rather than retry: a CashPilot older than v1.16.0 will
// answer 404 on every heartbeat for as long as it runs.
var ErrImportUnsupported = errors.New("upstream: this CashPilot server does not accept an earnings import")

// Confirmed reports whether this Desktop is fully enrolled with the server.
//
// Only a confirmed worker may import: "still enrolling" means we authenticated
// with the SHARED key, which every worker on the fleet holds, and the server
// refuses an import from a shared-key holder for that reason.
//
// The signal is the pair of keys around one heartbeat. We are confirmed when we
// SENT this machine's own key and the server did not send one back — the server
// re-delivers the key on every heartbeat until the worker proves receipt by
// authenticating with it, so a key in the response means enrolment is still in
// flight.
func Confirmed(sentWorkerKey, receivedWorkerKey string) bool {
	return strings.TrimSpace(sentWorkerKey) != "" && strings.TrimSpace(receivedWorkerKey) == ""
}

// ChunkReadings splits readings into request-sized batches.
//
// Pure, so the boundary cases are tested without a server: an empty history
// yields no requests at all (not one empty request), and an exact multiple of
// the chunk size does not produce a trailing empty batch.
func ChunkReadings(readings []ImportReading, size int) [][]ImportReading {
	if size <= 0 {
		size = ImportChunk
	}
	var out [][]ImportReading
	for start := 0; start < len(readings); start += size {
		end := start + size
		if end > len(readings) {
			end = len(readings)
		}
		out = append(out, readings[start:end])
	}
	return out
}

// Import posts one batch of readings and returns what the server recorded.
func (c *Client) Import(ctx context.Context, serverURL, token string, p ImportPayload) (*ImportResponse, error) {
	serverURL = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	if serverURL == "" {
		return nil, ErrNotPaired
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("upstream: no credential to authenticate with")
	}
	// The same SSRF policy the heartbeat gets. The URL is user-supplied and the
	// request carries a bearer token; sending earnings to it is not a reason to
	// validate it less.
	if err := fleetnet.ValidateWorkerURL(serverURL, c.Policy); err != nil {
		return nil, fmt.Errorf("upstream: refusing to contact %s: %w", serverURL, err)
	}

	body, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("upstream: encoding earnings import: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL+"/api/workers/earnings-import", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("upstream: building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	client := c.HTTP
	if client == nil {
		client = defaultHTTPClient()
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upstream: earnings import failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("upstream: reading response: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		// A server too old to have the endpoint. Distinguished from every other
		// failure because it is not transient: retrying it once a minute forever
		// would fill the log with an error the user cannot act on except by
		// upgrading, which they will not do because of a log line.
		return nil, ErrImportUnsupported
	}
	if resp.StatusCode != http.StatusOK {
		// 403 here is its own diagnosis and worth keeping legible: it means the
		// server still considers this worker unconfirmed, so the fix is another
		// heartbeat, not a new key.
		return nil, fmt.Errorf("upstream: server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out ImportResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("upstream: decoding response: %w", err)
	}
	return &out, nil
}
