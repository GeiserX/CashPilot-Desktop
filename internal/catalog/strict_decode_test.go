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
		if entry.IsDir() && entry.Name() == I18nDir {
			// Translations, not services: they have their own shape.
			checkI18nFiles(t, path)
			return filepath.SkipDir
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

// The vendored seed_file block passes the same rules the web catalog applies, so
// a deploy never refuses Sia over it, and a broken block is caught by them.
func TestTheVendoredSeedFileIsValid(t *testing.T) {
	cat, err := LoadEmbedded(os.DirFS(filepath.Join("..", "..")))
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	svc, ok := cat.Get("sia-hostd")
	if !ok {
		t.Fatal("sia-hostd is not in the catalog")
	}
	item, ok := svc.SeedVar()
	if !ok || item.Key != "HOSTD_WALLET_SEED" || item.SeedFile.Path != "/data/hostd.yml" {
		t.Fatalf("sia-hostd has no seed_file on HOSTD_WALLET_SEED: %+v", item)
	}
	if problem := svc.SeedFileProblem(); problem != "" {
		t.Fatalf("the vendored seed_file is refused: %s", problem)
	}
	broken := svc
	broken.Docker.Env = append([]EnvVar(nil), svc.Docker.Env...)
	for i := range broken.Docker.Env {
		if broken.Docker.Env[i].SeedFile != nil {
			spec := *broken.Docker.Env[i].SeedFile
			spec.Path = "/elsewhere/hostd.yml"
			spec.PhrasePattern = "no group"
			broken.Docker.Env[i].SeedFile = &spec
			broken.Docker.Env[i].Secret = false
		}
	}
	problem := broken.SeedFileProblem()
	for _, want := range []string{"only for a secret", "folder docker.volumes mounts", "phrase_pattern must have exactly one group"} {
		if !strings.Contains(problem, want) {
			t.Errorf("SeedFileProblem missed %q: %s", want, problem)
		}
	}
}

// checkI18nFiles decodes services/_i18n strictly: en.yml maps each key to its
// English, and every other file maps each key to {text, source}. A field the
// loader does not read would drop that translation at load without a word.
func checkI18nFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var targets int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yml") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		decoder := yaml.NewDecoder(bytes.NewReader(raw))
		decoder.KnownFields(true)
		if e.Name() == "en.yml" {
			var english map[string]string
			err = decoder.Decode(&english)
		} else {
			var target map[string]translation
			err = decoder.Decode(&target)
			targets++
		}
		if err != nil {
			t.Errorf("_i18n/%s does not have the shape the loader reads: %v", e.Name(), err)
		}
	}
	if targets == 0 {
		t.Errorf("%s holds no translation files", dir)
	}
}
