package collectors

import "testing"

func TestParseBytelixirBalance(t *testing.T) {
	balance, ok := parseBytelixirBalance(`<span>$</span>0.04<span class="text-2xs">025</span>`)
	if !ok {
		t.Fatal("expected balance to parse")
	}
	if balance != 0.04025 {
		t.Fatalf("unexpected balance: %f", balance)
	}
}

func TestParsePacketStreamBalance(t *testing.T) {
	balance, ok := parsePacketStreamBalance(`<h3>Balance</h3><div><h2 class="x">$1.23</h2></div>`)
	if !ok {
		t.Fatal("expected balance to parse")
	}
	if balance != 1.23 {
		t.Fatalf("unexpected balance: %f", balance)
	}
}

func TestStorjPayoutUSD(t *testing.T) {
	balance := storjPayoutUSD(map[string]any{
		"currentMonth": map[string]any{
			"egressBandwidthPayout":   float64(100),
			"egressRepairAuditPayout": float64(50),
			"diskSpacePayout":         float64(25),
		},
	})
	if balance != 1.75 {
		t.Fatalf("unexpected balance: %f", balance)
	}
}
