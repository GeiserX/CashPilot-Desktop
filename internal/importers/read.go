package importers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The limits a pasted config is held to. Real files from these tools are a few KiB.
const (
	MaxFiles      = 4
	MaxTotalBytes = 64 * 1024
	MaxLines      = 5000
	MaxLineBytes  = 8 * 1024
	MaxValueBytes = 4 * 1024
	MaxJSONKeys   = 1000
	MaxJSONDepth  = 20
	MaxNameLen    = 200
)

var (
	keyRE         = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	placeholderRE = regexp.MustCompile(`<[^<>]*>|your[A-Z]{3,}`)
	// One line of InternetIncome's earnapp.txt: "<date> <time> https://earnapp.com/r/sdk-node-<id>".
	earnappLineRE = regexp.MustCompile(`https://earnapp\.com/r/(\S+)`)
	hex64RE       = regexp.MustCompile(`^[0-9A-Fa-f]{64}$`)
	// The scheme of one proxies.txt line ("socks5://user:pass@host:port").
	proxySchemeRE = regexp.MustCompile(`^[a-z0-9]+$`)
)

// shellSpecial is what a shell would do something with in an unquoted word.
// InternetIncome runs every value through `eval "echo $value"`, so an unquoted value
// holding any of these is not the value its tool actually used.
const shellSpecial = "$`\\\"'*?[]{}()<>|&;~!# \t"

// Refused is a file the importer will not read. Its message never quotes the file:
// it may be a credential.
type Refused struct{ msg string }

func (r *Refused) Error() string { return r.msg }

func refuse(format string, args ...any) error { return &Refused{msg: fmt.Sprintf(format, args...)} }

// File is one uploaded or pasted file as the frontend sends it.
type File struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// Value is one setting read from a file. Retype means its text cannot be trusted.
type Value struct {
	Text   string
	Retype bool
}

type envMap struct {
	keys   []string
	values map[string]Value
}

func newEnv() *envMap { return &envMap{values: map[string]Value{}} }

func (e *envMap) set(key string, v Value) {
	if _, ok := e.values[key]; !ok {
		e.keys = append(e.keys, key)
	}
	e.values[key] = v
}

func (e *envMap) get(key string) (Value, bool) {
	v, ok := e.values[key]
	return v, ok
}

// jsonObject is a JSON object with its keys in file order.
type jsonObject struct {
	keys   []string
	values map[string]any
}

func (o *jsonObject) get(key string) any { return o.values[key] }

// fileSettings is what one file holds.
type fileSettings struct {
	name       string
	kind       string // json | env | devices (earnapp.txt, proxyrack.txt) | proxies
	env        *envMap
	json       *jsonObject
	extraLines int // state files: entries past the first
}

// splitLines splits like Python's str.splitlines, so a file reads the same here as
// in the web app: every line boundary it knows, and no empty line after a final one.
func splitLines(text string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		switch r {
		case '\n', '\v', '\f', '\x1c', '\x1d', '\x1e', '\u0085', '\u2028', '\u2029':
			lines = append(lines, text[start:i])
			i += size
			start = i
		case '\r':
			lines = append(lines, text[start:i])
			i += size
			if i < len(text) && text[i] == '\n' {
				i++
			}
			start = i
		default:
			i += size
		}
	}
	if start < len(text) {
		lines = append(lines, text[start:])
	}
	return lines
}

func checkText(name, content string) (string, error) {
	if strings.ContainsRune(content, 0) {
		return "", refuse("%s is not a text file.", name)
	}
	if !utf8.ValidString(content) {
		return "", refuse("%s is not a UTF-8 text file.", name)
	}
	content = strings.TrimLeft(content, "\ufeff")
	lines := splitLines(content)
	if len(lines) > MaxLines {
		return "", refuse("%s has more than %d lines; this is not a config file.", name, MaxLines)
	}
	for _, line := range lines {
		if len(line) > MaxLineBytes {
			return "", refuse("%s has a line longer than %d KiB.", name, MaxLineBytes/1024)
		}
	}
	return content, nil
}

// stripInlineComment cuts a ` # comment` after an unquoted value; a `#` inside a word stays.
func stripInlineComment(raw string) string {
	prev, prevSpace := 0, false
	for i, r := range raw {
		if r == '#' && prevSpace {
			return raw[:prev]
		}
		prev, prevSpace = i, unicode.IsSpace(r)
	}
	return raw
}

