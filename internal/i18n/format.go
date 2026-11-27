package i18n

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"golang.org/x/text/feature/plural"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"golang.org/x/text/number"
)

// Format fills one message. It reads {name}, {name, plural, ...} and
// {name, select, ...}; # in a plural case is the number. An apostrophe before
// { or } quotes it, and two apostrophes make one, as in ICU. A message that does not
// parse is returned as written, so a broken translation still shows its words.
func Format(locale, msg string, args map[string]any) string {
	if !strings.ContainsAny(msg, "{}'") {
		return msg
	}
	p := parser{src: msg}
	nodes, ok := p.parse(false)
	if !ok || p.pos != len(p.src) {
		return msg
	}
	var b strings.Builder
	render(&b, locale, nodes, args, nil)
	return b.String()
}

type node struct {
	text  string
	arg   string
	kind  string // "", "plural", "select"
	cases map[string][]node
	order []string
	pound bool // a # inside a plural case
}

type parser struct {
	src string
	pos int
}

// parse reads nodes up to the end, or up to a } closing a case when inCase.
func (p *parser) parse(inCase bool) ([]node, bool) {
	var nodes []node
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			nodes = append(nodes, node{text: text.String()})
			text.Reset()
		}
	}
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == '\'':
			if p.pos+1 < len(p.src) && p.src[p.pos+1] == '\'' {
				text.WriteByte('\'')
				p.pos += 2
				continue
			}
			if p.pos+1 < len(p.src) && strings.ContainsRune("{}#", rune(p.src[p.pos+1])) {
				// A quoted span runs to the next lone apostrophe; two apostrophes
				// inside it are one, as everywhere else.
				p.pos++
				for p.pos < len(p.src) {
					if p.src[p.pos] == '\'' {
						if p.pos+1 < len(p.src) && p.src[p.pos+1] == '\'' {
							text.WriteByte('\'')
							p.pos += 2
							continue
						}
						p.pos++
						break
					}
					text.WriteByte(p.src[p.pos])
					p.pos++
				}
				continue
			}
			text.WriteByte(c)
			p.pos++
		case c == '#' && inCase:
			flush()
			nodes = append(nodes, node{pound: true})
			p.pos++
		case c == '{':
			flush()
			n, ok := p.argument()
			if !ok {
				return nil, false
			}
			nodes = append(nodes, n)
		case c == '}':
			if inCase {
				flush()
				return nodes, true
			}
			return nil, false
		default:
			text.WriteByte(c)
			p.pos++
		}
	}
	if inCase {
		return nil, false
	}
	flush()
	return nodes, true
}

func (p *parser) argument() (node, bool) {
	p.pos++ // {
	end := strings.IndexAny(p.src[p.pos:], ",}")
	if end < 0 {
		return node{}, false
	}
	name := strings.TrimSpace(p.src[p.pos : p.pos+end])
	p.pos += end
	if name == "" {
		return node{}, false
	}
	if p.src[p.pos] == '}' {
		p.pos++
		return node{arg: name}, true
	}
	p.pos++ // ,
	end = strings.IndexAny(p.src[p.pos:], ",}")
	if end < 0 {
		return node{}, false
	}
	kind := strings.TrimSpace(p.src[p.pos : p.pos+end])
	p.pos += end
	if kind != "plural" && kind != "select" || p.src[p.pos] != ',' {
		return node{}, false
	}
	p.pos++
	n := node{arg: name, kind: kind, cases: map[string][]node{}}
	for {
		p.skipSpace()
		if p.pos >= len(p.src) {
			return node{}, false
		}
		if p.src[p.pos] == '}' {
			p.pos++
			break
		}
		start := p.pos
		for p.pos < len(p.src) && p.src[p.pos] != '{' && p.src[p.pos] != ' ' && p.src[p.pos] != '\n' && p.src[p.pos] != '\t' {
			p.pos++
		}
		sel := p.src[start:p.pos]
		p.skipSpace()
		if sel == "" || p.pos >= len(p.src) || p.src[p.pos] != '{' {
			return node{}, false
		}
		p.pos++
		body, ok := p.parse(true)
		if !ok {
			return node{}, false
		}
		p.pos++ // the } that closed the case
		n.cases[sel] = body
		n.order = append(n.order, sel)
	}
	if _, ok := n.cases["other"]; !ok {
		return node{}, false
	}
	return n, true
}

func (p *parser) skipSpace() {
	for p.pos < len(p.src) && strings.ContainsRune(" \n\t\r", rune(p.src[p.pos])) {
		p.pos++
	}
}

// number is the plural's value, already formatted, for # in its cases.
func render(b *strings.Builder, locale string, nodes []node, args map[string]any, number *string) {
	for _, n := range nodes {
		switch {
		case n.pound:
			if number != nil {
				b.WriteString(*number)
			} else {
				b.WriteByte('#')
			}
		case n.arg == "":
			b.WriteString(n.text)
		case n.kind == "":
			v, ok := args[n.arg]
			if !ok {
				b.WriteString("{" + n.arg + "}")
				continue
			}
			b.WriteString(fmt.Sprint(v))
		case n.kind == "select":
			v := fmt.Sprint(args[n.arg])
			body, ok := n.cases[v]
			if !ok {
				body = n.cases["other"]
			}
			render(b, locale, body, args, number)
		case n.kind == "plural":
			// A fraction is not a count: it takes the other case and shows as
			// written, in Go and in the frontend alike.
			if x, ok := args[n.arg].(float64); ok && x != math.Trunc(x) {
				shown := formatNumber(locale, x)
				render(b, locale, n.cases["other"], args, &shown)
				continue
			}
			count, ok := toInt(args[n.arg])
			if !ok {
				render(b, locale, n.cases["other"], args, nil)
				continue
			}
			body, ok := n.cases["="+strconv.FormatInt(count, 10)]
			if !ok {
				body, ok = n.cases[PluralCategory(locale, count)]
			}
			if !ok {
				body = n.cases["other"]
			}
			shown := formatNumber(locale, count)
			render(b, locale, body, args, &shown)
		}
	}
}

func toInt(v any) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case int32:
		return int64(x), true
	case int64:
		return x, true
	case uint:
		return int64(x), true
	case uint32:
		return int64(x), true
	case uint64:
		return int64(x), true
	case float64:
		return int64(x), true
	}
	return 0, false
}

// formatNumber writes n with the locale's grouping and Latin digits, as the
// frontend's Intl.NumberFormat does: current CLDR writes Latin digits for ar,
// and the copy in x/text predates that.
func formatNumber[N int64 | float64](locale string, n N) string {
	tag, err := language.Parse(locale + "-u-nu-latn")
	if err != nil {
		return fmt.Sprint(n)
	}
	return message.NewPrinter(tag).Sprint(number.Decimal(n))
}

// PluralCategory is the CLDR cardinal category of a whole number in locale:
// zero, one, two, few, many or other.
func PluralCategory(locale string, n int64) string {
	tag, err := language.Parse(locale)
	if err != nil {
		return "other"
	}
	if n < 0 {
		n = -n
	}
	i := int(n)
	switch plural.Cardinal.MatchPlural(tag, i, 0, 0, 0, 0) {
	case plural.Zero:
		return "zero"
	case plural.One:
		return "one"
	case plural.Two:
		return "two"
	case plural.Few:
		return "few"
	case plural.Many:
		return "many"
	}
	return "other"
}
