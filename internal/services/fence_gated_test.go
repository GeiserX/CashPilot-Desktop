package services

import (
	"context"
	"fmt"
	goruntime "runtime"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// gatedServiceYAML has Mysterium's network shape: no static ports, the WebUI and a
// UDP range published from the env, and fence-gated.
const gatedServiceYAML = `name: Gated Example
slug: gated-example
category: bandwidth
status: active
docker:
  image: example/gated@sha256:` + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" + `
  env:
    - key: UI_ADDRESS
      default: "127.0.0.1"
    - key: UDP_PORTS
      default: "23000:23029"
      port_range: true
  ui_port:
    env: UI_ADDRESS
    port: 4449
  udp_port_range:
    env: UDP_PORTS
    max_ports: 100
  fence_gated: true
`

func newGatedCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	cat, err := catalog.LoadEmbedded(fstest.MapFS{"services/bandwidth/gated.yml": {Data: []byte(gatedServiceYAML)}})
	if err != nil {
		t.Fatalf("LoadEmbedded error: %v", err)
	}
	return cat
}

// It holds NET_ADMIN, so it runs only the image the catalog pins.
func TestAFenceGatedServiceRefusesAChosenImage(t *testing.T) {
	fake := &fakeProvider{}
	m := NewManager(fake, newGatedCatalog(t), newTestStore(t))
	_, err := m.DeployImage(context.Background(), "gated-example", nil, strPtr("example/other:1.0"))
	if err == nil || !strings.Contains(err.Error(), "only the image the catalog pins") {
		t.Fatalf("err = %v, want a refusal of the chosen image", err)
	}
	if fake.deployCalls != 0 {
		t.Fatalf("the runtime was called %d times for a refused image", fake.deployCalls)
	}
	if _, err := m.DeployImage(context.Background(), "gated-example", nil, strPtr("")); err != nil {
		t.Fatalf("the catalog image was refused: %v", err)
	}
}

// A range wider than the entry's cap passes the pattern, so the deploy validation
// must catch it before the runtime is reached.
func TestAPublishedValueThatCannotBePublishedIsRefusedBeforeTheRuntime(t *testing.T) {
	fake := &fakeProvider{}
	m := NewManager(fake, newGatedCatalog(t), newTestStore(t))
	for _, creds := range []map[string]string{
		{"UDP_PORTS": "23000:23100"},
		{"UI_ADDRESS": "999.0.0.1"},
	} {
		if err := m.ValidateCredentials("gated-example", creds); err == nil {
			t.Errorf("%v validated", creds)
		}
		if _, err := m.Deploy(context.Background(), "gated-example", creds); err == nil {
			t.Errorf("%v deployed", creds)
		}
	}
	if fake.deployCalls != 0 {
		t.Fatalf("the runtime was called %d times for refused values", fake.deployCalls)
	}
	if err := m.ValidateCredentials("gated-example", map[string]string{"UDP_PORTS": "23000:23099"}); err != nil {
		t.Fatalf("a range of exactly the cap was refused: %v", err)
	}
}

// Ports published from the env still belong to one container.
func TestAServicePublishingFromItsEnvRunsOnce(t *testing.T) {
	m := NewManager(&fakeProvider{}, newGatedCatalog(t), newTestStore(t))
	svc, _ := m.catalog.Get("gated-example")
	if limit := m.ScalingLimit(svc); limit.Max != 1 || !strings.Contains(limit.Reason, "port") {
		t.Fatalf("ScalingLimit = %+v, want one copy because of its published ports", limit)
	}
}

// The native node publishes nothing and ignores the UDP range, so a range the Docker
// path would refuse does not stop a native deploy.
func TestTheNativeNodeIsNotHeldToWhatDockerPublishes(t *testing.T) {
	yaml := gatedServiceYAML + fmt.Sprintf(`native:
  binaries:
    - os: %s
      arch: %s
      url: "https://example.test/gated"
      sha256: "%s"
      archive: none
      bin: "gatedstub"
  command: ""
`, goruntime.GOOS, goruntime.GOARCH, strings.Repeat("0", 64))
	cat, err := catalog.LoadEmbedded(fstest.MapFS{"services/bandwidth/gated.yml": {Data: []byte(yaml)}})
	if err != nil {
		t.Fatalf("LoadEmbedded error: %v", err)
	}
	docker := &fakeProvider{}
	native := &fakeProvider{deployResult: runtime.ContainerInfo{ContainerID: "n1", Status: "running"}}
	m := NewManager(docker, cat, newTestStore(t))
	m.Register(nativeRuntimeKind, native)
	wide := map[string]string{"UDP_PORTS": "20000:30000"}
	if err := m.ValidateCredentials("gated-example", wide); err != nil {
		t.Fatalf("ValidateCredentials refused a range the native node ignores: %v", err)
	}
	if _, err := m.Deploy(context.Background(), "gated-example", wide); err != nil {
		t.Fatalf("native deploy refused: %v", err)
	}
	if native.deployCalls != 1 || docker.deployCalls != 0 {
		t.Fatalf("native=%d docker=%d, want the native runtime", native.deployCalls, docker.deployCalls)
	}
}