// readEnvValue reads one value as the tool that reads it would see it, or marks it
// for retyping.
//
// shell is InternetIncome's reading (`eval "echo $value"`); otherwise Docker
// Compose's .env reading. Single quotes are literal in both. Anything a shell or
// Compose would expand is never imported, because its real value is whatever that
// expansion produced, and finding out would mean running it.
func readEnvValue(raw string, shell bool) Value {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Value{}
	}
	retype := Value{Retype: true}
	switch raw[0] {
	case '\'':
		end := strings.IndexByte(raw[1:], '\'')
		if end == -1 {
			return retype
		}
		end++
		if strings.TrimSpace(stripInlineComment(raw[end+1:])) != "" {
			// 'it'\''s'-style concatenation: a shell's reading.
			return retype
		}
		return Value{Text: raw[1:end]}
	case '"':
		end := 1
		for end < len(raw) {
			if raw[end] == '\\' {
				end += 2
				continue
			}
			if raw[end] == '"' {
				break
			}
			end++
		}
		if end >= len(raw) || strings.TrimSpace(stripInlineComment(raw[end+1:])) != "" {
			return retype
		}
		inner := raw[1:end]
		// Escapes mean different things to a shell and to Compose; $ and ` expand in both.
		if strings.ContainsAny(inner, "$`\\") {
			return retype
		}
		return Value{Text: inner}
	}
	value := strings.TrimSpace(stripInlineComment(raw))
	special := "$`"
	if shell {
		special = shellSpecial
	}
	if strings.ContainsAny(value, special) {
		return retype
	}
	return Value{Text: value}
}

func parseEnv(text string, shell bool) *envMap {
	env := newEnv()
	for _, line := range splitLines(text) {
		stripped := strings.TrimSpace(line)
		if stripped == "" || strings.HasPrefix(stripped, "#") {
			continue
		}
		if strings.HasPrefix(stripped, "export ") {
			stripped = strings.TrimLeftFunc(stripped[len("export "):], unicode.IsSpace)
		}
		key, raw, found := strings.Cut(stripped, "=")
		key = strings.TrimSpace(key)
		if !found || !keyRE.MatchString(key) {
			continue
		}
		value := readEnvValue(raw, shell)
		if !value.Retype && len(value.Text) > MaxValueBytes {
			value = Value{Retype: true}
		}
		env.set(key, value)
	}
	return env
}

// jsonReader decodes a JSON document with object keys in order, counting keys and
// depth as it goes so a hostile document is refused before it is built.
type jsonReader struct {
	dec  *json.Decoder
	name string
	keys int
}

var errNotJSON = errors.New("not json")

func (r *jsonReader) value(depth int) (any, error) {
	if depth > MaxJSONDepth {
		return nil, refuse("%s is nested too deeply; this is not a config file.", r.name)
	}
	tok, err := r.dec.Token()
	if err != nil {
		return nil, errNotJSON
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := &jsonObject{values: map[string]any{}}
			for r.dec.More() {
				keyTok, err := r.dec.Token()
				if err != nil {
					return nil, errNotJSON
				}
				key, ok := keyTok.(string)
				if !ok {
					return nil, errNotJSON
				}
				r.keys++
				if r.keys > MaxJSONKeys {
					return nil, refuse("%s has more than %d keys; this is not a config file.", r.name, MaxJSONKeys)
				}
				v, err := r.value(depth + 1)
				if err != nil {
					return nil, err
				}
				if _, seen := obj.values[key]; !seen {
					obj.keys = append(obj.keys, key)
				}
				obj.values[key] = v
			}
			if _, err := r.dec.Token(); err != nil {
				return nil, errNotJSON
			}
			return obj, nil
		case '[':
			var list []any
			for r.dec.More() {
				v, err := r.value(depth + 1)
				if err != nil {
					return nil, err
				}
				list = append(list, v)
			}
			if _, err := r.dec.Token(); err != nil {
				return nil, errNotJSON
			}
			return list, nil
		}
		return nil, errNotJSON
	default:
		return t, nil
	}
}

func parseJSON(name, text string) (*jsonObject, error) {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	r := &jsonReader{dec: dec, name: name}
	v, err := r.value(0)
	if err == nil {
		if _, tail := dec.Token(); tail != io.EOF {
			err = errNotJSON
		}
	}
	if err != nil {
		var refused *Refused
		if errors.As(err, &refused) {
			return nil, err
		}
		return nil, refuse("%s is not valid JSON.", name)
	}
	obj, ok := v.(*jsonObject)
	if !ok {
		return nil, refuse("%s is not a JSON object.", name)
	}
	return obj, nil
}

func isProxyLine(line string) bool {
	scheme, rest, found := strings.Cut(line, "://")
	return found && rest != "" && proxySchemeRE.MatchString(scheme) && !strings.ContainsFunc(rest, unicode.IsSpace)
}

// stateFile reads InternetIncome's earnapp.txt or proxyrack.txt, or a proxies list;
// nil otherwise.
//
// Only the first entry is taken: it is the device the main container used, the one
// the user linked to their account. The rest belong to proxy copies.
func stateFile(name, text string) *fileSettings {
	var lines []string
	for _, ln := range splitLines(text) {
		ln = strings.TrimSpace(ln)
		if ln != "" && !strings.HasPrefix(ln, "#") {
			lines = append(lines, ln)
		}
	}
	if len(lines) == 0 {
		return nil
	}
	all := func(ok func(string) bool) bool {
		for _, ln := range lines {
			if !ok(ln) {
				return false
			}
		}
		return true
	}
	if all(earnappLineRE.MatchString) {
		env := newEnv()
		env.set("earnapp.txt", Value{Text: earnappLineRE.FindStringSubmatch(lines[0])[1]})
		return &fileSettings{name: name, kind: "devices", env: env, extraLines: len(lines) - 1}
	}
	if all(hex64RE.MatchString) {
		env := newEnv()
		env.set("proxyrack.txt", Value{Text: lines[0]})
		return &fileSettings{name: name, kind: "devices", env: env, extraLines: len(lines) - 1}
	}
	if all(isProxyLine) {
		return &fileSettings{name: name, kind: "proxies", env: newEnv(), extraLines: len(lines)}
	}
	return nil
}

