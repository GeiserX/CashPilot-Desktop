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
