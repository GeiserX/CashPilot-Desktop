package collectors

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestOceanReadsUnpaidBalance(t *testing.T) {
	const address = "bc1q4efd2vns8qwawg3nt4xxzs9053shh0q3x0tuu9"
	registry := newTestRegistry(t, &stubTransport{routes: map[string]stubResponse{
		"GET https://api.ocean.xyz/v1/statsnap/" + address: {
			body: `{"result":{"unpaid":"0.00571777","estimated_payout_next_block":"0.00000000"}}`,
		},
	}})
	creds := map[string]string{
		"OCEAN_ADDRESS": " " + strings.ToUpper(address) + " ",
	}
	if err := registry.store.SaveCredentials("ocean", creds); err != nil {
		t.Fatal(err)
	}
	record, err := registry.CollectSaved(context.Background(), "ocean", creds)
	if err != nil || record.Error != "" || record.Currency != "BTC" || record.Balance != 0.00571777 {
		t.Fatalf("collect = %+v, %v", record, err)
	}
	if hash, ok := PoolAddressHash("ocean", creds); !ok || hash != poolAddressHash(address) || hash != record.PoolAddressHash {
		t.Fatal("saved Bech32 address and collector result must use the same normalized hash")
	}
}

func TestOceanUnsuccessfulReadingDoesNotBecomeABalance(t *testing.T) {
	const address = "bc1qwvp8mr0gt96legu36rdc5kp2vwpzerefdspyu2"
	tests := []struct {
		name    string
		body    string
		status  int
		message string
		kind    string
	}{
		{name: "unknown address", body: `{"error":"No such user/worker"}`, message: "has no record"},
		{name: "missing amount", body: `{"result":{}}`, message: "unpaid balance", kind: "shape"},
		{name: "negative amount", body: `{"result":{"unpaid":"-1"}}`, message: "unpaid balance", kind: "shape"},
		{name: "bad amount", body: `{"result":{"unpaid":"NaN"}}`, message: "unpaid balance", kind: "shape"},
		{name: "API unavailable", status: http.StatusServiceUnavailable, message: "Try again shortly", kind: "transient"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := newTestRegistry(t, &stubTransport{routes: map[string]stubResponse{
				"GET https://api.ocean.xyz/v1/statsnap/" + address: {status: tt.status, body: tt.body},
			}})
			record, err := registry.Collect(context.Background(), "ocean", map[string]string{"OCEAN_ADDRESS": address})
			if err != nil || !strings.Contains(record.Error, tt.message) || record.ErrorKind != tt.kind {
				t.Fatalf("collect = %+v, %v", record, err)
			}
			if got := registry.store.ListDailyBalances(1); len(got) != 0 {
				t.Fatalf("failed read entered the balance history: %+v", got)
			}
		})
	}
}

func TestOceanRejectsMalformedAddressBeforeRequest(t *testing.T) {
	transport := &countingTransport{}
	registry := NewRegistry(nil, WithHTTPClient(&http.Client{Transport: transport}))
	_, err := registry.collectOcean(context.Background(), map[string]string{
		"OCEAN_ADDRESS": "bc1q4efd2vns8qwawg3nt4xxzs9053shh0q3x0tuu9.rig",
	})
	if err == nil || !strings.Contains(err.Error(), "Bitcoin payout address") || transport.calls.Load() != 0 {
		t.Fatalf("collect error = %v with %d requests", err, transport.calls.Load())
	}
}

func TestOceanAcceptsReportedZero(t *testing.T) {
	const address = "bc1q4efd2vns8qwawg3nt4xxzs9053shh0q3x0tuu9"
	registry := newTestRegistry(t, &stubTransport{routes: map[string]stubResponse{
		"GET https://api.ocean.xyz/v1/statsnap/" + address: {body: `{"result":{"unpaid":"0.00000000"}}`},
	}})
	record, err := registry.Collect(context.Background(), "ocean", map[string]string{"OCEAN_ADDRESS": address})
	if err != nil || record.Error != "" || record.Currency != "BTC" || record.Balance != 0 {
		t.Fatalf("collect = %+v, %v", record, err)
	}
}
