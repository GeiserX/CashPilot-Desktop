package i18n

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
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
