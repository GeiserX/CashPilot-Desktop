package i18n

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// callKey finds a literal key in a call: t("x"), ht("x"), i18n.T("x"),
// i18n.TL(locale, "x"). Keys are literal by rule, so this sees every one.
var callKey = regexp.MustCompile(`(?:\bh?t|\bi18n\.T|\bi18n\.TL\([^,()]+,)\(\s*"([a-z][A-Za-z0-9_]*(?:\.[A-Za-z0-9_]+)+)"`)

// sources walks the code that can use a key: the frontend and the Go tree.
func sources(t *testing.T, visit func(path, text string)) {
	t.Helper()
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			switch name {
			case "node_modules", ".git", ".harness-build", "dist", "wailsjs", "services", "e2e":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(name, "_test.go") || !(strings.HasSuffix(name, ".go") || strings.HasSuffix(name, ".ts")) {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		visit(p, string(raw))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func english(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "frontend", "src", "locales", "en.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// Every key the code asks for is in en.json, and every key in en.json is used.
func TestEveryKeyIsDefinedAndUsed(t *testing.T) {
	en := english(t)
	used := map[string]bool{}
	var missing []string
	all := ""
	sources(t, func(path, text string) {
		all += text
		for _, m := range callKey.FindAllStringSubmatch(text, -1) {
			used[m[1]] = true
			if _, ok := en[m[1]]; !ok {
				missing = append(missing, path+": "+m[1])
			}
		}
	})
	if len(used) == 0 {
		t.Fatal("found no key in the sources; the scan is broken")
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("keys the code uses that en.json lacks:\n  %s", strings.Join(missing, "\n  "))
	}
	var unused []string
	for key := range en {
		// The installer's keys are read by cmd/i18nnsis, not by a call.
		if used[key] || strings.HasPrefix(key, "installer.") || strings.Contains(all, `"`+key+`"`) {
			continue
		}
		unused = append(unused, key)
	}
	sort.Strings(unused)
	if len(unused) > 0 {
		t.Errorf("keys in en.json that no code uses:\n  %s", strings.Join(unused, "\n  "))
	}
}
