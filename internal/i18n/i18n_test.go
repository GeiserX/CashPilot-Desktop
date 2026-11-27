package i18n

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

type vectors struct {
	Format []struct {
		Locale string         `json:"locale"`
		Msg    string         `json:"msg"`
		Args   map[string]any `json:"args"`
		Want   string         `json:"want"`
	} `json:"format"`
	Match []struct {
		Tags []string `json:"tags"`
		Want string   `json:"want"`
	} `json:"match"`
}

// The frontend runs the same file (frontend/scripts/i18n_runtime_check.mjs), so the two
// implementations cannot drift apart.
func loadVectors(t *testing.T) vectors {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Format) == 0 || len(v.Match) == 0 {
		t.Fatal("no vectors")
	}
	return v
}

func TestFormatVectors(t *testing.T) {
	for _, c := range loadVectors(t).Format {
		// JSON numbers arrive as float64, as a Go caller's int would be read.
		if got := Format(c.Locale, c.Msg, c.Args); got != c.Want {
			t.Errorf("Format(%s, %q, %v) = %q, want %q", c.Locale, c.Msg, c.Args, got, c.Want)
		}
	}
}

func TestMatchVectors(t *testing.T) {
	for _, c := range loadVectors(t).Match {
		if got := Match(c.Tags...); got != c.Want {
			t.Errorf("Match(%q) = %q, want %q", c.Tags, got, c.Want)
		}
	}
}

func TestEveryLocaleHasAFileAndEveryFileALocale(t *testing.T) {
	dir := filepath.Join("..", "..", "frontend", "src", "locales")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			files = append(files, e.Name()[:len(e.Name())-len(".json")])
		}
	}
	want := append([]string(nil), Locales...)
	sort.Strings(files)
	sort.Strings(want)
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("locale files %v, Locales %v", files, want)
	}
	if len(Languages()) != len(Locales) {
		t.Fatalf("Languages() has %d entries, want %d", len(Languages()), len(Locales))
	}
}

func TestMissingKeyFallsBackToEnglish(t *testing.T) {
	ensureLoaded()
	mu.Lock()
	saved := messages
	messages = map[string]map[string]string{
		"en": {"a": "Hello {name}", "b": "Only English"},
		"es": {"a": "Hola {name}", "b": ""},
	}
	mu.Unlock()
	t.Cleanup(func() { mu.Lock(); messages = saved; mu.Unlock() })

	if got := TL("es", "a", "name", "Ada"); got != "Hola Ada" {
		t.Errorf("es a = %q", got)
	}
	if got := TL("es", "b"); got != "Only English" {
		t.Errorf("an empty translation must show English, got %q", got)
	}
	if got := TL("de", "a", "name", "Ada"); got != "Hello Ada" {
		t.Errorf("a locale with no file must show English, got %q", got)
	}
	if got := TL("es", "missing"); got != "missing" {
		t.Errorf("an unknown key shows the key, got %q", got)
	}
}

func TestSetLocaleRefusesAnUnsupportedCode(t *testing.T) {
	t.Cleanup(func() { SetLocale(Default) })
	SetLocale("es")
	if Current() != "es" {
		t.Fatalf("Current() = %q", Current())
	}
	SetLocale("xx")
	if Current() != Default {
		t.Fatalf("an unsupported code must fall back to English, got %q", Current())
	}
}

func TestEnvLanguagesOrder(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "de_DE.UTF-8")
	t.Setenv("LANGUAGE", "fr:es")
	if got := envLanguages(); !reflect.DeepEqual(got, []string{"fr", "es", "de_DE.UTF-8"}) {
		t.Fatalf("envLanguages() = %v", got)
	}
	t.Setenv("LANG", "C")
	if got := envLanguages(); !reflect.DeepEqual(got, []string{"C"}) {
		t.Fatalf("LANGUAGE must be ignored under the C locale, got %v", got)
	}
}

func TestStaleTranslationShowsEnglish(t *testing.T) {
	files := map[string]string{
		"l/en.json": `{"a": "Save", "b": "Remove"}`,
		"l/es.json": `{"a": "Guardar", "b": "Quitar"}`,
		"h/es.json": `{"a": "` + SourceHash("Save") + `", "b": "` + SourceHash("Delete") + `"}`,
	}
	read := func(name string) ([]byte, error) {
		if v, ok := files[name]; ok {
			return []byte(v), nil
		}
		return nil, os.ErrNotExist
	}
	loaded, err := load(read, path.Join, "l", "h") // the fake files use slashes on every OS
	if err != nil {
		t.Fatal(err)
	}
	if loaded["es"]["a"] != "Guardar" {
		t.Errorf("a fresh translation must stay, got %q", loaded["es"]["a"])
	}
	if _, ok := loaded["es"]["b"]; ok {
		t.Errorf("a translation of older English must be dropped so English shows")
	}
	if SourceHash("Save") != "1509f561f241" {
		t.Errorf("SourceHash must match i18n_check.py, got %s", SourceHash("Save"))
	}
}
