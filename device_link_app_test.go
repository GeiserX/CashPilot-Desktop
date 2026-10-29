package main

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// fakeDevices answers the device-on-account questions without a provider.
type fakeDevices struct {
	*fakeCollector
	unlinked map[string][]string
	register bool
	asked    []string
}

func (f *fakeDevices) Unlinked(slug string) []string { return f.unlinked[slug] }

func (f *fakeDevices) RegisterDevice(_ context.Context, slug string, _ map[string]string) bool {
	f.asked = append(f.asked, slug)
	return f.register
}

func deviceLinkCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	cat, err := catalog.LoadEmbedded(fstest.MapFS{
		"services/bandwidth/linked.yml": {Data: []byte("name: Linked\nslug: linked\ncategory: bandwidth\nstatus: active\n" +
			"docker:\n  image: org/linked@sha256:x\n  env:\n    - key: UUID\n      label: Device UUID\n" +
			"device_link:\n  id_env: UUID\n  url: \"https://example.test/r/${UUID}\"\n  steps: Open the link.\n")},
		"services/bandwidth/plain.yml": {Data: []byte("name: Plain\nslug: plain\ncategory: bandwidth\nstatus: active\ndocker:\n  image: org/plain@sha256:x\n")},
	})
	if err != nil {
		t.Fatalf("catalog.LoadEmbedded: %v", err)
	}
	return cat
}

func TestADeviceTheProviderDoesNotKnowIsAWarning(t *testing.T) {
	devices := &fakeDevices{
		fakeCollector: newFakeCollector(nil, nil),
		unlinked:      map[string][]string{"linked": {"dev-1"}, "plain": {"dev-2"}},
	}
	app := &App{catalog: deviceLinkCatalog(t), collectors: devices}

	items := app.notifications(runtime.Status{Available: true}, nil, []store.Deployment{{Slug: "linked"}})
	var found *Notification
	for i := range items {
		if strings.Contains(items[i].Title, "not on your account") {
			found = &items[i]
		}
	}
	if found == nil || found.Level != "warning" || !strings.Contains(found.Message, "dev-1 https://example.test/r/dev-1") {
		t.Fatalf("want a warning naming the device and its link, got %+v", items)
	}

	// Only for what is deployed here: saved credentials alone are not a device.
	items = app.notifications(runtime.Status{Available: true}, nil, []store.Deployment{{Slug: "plain"}})
	for _, it := range items {
		if strings.Contains(it.Title, "not on your account") {
			t.Fatalf("a service without a device link warned: %+v", it)
		}
	}
}

func TestDeployRecordsADeviceTheProviderAdded(t *testing.T) {
	devices := &fakeDevices{fakeCollector: newFakeCollector(nil, nil), register: true}
	app := &App{ctx: context.Background(), catalog: deviceLinkCatalog(t), collectors: devices}

	app.registerDevice("linked", map[string]string{"UUID": " dev-1 "})
	if got := app.addedDeviceIDs()["linked"]; got != "dev-1" {
		t.Fatalf("addedDevices[linked] = %q, want dev-1", got)
	}

	devices.register = false
	app.registerDevice("linked", map[string]string{"UUID": "dev-1"})
	if _, ok := app.addedDeviceIDs()["linked"]; ok {
		t.Fatal("a failed add must leave the manual step, not a stale 'added'")
	}

	app.registerDevice("plain", map[string]string{"UUID": "dev-2"})
	if slices := devices.asked; len(slices) != 2 {
		t.Fatalf("a service without a device link must not reach the provider, asked %v", slices)
	}
}
