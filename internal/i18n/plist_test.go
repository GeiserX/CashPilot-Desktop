package i18n

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"text/template"
)

// macOS offers a per-app language only for the languages the bundle declares, so
// build/darwin/Info.plist must list exactly the locales we ship.
func TestInfoPlistDeclaresEveryLocale(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "build", "darwin", "Info.plist"))
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)<key>CFBundleLocalizations</key>\s*<array>(.*?)</array>`).FindStringSubmatch(string(raw))
	if block == nil {
		t.Fatal("Info.plist has no CFBundleLocalizations array")
	}
	var got []string
	for _, m := range regexp.MustCompile(`<string>([^<]+)</string>`).FindAllStringSubmatch(block[1], -1) {
		got = append(got, strings.TrimSpace(m[1]))
	}
	if !reflect.DeepEqual(got, Locales) {
		t.Fatalf("CFBundleLocalizations %v, Locales %v", got, Locales)
	}
}

// The release builds with the wails CLI pinned in desktop-release.yml, and
// v2.12.0 parses the plist with text/template and no functions: a call such
// as {{safeBundleID .Name}} from a newer wails template fails the macOS
// build. Parsing it the same way here fails the same way.
func TestInfoPlistCallsNoTemplateFunction(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "build", "darwin", "Info.plist"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := template.New("").Parse(string(raw)); err != nil {
		t.Fatalf("the pinned wails CLI cannot read Info.plist: %v", err)
	}
}