func cleanName(name string, i int) string {
	fallback := fmt.Sprintf("file %d", i)
	if strings.TrimSpace(name) == "" {
		return fallback
	}
	var b strings.Builder
	n := 0
	for _, r := range name {
		if !unicode.IsPrint(r) {
			continue
		}
		if n == MaxNameLen {
			break
		}
		b.WriteRune(r)
		n++
	}
	if b.Len() == 0 {
		return fallback
	}
	return b.String()
}

// ReadFiles checks and reads the uploaded files. source is "auto" or one of Sources.
func ReadFiles(files []File, source string) ([]*fileSettings, error) {
	if len(files) == 0 {
		return nil, refuse("Add at least one file.")
	}
	if len(files) > MaxFiles {
		return nil, refuse("At most %d files at a time.", MaxFiles)
	}
	total := 0
	type checked struct{ name, text string }
	var ok []checked
	for i, f := range files {
		name := cleanName(f.Name, i+1)
		total += len(f.Content)
		if total > MaxTotalBytes {
			return nil, refuse("The files add up to more than %d KiB.", MaxTotalBytes/1024)
		}
		text, err := checkText(name, f.Content)
		if err != nil {
			return nil, err
		}
		ok = append(ok, checked{name, text})
	}

	shell := source == "internetincome"
	var result []*fileSettings
	type envText struct {
		fs   *fileSettings
		text string
	}
	var envTexts []envText
	for _, c := range ok {
		if strings.HasPrefix(strings.TrimLeftFunc(c.text, unicode.IsSpace), "{") {
			obj, err := parseJSON(c.name, c.text)
			if err != nil {
				return nil, err
			}
			result = append(result, &fileSettings{name: c.name, kind: "json", json: obj, env: newEnv()})
			continue
		}
		if state := stateFile(c.name, c.text); state != nil {
			result = append(result, state)
			continue
		}
		fs := &fileSettings{name: c.name, kind: "env", env: parseEnv(c.text, shell)}
		result = append(result, fs)
		envTexts = append(envTexts, envText{fs, c.text})
	}
	if !shell && source == "auto" {
		for _, fs := range result {
			if fileTool(fs) == "internetincome" {
				// Any file naming InternetIncome (its properties.conf, earnapp.txt or
				// proxyrack.txt) means every env file went through its shell: read them
				// all that way, not just the one carrying the marker.
				for _, et := range envTexts {
					et.fs.env = parseEnv(et.text, true)
				}
				break
			}
		}
	}
	return result, nil
}

func guessToolEnv(env *envMap) string {
	hits := map[string]bool{}
	for _, tool := range sources.Tools.Keys {
		spec := sources.Tools.Values[tool]
		for _, k := range spec.Markers.Keys {
			if _, ok := env.get(k); ok {
				hits[tool] = true
			}
		}
		for key, want := range spec.Markers.Values {
			if got, ok := env.get(key); ok && strings.EqualFold(strings.TrimSpace(got.Text), want) {
				hits[tool] = true
			}
		}
	}
	switch len(hits) {
	case 0:
		return ""
	case 1:
		for tool := range hits {
			return tool
		}
	}
	return "ambiguous"
}

func fileTool(fs *fileSettings) string {
	switch fs.kind {
	case "json":
		if _, ok := fs.json.get("apps").(*jsonObject); ok {
			return "money4band"
		}
		return ""
	case "devices":
		return "internetincome"
	case "proxies":
		return ""
	}
	return guessToolEnv(fs.env)
}

// DetectSource names the tool that wrote files. It never guesses between two.
func DetectSource(files []*fileSettings, source string) (string, error) {
	seen := map[string]bool{}
	for _, fs := range files {
		if t := fileTool(fs); t != "" {
			seen[t] = true
		}
	}
	if seen["ambiguous"] {
		return "", refuse("These files could come from more than one tool. Pick the tool you used.")
	}
	if isSource(source) {
		var others []string
		for t := range seen {
			if t != source {
				others = append(others, t)
			}
		}
		sort.Strings(others)
		if len(others) > 0 {
			return "", refuse("These files look like %s's, not %s's. Check the tool.",
				sources.Tools.Values[others[0]].Name, sources.Tools.Values[source].Name)
		}
		return source, nil
	}
	if len(seen) > 1 {
		return "", refuse("These files come from different tools. Import one tool at a time.")
	}
	for t := range seen {
		return t, nil
	}
	return "", refuse("Could not tell which tool wrote these files. Pick the tool you used.")
}
