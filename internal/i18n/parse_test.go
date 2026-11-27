package i18n

import (
	"strings"
	"testing"
)

// Every message in every locale must parse. Format shows a message that does
// not parse as written, braces and all, so a broken translation would reach the
// screen silently; this is where it fails instead.
func TestEveryMessageParses(t *testing.T) {
	ensureLoaded()
	mu.RLock()
	defer mu.RUnlock()
	if len(messages) < 2 {
		t.Fatal("the locale files did not load")
	}
	for locale, m := range messages {
		for key, msg := range m {
			if !strings.ContainsAny(msg, "{}'") {
				continue
			}
			p := parser{src: msg}
			nodes, ok := p.parse(false)
			if !ok || p.pos != len(p.src) {
				t.Errorf("%s %s: does not parse: %q", locale, key, msg)
				continue
			}
			checkPlurals(t, locale, key, nodes)
		}
	}
}

// checkPlurals: a plural case named by a CLDR category the locale never uses is
// dead text, a sign the translation copied English's one/other.
func checkPlurals(t *testing.T, locale, key string, nodes []node) {
	for _, n := range nodes {
		if n.kind == "plural" && locale != Default {
			for _, sel := range n.order {
				if strings.HasPrefix(sel, "=") || sel == "other" {
					continue
				}
				if !usesCategory(locale, sel) {
					t.Errorf("%s %s: plural case %q is never chosen in %s", locale, key, sel, locale)
				}
			}
		}
		for _, c := range n.cases {
			checkPlurals(t, locale, key, c)
		}
	}
}

func usesCategory(locale, category string) bool {
	for _, n := range append(seq(0, 200), 1000, 1000000) {
		if PluralCategory(locale, n) == category {
			return true
		}
	}
	return false
}

func seq(from, to int64) []int64 {
	out := make([]int64, 0, to-from+1)
	for n := from; n <= to; n++ {
		out = append(out, n)
	}
	return out
}
