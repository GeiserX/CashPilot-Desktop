package catalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The deploy output reads service.deviceLink.{idEnv,url,steps} from the JSON the
// frontend gets. Decoded into its own struct rather than DeviceLink, so a renamed
// JSON tag fails here instead of passing through a symmetric round trip.
func TestDeviceLinkReachesTheFrontendForEarnAppAndProxyRack(t *testing.T) {
	cat, err := LoadEmbedded(os.DirFS(filepath.Join("..", "..")))
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	for _, slug := range []string{"earnapp", "proxyrack"} {
		svc, ok := cat.Get(slug)
		if !ok {
			t.Fatalf("%s missing from the catalog", slug)
		}
		raw, err := json.Marshal(svc)
		if err != nil {
			t.Fatalf("marshal %s: %v", slug, err)
		}
		var wire struct {
			DeviceLink *struct {
				IDEnv string `json:"idEnv"`
				URL   string `json:"url"`
				Steps string `json:"steps"`
			} `json:"deviceLink"`
		}
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatalf("unmarshal %s: %v", slug, err)
		}
		if wire.DeviceLink == nil || wire.DeviceLink.IDEnv == "" || wire.DeviceLink.URL == "" || wire.DeviceLink.Steps == "" {
			t.Errorf("%s: deviceLink does not reach the frontend whole: %+v", slug, wire.DeviceLink)
		}
	}
}
