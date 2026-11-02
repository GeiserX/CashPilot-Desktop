package main

import (
	"context"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/noderead"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
	"github.com/zalando/go-keyring"
)

// A Mysterium node Desktop runs natively has no container: it must be read through
// the native reader, not sent to a container exec that cannot work.
func TestNativeMysteriumIsReadNatively(t *testing.T) {
	keyring.MockInit()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cat, err := catalog.LoadEmbedded(serviceFiles)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertDeployment(store.Deployment{Slug: "mysterium", Name: "mysterium", Status: "running", Runtime: runtime.NativeRuntimeKind}); err != nil {
		t.Fatal(err)
	}
	old := noderead.NativeReaders["mysterium"]
	t.Cleanup(func() { noderead.NativeReaders["mysterium"] = old })
	called := false
	noderead.NativeReaders["mysterium"] = func(context.Context) (string, error) {
		called = true
		return `{"id":"0x1111111111111111111111111111111111111111","earnings_tokens":{"ether":"2"},"earnings_total_tokens":{"ether":"5"}}`, nil
	}
	app := &App{store: st, catalog: cat, services: services.NewManager(runtime.NewDockerProvider(), cat, st)}
	got, err := app.readNodes(context.Background(), "mysterium")
	if err != nil {
		t.Fatal(err)
	}
	if !called || len(got) != 1 || !got[0].OK() || got[0].Balance != 2 || got[0].Lifetime != 5 {
		t.Fatalf("got %+v (native reader called: %v), want one native reading of 2/5", got, called)
	}
	// A service without node_read keeps its own path.
	if _, err := app.readNodes(context.Background(), "honeygain"); err != noderead.ErrNotDeclared {
		t.Errorf("honeygain: err = %v, want ErrNotDeclared", err)
	}
}
