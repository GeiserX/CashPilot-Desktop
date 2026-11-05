package catalog

import (
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"gopkg.in/yaml.v3"
)

// No catalog entry may use the custom- prefix: it would take over a user's service
// and hand it whatever the entry grants. The loader skips one; the entry beside it
// is the control.
func TestLoaderSkipsTheReservedCustomPrefix(t *testing.T) {
	cat, err := LoadEmbedded(fstest.MapFS{
		"services/bandwidth/ok.yml":    {Data: []byte("name: Ok\nslug: ok\ncategory: bandwidth\nstatus: active\ndocker:\n  image: a/b:1\n")},
		"services/bandwidth/taken.yml": {Data: []byte("name: Taken\nslug: custom-taken\ncategory: bandwidth\nstatus: active\ndocker:\n  image: a/b:1\n  cap_add: [NET_ADMIN]\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cat.Get("ok"); !ok {
		t.Fatal("control: the ordinary entry was not loaded")
	}
	if _, ok := cat.Get("custom-taken"); ok {
		t.Fatal("an entry with the reserved custom- prefix was loaded")
	}
}

// The vendored catalog holds no reserved slug. Read raw, past the loader that would
// skip one, so the check can fail.
func TestVendoredCatalogHasNoCustomSlug(t *testing.T) {
	services, err := loadFromFSUnfiltered(os.DirFS("../.."), "services")
	if err != nil || len(services) < 20 {
		t.Fatalf("vendored catalog: %d services, %v", len(services), err)
	}
	for _, svc := range services {
		if IsCustomSlug(svc.Slug) {
			t.Errorf("%s uses the reserved %q prefix", svc.Slug, CustomPrefix)
		}
	}
}

// SetCustom adds the user's services to every lookup, and refuses anything that is
// not marked custom or does not carry the prefix, or that would shadow an entry.
func TestSetCustom(t *testing.T) {
	cat := newCatalog([]Service{{Name: "Ok", Slug: "ok"}})
	cat.SetCustom([]Service{
		{Name: "Mine", Slug: "custom-mine", Custom: true},
		{Name: "Unmarked", Slug: "custom-unmarked"},
		{Name: "Impostor", Slug: "ok", Custom: true},
	})
	if svc, ok := cat.Get("custom-mine"); !ok || !svc.Custom {
		t.Fatalf("Get(custom-mine) = %+v, %v", svc, ok)
	}
	if _, ok := cat.Get("custom-unmarked"); ok {
		t.Fatal("an unmarked service was added")
	}
	if svc, _ := cat.Get("ok"); svc.Custom {
		t.Fatal("a custom service shadowed a catalog entry")
	}
	if n := len(cat.ListVisible()); n != 2 {
		t.Fatalf("ListVisible has %d services, want the entry and the custom one", n)
	}
	cat.SetCustom(nil)
	if _, ok := cat.Get("custom-mine"); ok {
		t.Fatal("a removed custom service is still found")
	}
}

// loadFromFSUnfiltered reads every entry's slug with no filtering at all.
func loadFromFSUnfiltered(fsys fs.FS, root string) ([]Service, error) {
	var out []Service
	err := fs.WalkDir(fsys, root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || !strings.HasSuffix(path, ".yml") {
			return walkErr
		}
		raw, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		var svc Service
		if err := yaml.Unmarshal(raw, &svc); err != nil {
			return nil
		}
		out = append(out, svc)
		return nil
	})
	return out, err
}
