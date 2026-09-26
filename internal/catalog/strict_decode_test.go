package catalog

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// THE RULE: a key the web catalog ships is a key this loader reads.
//
// Load() uses a forgiving yaml.Unmarshal, which drops any field the Service struct
// does not declare without a word. The catalog is vendored from the web repository
// now, so a rename there arrives automatically — and arrives green. That already
// happened once: the web catalog renamed the collector's `credentials` block to
// `credential_hint`, the sync copied it in, every test passed, and the value was
// dropped on the floor at load.
//
// Strict decoding turns that silence into a failing test on the sync PR itself, which
// is the only moment anyone is looking at the change. It is deliberately a test rather
// than the loader's own mode: a user's app must still start when upstream adds a key,
// and it is the repository, not the running app, that has to notice.
func TestEveryVendoredKeyIsReadByTheLoader(t *testing.T) {
	root := filepath.Join("..", "..", "services")
	var checked int
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yml") || strings.HasPrefix(entry.Name(), "_") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		decoder := yaml.NewDecoder(bytes.NewReader(raw))
		decoder.KnownFields(true)
		var svc Service
		if err := decoder.Decode(&svc); err != nil {
			rel, _ := filepath.Rel(root, path)
			t.Errorf("%s carries something the loader does not read, so it is silently dropped at load: %v\n"+
				"add the field to catalog.Service (or, if Desktop deliberately ignores it, say so in catalog-overlay/README.md)",
				filepath.ToSlash(rel), err)
			return nil
		}
		checked++
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	// A zero-file walk would pass this test while checking nothing.
	if checked < 20 {
		t.Fatalf("strict-decoded only %d catalog files; the catalog did not load", checked)
	}
}

// Every pattern the web catalog declares must compile under Go's regexp engine too,
// and accept the variable's own default. The web app checks patterns with Python's
// re; a construct RE2 lacks would make Desktop refuse every value for that field.
func TestEveryVendoredPatternCompilesAndAcceptsItsDefault(t *testing.T) {
	cat, err := LoadEmbedded(os.DirFS(filepath.Join("..", "..")))
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	var checked int
	for _, svc := range cat.List() {
		for _, item := range svc.Docker.Env {
			if item.Pattern == "" {
				continue
			}
			checked++
			re, err := regexp.Compile(`^(?:` + item.Pattern + `)$`)
			if err != nil {
				t.Errorf("%s %s: pattern does not compile in Go: %v", svc.Slug, item.Key, err)
				continue
			}
			if item.Default != "" && !re.MatchString(item.Default) {
				t.Errorf("%s %s: pattern refuses its own default %q", svc.Slug, item.Key, item.Default)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no vendored entry declares a pattern; this test would pass by checking nothing")
	}
}
