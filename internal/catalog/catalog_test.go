package catalog

import (
	"testing"
	"testing/fstest"
)

func TestLoadEmbeddedCatalog(t *testing.T) {
	fsys := fstest.MapFS{
		"services/bandwidth/example.yml": {
			Data: []byte(`
name: Example
slug: example
category: bandwidth
status: active
docker:
  image: example/image
  env:
    - key: TOKEN
      label: Token
      required: true
`),
		},
	}

	cat, err := LoadEmbedded(fsys)
	if err != nil {
		t.Fatalf("LoadEmbedded returned error: %v", err)
	}
	svc, ok := cat.Get("example")
	if !ok {
		t.Fatal("expected example service")
	}
	if svc.Docker.Image != "example/image" {
		t.Fatalf("unexpected image: %s", svc.Docker.Image)
	}
	if svc.ManualOnly {
		t.Fatal("docker-backed service should not be manual-only")
	}
}

// TestLoadEmbeddedParsesDockerResources verifies the optional docker.resources
// block maps into DockerConfig.Resources, and that a service without the block
// leaves the fields at their zero values (empty strings and a nil OomScoreAdj, so
// an absent OOM score is distinguishable from an explicit 0).
func TestLoadEmbeddedParsesDockerResources(t *testing.T) {
	fsys := fstest.MapFS{
		"services/bandwidth/limited.yml": {
			Data: []byte(`
name: Limited
slug: limited
category: bandwidth
status: active
docker:
  image: example/limited
  resources:
    mem_limit: "256m"
    mem_reservation: "128m"
    oom_score_adj: -100
`),
		},
		"services/bandwidth/plain.yml": {
			Data: []byte(`
name: Plain
slug: plain
category: bandwidth
status: active
docker:
  image: example/plain
`),
		},
	}

	cat, err := LoadEmbedded(fsys)
	if err != nil {
		t.Fatalf("LoadEmbedded returned error: %v", err)
	}

	limited, ok := cat.Get("limited")
	if !ok {
		t.Fatal("expected limited service")
	}
	res := limited.Docker.Resources
	if res.MemLimit != "256m" {
		t.Fatalf("MemLimit = %q, want %q", res.MemLimit, "256m")
	}
	if res.MemReservation != "128m" {
		t.Fatalf("MemReservation = %q, want %q", res.MemReservation, "128m")
	}
	if res.OomScoreAdj == nil || *res.OomScoreAdj != -100 {
		t.Fatalf("OomScoreAdj = %v, want -100", res.OomScoreAdj)
	}

	plain, ok := cat.Get("plain")
	if !ok {
		t.Fatal("expected plain service")
	}
	if pr := plain.Docker.Resources; pr.MemLimit != "" || pr.MemReservation != "" || pr.OomScoreAdj != nil {
		t.Fatalf("expected empty resources for a service without the block, got %+v", pr)
	}
}

// TestLoadEmbeddedParsesNativeBlock verifies the optional native: block parses, that a
// native-only service (no Docker image) is NOT manual-only, that HasNative reports it,
// and that NativeBinaryFor selects by OS/arch.
func TestLoadEmbeddedParsesNativeBlock(t *testing.T) {
	fsys := fstest.MapFS{
		"services/bandwidth/nativeonly.yml": {
			Data: []byte(`
name: Native Only
slug: native-only
category: bandwidth
status: active
native:
  binaries:
    - os: linux
      arch: amd64
      url: "https://example.com/tool_linux_amd64.tar.gz"
      sha256: "abc123"
      archive: tar.gz
      bin: "tool"
    - os: darwin
      arch: arm64
      url: "https://example.com/tool_darwin_arm64.tar.gz"
      sha256: "def456"
      archive: tar.gz
      bin: "tool"
  command: "--flag ${TOKEN} run"
  env:
    - key: TOKEN
      required: true
`),
		},
	}

	cat, err := LoadEmbedded(fsys)
	if err != nil {
		t.Fatalf("LoadEmbedded returned error: %v", err)
	}
	svc, ok := cat.Get("native-only")
	if !ok {
		t.Fatal("expected native-only service")
	}
	if !svc.HasNative() {
		t.Fatal("HasNative should be true for a service with a native block")
	}
	if svc.ManualOnly {
		t.Fatal("a native-capable service must not be manual-only")
	}
	if len(svc.Native.Binaries) != 2 {
		t.Fatalf("expected 2 native binaries, got %d", len(svc.Native.Binaries))
	}
	if svc.Native.Command != "--flag ${TOKEN} run" {
		t.Fatalf("unexpected native command: %q", svc.Native.Command)
	}
	bin, ok := svc.NativeBinaryFor("darwin", "arm64")
	if !ok || bin.URL != "https://example.com/tool_darwin_arm64.tar.gz" {
		t.Fatalf("NativeBinaryFor(darwin,arm64) = %+v ok=%v", bin, ok)
	}
	if _, ok := svc.NativeBinaryFor("windows", "amd64"); ok {
		t.Fatal("NativeBinaryFor should report no binary for an undeclared os/arch")
	}
}
