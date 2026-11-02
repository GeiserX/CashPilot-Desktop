package collectors

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/noderead"
)

// The Mysterium collector with node_read declared: the balance is the nodes'
// unsettled MYST, never the cloud's trailing 30-day earningsTotal. No route answers
// total-earnings here, so a collector that still asked it would fail these tests.
func mystNodeRoutes() map[string]stubResponse {
	return map[string]stubResponse{
		"POST https://my.mystnodes.com/api/v2/auth/login": {body: `{"accessToken":"myst-token"}`},
		"GET https://my.mystnodes.com/api/v2/node": {body: `{"nodes":[
			{"identity":"0xAAA","name":"node-a","nodeStatus":{"online":true},"lifetimeEarnings":{"totalEther":0,"settledEther":0,"unsettledEther":0}}
		]}`},
	}
}

func withReadings(readings []noderead.Reading, err error) NodeReadFunc {
	return func(context.Context, string) ([]noderead.Reading, error) { return readings, err }
}

func TestMystBalanceIsTheNodesUnsettledTotal(t *testing.T) {
	r := newTestRegistry(t, &stubTransport{routes: mystNodeRoutes()})
	r.readNodes = withReadings([]noderead.Reading{
		{Instance: 1, Node: "0xaaa", Balance: 1.25, Lifetime: 9.5},
		{Instance: 2, Node: "0xAAA", Balance: 1.25, Lifetime: 9.5}, // the same node twice counts once
		{Instance: 3, Node: "0xbbb", Balance: 0.5, Lifetime: 2},
		{Instance: 4, Error: "no figures in the node's answer"},
	}, nil)
	res, err := r.collectMystNodes(context.Background(), map[string]string{"MYSTNODES_EMAIL": "a@b.co", "MYSTNODES_PASSWORD": "pw"})
	if err != nil {
		t.Fatalf("collectMystNodes: %v", err)
	}
	if res.Balance != 1.75 || res.Currency != "MYST" {
		t.Fatalf("got %v %s, want 1.75 MYST (1.25 + 0.5, each node once)", res.Balance, res.Currency)
	}
	detail, err := r.store.GetServiceDetail("mysterium")
	if err != nil {
		t.Fatal(err)
	}
	var nodes []mystNode
	if err := json.Unmarshal([]byte(detail), &nodes); err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("per-node detail = %+v, want the cloud's node plus the one only read here", nodes)
	}
	if a := nodes[0]; a.Name != "node-a" || a.LifetimeMYST != 9.5 || a.LifetimeUnsettledMYST != 1.25 || a.LifetimeSettledMYST != 8.25 {
		t.Errorf("node A = %+v, want the node's own lifetime 9.5 split 8.25 settled / 1.25 unsettled over the cloud's 0", a)
	}
	if b := nodes[1]; b.Identity != "0xbbb" || b.LifetimeMYST != 2 {
		t.Errorf("node B = %+v, want the node read here and missing from the cloud list", b)
	}
}

// No node read is an error, not the 30-day sum: a wrong figure that looks real is
// what this replaces.
func TestMystWithNoNodeReadIsAnErrorNotTheCloudSum(t *testing.T) {
	routes := mystNodeRoutes()
	routes["GET https://my.mystnodes.com/api/v2/node/total-earnings"] = stubResponse{body: `{"earningsTotal":12.5}`}
	for name, read := range map[string]NodeReadFunc{
		"no running node":     withReadings(nil, nil),
		"every read failed":   withReadings([]noderead.Reading{{Instance: 1, Error: "the read command exited with 1"}}, nil),
		"runtime cannot exec": withReadings(nil, errors.New("this runtime cannot run a command inside mysterium")),
	} {
		r := newTestRegistry(t, &stubTransport{routes: routes})
		r.readNodes = read
		res, err := r.collectMystNodes(context.Background(), map[string]string{"MYSTNODES_EMAIL": "a@b.co", "MYSTNODES_PASSWORD": "pw"})
		if err == nil || res.Balance != 0 {
			t.Errorf("%s: got %+v, err %v; want an error and no balance", name, res, err)
			continue
		}
		if !strings.Contains(err.Error(), "could be read") {
			t.Errorf("%s: error %q does not say the node could not be read", name, err)
		}
	}
}

// The control: a catalog without node_read keeps the account-API path, so the
// tests above pass only because the node path ran.
func TestMystWithoutNodeReadKeepsTheAccountPath(t *testing.T) {
	routes := mystNodeRoutes()
	routes["GET https://my.mystnodes.com/api/v2/node/total-earnings"] = stubResponse{body: `{"earningsTotal":12.5}`}
	r := newTestRegistry(t, &stubTransport{routes: routes})
	r.readNodes = withReadings(nil, noderead.ErrNotDeclared)
	res, err := r.collectMystNodes(context.Background(), map[string]string{"MYSTNODES_EMAIL": "a@b.co", "MYSTNODES_PASSWORD": "pw"})
	if err != nil || res.Balance != 12.5 {
		t.Fatalf("got %+v, err %v; want the earningsTotal path when node_read is not declared", res, err)
	}
}

// A wrong password still fails at the cloud login before any node is read.
func TestMystLoginStillGuardsTheNodePath(t *testing.T) {
	r := newTestRegistry(t, &stubTransport{routes: map[string]stubResponse{
		"POST https://my.mystnodes.com/api/v2/auth/login": {status: 401, body: `{"message":"bad credentials"}`},
	}})
	called := false
	r.readNodes = func(context.Context, string) ([]noderead.Reading, error) { called = true; return nil, nil }
	if _, err := r.collectMystNodes(context.Background(), map[string]string{"MYSTNODES_EMAIL": "a@b.co", "MYSTNODES_PASSWORD": "pw"}); err == nil {
		t.Fatal("a refused login must fail the collect")
	}
	if called {
		t.Error("nodes were read before the login was checked")
	}
}
