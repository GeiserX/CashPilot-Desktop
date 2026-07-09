package collectors

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// This file pins the per-node earnings extension of collectMystNodes. It reuses the
// stub round-tripper + temp-store Registry from collectors_http_test.go
// (newTestRegistry / stubTransport / stubResponse): after login + total-earnings, the
// collector also fetches GET /api/v2/node with the SAME bearer token and stashes a
// flattened per-node breakdown as JSON in the generic service_details store under the
// "mysterium" slug — while the flat Result balance stays the total-earnings figure.
//
// Route matching note: the per-node request URL is ".../node?page=1&itemsPerPage=100".
// stubTransport routes it via its longest-prefix rule to the "GET .../node" key, while
// ".../node/total-earnings" stays an exact match — the two never collide.

// TestCollectMystNodesPerNode drives the success path: the Result is the unchanged
// total, and the persisted detail JSON carries every node's mapped fields (identity,
// name, localIp, online, country code, version, summed 30-day earnings, lifetime split).
func TestCollectMystNodesPerNode(t *testing.T) {
	routes := map[string]stubResponse{
		"POST https://my.mystnodes.com/api/v2/auth/login":         {body: `{"accessToken":"myst-token"}`},
		"GET https://my.mystnodes.com/api/v2/node/total-earnings": {body: `{"earningsTotal":12.5}`},
		// node A: online, ES, two earnings rows (0.5 + 0.25 = 0.75 30-day), lifetime 3/2/1.
		// node B: offline, US, one earnings row (1.0), lifetime 5/5/0.
		"GET https://my.mystnodes.com/api/v2/node": {body: `{"nodes":[
			{"identity":"0xabc","name":"node-a","localIp":"192.168.1.10","version":"1.2.3","nodeStatus":{"online":true},"country":{"code":"ES"},"earnings":[{"etherAmount":0.5},{"etherAmount":0.25}],"lifetimeEarnings":{"totalEther":3.0,"settledEther":2.0,"unsettledEther":1.0}},
			{"identity":"0xdef","name":"node-b","localIp":"192.168.1.11","version":"1.2.4","nodeStatus":{"online":false},"country":{"code":"US"},"earnings":[{"etherAmount":1.0}],"lifetimeEarnings":{"totalEther":5.0,"settledEther":5.0,"unsettledEther":0.0}}
		]}`},
	}
	r := newTestRegistry(t, &stubTransport{routes: routes})

	res, err := r.collectMystNodes(context.Background(), map[string]string{"MYSTNODES_EMAIL": "a@b.co", "MYSTNODES_PASSWORD": "pw"})
	if err != nil {
		t.Fatalf("collectMystNodes error: %v", err)
	}
	if res.Platform != "mysterium" {
		t.Fatalf("Platform = %q, want mysterium", res.Platform)
	}
	if res.Currency != "MYST" {
		t.Fatalf("Currency = %q, want MYST", res.Currency)
	}
	if res.Balance != 12.5 {
		t.Fatalf("Balance = %v, want 12.5 (total-earnings, unchanged by the per-node fetch)", res.Balance)
	}

	// The per-node breakdown was stashed in the generic service_details store.
	detail, err := r.store.GetServiceDetail("mysterium")
	if err != nil {
		t.Fatalf("GetServiceDetail error: %v", err)
	}
	if detail == "" {
		t.Fatal("expected per-node detail JSON to be persisted under the mysterium slug")
	}
	// The raw JSON carries the per-node fields the frontend will render next cycle.
	for _, want := range []string{"identity", "0xabc", "online", "earnings30dMyst", "node-a", "192.168.1.10", "ES"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("persisted detail is missing %q: %s", want, detail)
		}
	}

	// Unmarshal and pin the exact mapped values: the summed 30-day earnings, the
	// lifetime split, the online bool, and the country code.
	var nodes []mystNode
	if err := json.Unmarshal([]byte(detail), &nodes); err != nil {
		t.Fatalf("persisted detail is not a valid mystNode slice: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d: %+v", len(nodes), nodes)
	}
	a := nodes[0]
	if a.Identity != "0xabc" || a.Name != "node-a" || a.LocalIP != "192.168.1.10" || a.Version != "1.2.3" {
		t.Fatalf("node A basic fields mismatch: %+v", a)
	}
	if !a.Online {
		t.Fatalf("node A should be online: %+v", a)
	}
	if a.Country != "ES" {
		t.Fatalf("node A country = %q, want ES (mapped from country.code)", a.Country)
	}
	if a.Earnings30dMYST != 0.75 {
		t.Fatalf("node A 30-day earnings = %v, want 0.75 (sum of earnings[].etherAmount)", a.Earnings30dMYST)
	}
	if a.LifetimeMYST != 3.0 || a.LifetimeSettledMYST != 2.0 || a.LifetimeUnsettledMYST != 1.0 {
		t.Fatalf("node A lifetime split mismatch: got %v/%v/%v, want 3/2/1",
			a.LifetimeMYST, a.LifetimeSettledMYST, a.LifetimeUnsettledMYST)
	}
	b := nodes[1]
	if b.Identity != "0xdef" || b.Online {
		t.Fatalf("node B should be 0xdef and offline: %+v", b)
	}
	if b.Country != "US" {
		t.Fatalf("node B country = %q, want US", b.Country)
	}
	if b.Earnings30dMYST != 1.0 {
		t.Fatalf("node B 30-day earnings = %v, want 1.0", b.Earnings30dMYST)
	}
	if b.LifetimeMYST != 5.0 || b.LifetimeSettledMYST != 5.0 || b.LifetimeUnsettledMYST != 0.0 {
		t.Fatalf("node B lifetime split mismatch: got %v/%v/%v, want 5/5/0",
			b.LifetimeMYST, b.LifetimeSettledMYST, b.LifetimeUnsettledMYST)
	}
}

