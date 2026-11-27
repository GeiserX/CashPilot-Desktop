// Package i18n translates the text CashPilot Desktop shows. The frontend and this
// package read the same files: one flat JSON object per locale in
// frontend/src/locales/<code>.json, key to message. Package main embeds them and
// calls Register at start.
//
// Messages use a small part of ICU MessageFormat: {name} arguments, and
// {name, plural, ...} and {name, select, ...} blocks, with # for the number in a
// plural case. frontend/src/render/i18n.ts implements the same rules, and both
// run the vectors in testdata/vectors.json.
//
// A key missing in a locale shows the English message, never a blank.
package i18n

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
)

// Default is the source locale and the fallback for every missing key.
const Default = "en"

// Locales are the locales we ship, in the order the language picker lists them.
// Every one has a file frontend/src/locales/<code>.json; a test checks both ways.
var Locales = []string{
	"en", "es", "pt-BR", "fr", "de", "it", "nl", "pl", "ru", "uk",
	"tr", "ar", "hi", "id", "vi", "th", "fil", "zh-Hans", "ja", "ko",
}

// NameKey is the key whose value, in each file, is that language's own name.
const NameKey = "language.name"

var (
	mu       sync.RWMutex
	messages map[string]map[string]string // locale -> key -> message
	current  atomic.Value                 // string
	loadOnce sync.Once
)

func init() { current.Store(Default) }

// Register loads every <code>.json under dir in fsys, and the source hashes
// under hashesDir (empty for none). Package main calls it with the embedded
// files before anything is translated.
func Register(fsys fs.FS, dir, hashesDir string) error {
	read := func(name string) ([]byte, error) { return fs.ReadFile(fsys, name) }
	loaded, err := load(read, path.Join, dir, hashesDir)
	if err != nil {
		return err
	}
	mu.Lock()
	messages = loaded
	mu.Unlock()
	loadOnce.Do(func() {}) // a later lookup must not replace what was registered
	return nil
}

// ensureLoaded covers a package test, where no main registered the files: it
// reads them from the source tree, found from this file's own path.
func ensureLoaded() {
	loadOnce.Do(func() {
		mu.RLock()
		have := messages != nil
		mu.RUnlock()
		if have {
			return
		}
		_, file, _, ok := goruntime.Caller(0)
		if !ok {
			return
		}
		root := filepath.Join(filepath.Dir(file), "..", "..")
		loaded, err := load(os.ReadFile, filepath.Join,
			filepath.Join(root, "frontend", "src", "locales"), filepath.Join(root, "i18n", "source-hashes"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "i18n: %v\n", err)
			return
		}
		mu.Lock()
		messages = loaded
		mu.Unlock()
	})
}

// load reads every locale file. A translation whose stored source hash is not
// the hash of today's English is stale: it is dropped, so the English shows
// until someone translates the new text. A translation with no hash is kept.
func load(read func(string) ([]byte, error), join func(...string) string, dir, hashesDir string) (map[string]map[string]string, error) {
	loaded := map[string]map[string]string{}
	for _, code := range Locales {
		raw, err := read(join(dir, code+".json"))
		if err != nil {
			if code == Default {
				return nil, fmt.Errorf("i18n: %w", err)
			}
			continue
		}
		var m map[string]string
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("i18n: %s.json: %w", code, err)
		}
		loaded[code] = m
	}
	en := loaded[Default]
	for code, m := range loaded {
		if code == Default || hashesDir == "" {
			continue
		}
		raw, err := read(join(hashesDir, code+".json"))
		if err != nil {
			continue
		}
		var hashes map[string]string
		if err := json.Unmarshal(raw, &hashes); err != nil {
			return nil, fmt.Errorf("i18n: source hashes %s.json: %w", code, err)
		}
		for key, h := range hashes {
			if _, ok := m[key]; ok && h != SourceHash(en[key]) {
				delete(m, key)
			}
		}
	}
	return loaded, nil
}

