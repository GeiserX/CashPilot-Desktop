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
			if body, ok := n.cases["one"]; ok && !showsCount(body, n.arg) {
				if other, found := oneBeyondOne(locale); found {
					t.Errorf("%s %s: plural case \"one\" never shows the count, but %s also picks it for %d; name exactly one with =1", locale, key, locale, other)
				}
			}
		}
		for _, c := range n.cases {
			checkPlurals(t, locale, key, c)
		}
	}
}

// showsCount: the case prints its own number, as # or as {arg}, or a select
// in it does so in every branch. A case that does not says "one" in words, which is wrong for any other count the
// locale's one category covers (0 in French, 21 in Russian, 2 in Filipino).
func showsCount(nodes []node, arg string) bool {
	for _, n := range nodes {
		if n.pound || (n.kind == "" && n.arg == arg) {
			return true
		}
		if n.kind == "select" && len(n.cases) > 0 {
			every := true
			for _, c := range n.cases {
				every = every && showsCount(c, arg)
			}
			if every {
				return true
			}
		}
	}
	return false
}

// oneBeyondOne finds a count other than 1 that the locale's one category
// covers. found is false when one means exactly 1.
func oneBeyondOne(locale string) (count int64, found bool) {
	for _, n := range seq(0, 200) {
		if n != 1 && PluralCategory(locale, n) == "one" {
			return n, true
		}
	}
	return 0, false
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

// A select inside a one case shows the count only when all its branches do.

func TestShowsCountNeedsEverySelectBranch(t *testing.T) {
	parse := func(s string) []node { p := parser{src: s}; n, _ := p.parse(false); return n[0].cases["one"] }
	if showsCount(parse("{n, plural, one {{m, select, a {# x} other {one x}}} other {# x}}"), "n") {
		t.Error("a select with one branch that names no count passed")
	}
	if !showsCount(parse("{n, plural, one {{m, select, a {# x} other {{n} x}}} other {# x}}"), "n") {
		t.Error("a select that shows the count in every branch failed")
	}
	if showsCount(parse("{n, plural, one {One x} other {# x}}"), "n") {
		t.Error("a case that names no count passed")
	}
}