// TestCollectMystNodesPerNodeBestEffort pins the robustness contract: when the per-node
// /node fetch fails (HTTP 500 after retries), the collector STILL returns the flat
// total-earnings Result without error, and no per-node detail is persisted. The bonus
// breakdown must never take the whole collect down with it.
func TestCollectMystNodesPerNodeBestEffort(t *testing.T) {
	routes := map[string]stubResponse{
		"POST https://my.mystnodes.com/api/v2/auth/login":         {body: `{"accessToken":"myst-token"}`},
		"GET https://my.mystnodes.com/api/v2/node/total-earnings": {body: `{"earningsTotal":7}`},
		// The per-node list is down: 500 on every attempt (retries exhaust, then error).
		"GET https://my.mystnodes.com/api/v2/node": {status: 500, body: `{}`},
	}
	r := newTestRegistry(t, &stubTransport{routes: routes})

	res, err := r.collectMystNodes(context.Background(), map[string]string{"MYSTNODES_EMAIL": "a@b.co", "MYSTNODES_PASSWORD": "pw"})
	if err != nil {
		t.Fatalf("a per-node fetch failure must not fail the whole collect: %v", err)
	}
	if res.Platform != "mysterium" || res.Currency != "MYST" || res.Balance != 7 {
		t.Fatalf("Result = %+v, want Platform=mysterium Balance=7 Currency=MYST despite the per-node failure", res)
	}
	if res.Error != "" {
		t.Fatalf("Result.Error = %q, want empty (the total succeeded; per-node is best-effort)", res.Error)
	}

	// A failed per-node fetch persists nothing, leaving the detail slug absent ("").
	detail, err := r.store.GetServiceDetail("mysterium")
	if err != nil {
		t.Fatalf("GetServiceDetail error: %v", err)
	}
	if detail != "" {
		t.Fatalf("expected no per-node detail after a failed fetch, got: %s", detail)
	}
}