// SourceHash is the hash a translation stores of the English it was made from:
// the first 12 hex digits of its SHA-256, as scripts/i18n_check.py writes it.
func SourceHash(english string) string {
	sum := sha256.Sum256([]byte(english))
	return hex.EncodeToString(sum[:])[:12]
}

// SetLocale makes code the locale T uses. An unsupported code means English.
func SetLocale(code string) {
	if !supported(code) {
		code = Default
	}
	current.Store(code)
}

// Current is the locale T uses now.
func Current() string { return current.Load().(string) }

func supported(code string) bool {
	for _, c := range Locales {
		if c == code {
			return true
		}
	}
	return false
}

// T translates key into the current locale. args are name, value pairs:
// T("fleet.devices", "count", 3).
func T(key string, args ...any) string { return TL(Current(), key, args...) }

// TL translates key into locale.
func TL(locale, key string, args ...any) string {
	ensureLoaded()
	mu.RLock()
	msg, ok := messages[locale][key]
	if !ok || msg == "" {
		msg, ok = messages[Default][key]
	}
	mu.RUnlock()
	if !ok {
		// A key that is in no file is a bug the parity check catches. Showing
		// the key keeps the screen readable enough to report it.
		return key
	}
	return Format(locale, msg, pairs(args))
}

// Has reports whether English has key.
func Has(key string) bool {
	ensureLoaded()
	mu.RLock()
	defer mu.RUnlock()
	_, ok := messages[Default][key]
	return ok
}

// Messages returns a copy of one locale's messages, for the frontend.
func Messages(locale string) map[string]string {
	ensureLoaded()
	mu.RLock()
	defer mu.RUnlock()
	out := make(map[string]string, len(messages[locale]))
	for k, v := range messages[locale] {
		out[k] = v
	}
	return out
}

// Language is one entry of the language picker.
type Language struct {
	Code string `json:"code"`
	Name string `json:"name"` // in its own language: "Español", "Deutsch"
}

// Languages lists every locale that has a file, each in its own name.
func Languages() []Language {
	ensureLoaded()
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Language, 0, len(Locales))
	for _, code := range Locales {
		m, ok := messages[code]
		if !ok {
			continue
		}
		name := m[NameKey]
		if name == "" {
			name = code
		}
		out = append(out, Language{Code: code, Name: name})
	}
	return out
}

func pairs(args []any) map[string]any {
	if len(args) == 0 {
		return nil
	}
	out := make(map[string]any, len(args)/2)
	for i := 0; i+1 < len(args); i += 2 {
		name, _ := args[i].(string)
		out[name] = args[i+1]
	}
	return out
}

// Match returns the first supported locale for a list of BCP 47 tags, in the
// user's order, or "" when none matches. A tag matches by language first, then
// by region: es-MX gets es, pt-PT gets pt-BR, zh-TW gets zh-Hans. POSIX forms
// (es_ES.UTF-8@euro) are read too.
func Match(tags ...string) string {
	for _, tag := range tags {
		if code := matchOne(tag); code != "" {
			return code
		}
	}
	return ""
}

func matchOne(tag string) string {
	tag = strings.TrimSpace(tag)
	if i := strings.IndexAny(tag, ".@"); i >= 0 {
		tag = tag[:i]
	}
	tag = strings.ReplaceAll(tag, "_", "-")
	if tag == "" || strings.EqualFold(tag, "C") || strings.EqualFold(tag, "POSIX") {
		return ""
	}
	for _, code := range Locales {
		if strings.EqualFold(code, tag) {
			return code
		}
	}
	base := strings.ToLower(strings.SplitN(tag, "-", 2)[0])
	if alias, ok := languageAliases[base]; ok {
		base = alias
	}
	for _, code := range Locales {
		if strings.ToLower(strings.SplitN(code, "-", 2)[0]) == base {
			return code
		}
	}
	return ""
}

// languageAliases maps old or sibling language codes to the one we ship.
var languageAliases = map[string]string{
	"tl": "fil", // Tagalog: Filipino is its standard form
	"in": "id",  // the old code for Indonesian
}
