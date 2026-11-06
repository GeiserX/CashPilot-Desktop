package importers

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// Field states, as the dry run shows them.
const (
	FieldImported    = "imported"
	FieldMissing     = "missing"
	FieldRetype      = "retype"
	FieldPlaceholder = "placeholder"
	FieldInvalid     = "invalid"
)

// Row states.
const (
	RowReady         = "ready"
	RowMissingFields = "missing_fields"
	RowInvalid       = "invalid"
	RowUnusable      = "unusable"
	RowNotDeployable = "not_deployable"
)

// Field is one catalog field of a row. Value is set only for a field that is not
// secret; a secret's value never leaves this package except in Row.Env.
type Field struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Secret   bool   `json:"secret"`
	Required bool   `json:"required"`
	State    string `json:"state"`
	Pattern  string `json:"pattern"`
	Value    string `json:"value,omitempty"`
}

// Row is one CashPilot service the files configure.
type Row struct {
	Slug        string  `json:"slug"`
	Name        string  `json:"name"`
	App         string  `json:"app"`
	Selected    bool    `json:"selected"`
	OffInSource bool    `json:"offInSource"`
	State       string  `json:"state"`
	Reason      string  `json:"reason"`
	Fields      []Field `json:"fields"`
	// Existing is what this computer already has for the service; the App binding
	// fills it in.
	Existing Existing `json:"existing"`
	// Env is what a save would store. It holds credentials and is never sent to the
	// frontend: the binding drops it (json:"-" keeps it out of any JSON by accident).
	Env map[string]string `json:"-"`
}

// Existing says whether the service already has saved credentials or a deployment
// here. A save never replaces a value that is already set (see App.ApplyImport).
type Existing struct {
	Configured bool `json:"configured"`
	Deployed   bool `json:"deployed"`
}

// Skipped is one setting the files hold that is not imported, and why.
type Skipped struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

// Preview is the dry run.
type Preview struct {
	Source      string    `json:"source"`
	SourceName  string    `json:"sourceName"`
	Rows        []Row     `json:"rows"`
	NotImported []Skipped `json:"notImported"`
	Notices     []string  `json:"notices"`
}

// IsPlaceholder reports whether text is blank or an untouched template value.
func IsPlaceholder(text string) bool {
	return strings.TrimSpace(text) == "" || placeholderRE.MatchString(text)
}

func matches(key string, patterns []string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, key); ok {
			return true
		}
	}
	return false
}

func notCarriedReason(key string, spec toolSpec) string {
	for _, pattern := range spec.NotCarried.Keys {
		if ok, _ := path.Match(pattern, key); ok {
			return spec.NotCarried.Values[pattern]
		}
	}
	return ""
}

type foundApp struct {
	name    string
	spec    appSpec
	values  map[string]Value // catalog env key -> value read
	enabled bool
}

// truthy is Python's truth for a JSON value, which the web app's check uses.
func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case fmt.Stringer: // json.Number
		s := t.String()
		return strings.Trim(s, "0.-+eE") != ""
	case []any:
		return len(t) > 0
	case *jsonObject:
		return len(t.keys) > 0
	}
	return true
}

// jsonLeaf is a string or a whole number; anything else is not a credential.
func jsonLeaf(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case fmt.Stringer: // json.Number
		s := t.String()
		if strings.ContainsAny(s, ".eE") {
			return "", false
		}
		return s, true
	}
	return "", false
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func appsFromJSON(files []*fileSettings, spec toolSpec, skipped *[]Skipped) []foundApp {
	var found []foundApp
	for _, fs := range files {
		if fs.kind != "json" {
			continue
		}
		data := fs.json
		for _, top := range data.keys {
			if top == "apps" || contains(spec.IgnoreJSON, top) {
				continue
			}
			node := data.get(top)
			if top == "proxies" || top == "notifications" {
				obj, ok := node.(*jsonObject)
				if !ok || !truthy(obj.get("enabled")) {
					continue
				}
			}
			reason := notCarriedReason(top, spec)
			if reason == "" {
				reason = "CashPilot does not read this setting."
			}
			*skipped = append(*skipped, Skipped{Key: top, Reason: reason})
		}
		apps, _ := data.get("apps").(*jsonObject)
		if apps == nil {
			continue
		}
		for _, appName := range apps.keys {
			conf, ok := apps.get(appName).(*jsonObject)
			if !ok {
				continue
			}
			aspec, known := spec.Apps.Values[appName]
			if !known {
				*skipped = append(*skipped, Skipped{Key: truncate(appName, 80), Reason: "CashPilot has no such service."})
				continue
			}
			values := map[string]Value{}
			for _, envKey := range aspec.Fields.Keys {
				src := aspec.Fields.Values[envKey]
				if src.JSON == "" {
					continue
				}
				text, ok := jsonLeaf(conf.get(src.JSON))
				if !ok {
					continue
				}
				if len(text) > MaxValueBytes {
					values[envKey] = Value{Retype: true}
				} else {
					values[envKey] = Value{Text: text}
				}
			}
			enabled, _ := conf.get("enabled").(bool)
			// Off with only template placeholders: the user never set this app up.
			hasValue := false
			for _, v := range values {
				if v.Retype || !IsPlaceholder(v.Text) {
					hasValue = true
				}
			}
			if !enabled && !hasValue {
				continue
			}
			found = append(found, foundApp{name: appName, spec: aspec, values: values, enabled: enabled})
		}
	}
	return found
}

func appsFromEnv(files []*fileSettings, spec toolSpec, skipped *[]Skipped) []foundApp {
	env := newEnv()
	for _, fs := range files {
		if fs.kind == "env" || fs.kind == "devices" {
			for _, k := range fs.env.keys {
				env.set(k, fs.env.values[k])
			}
		}
		if fs.kind == "proxies" {
			reason := notCarriedReason("proxies.txt", spec)
			if reason == "" {
				reason = "Proxies are not imported."
			}
			*skipped = append(*skipped, Skipped{Key: fs.name, Reason: reason})
		}
	}
	claimed := map[string]bool{}
	var found []foundApp
	for _, appName := range spec.Apps.Keys {
		aspec := spec.Apps.Values[appName]
		values := map[string]Value{}
		var own []string
		for _, envKey := range aspec.Fields.Keys {
			aliases := aspec.Fields.Values[envKey].Env
			own = append(own, aliases...)
			for _, alias := range aliases {
				if v, ok := env.get(alias); ok {
					values[envKey] = v
					break
				}
			}
		}
		own = append(own, aspec.OtherEnv...)
		on := false
		for _, key := range aspec.OnEnv.Keys {
			want := aspec.OnEnv.Values[key]
			if got, ok := env.get(key); ok && (want == "*" || strings.EqualFold(strings.TrimSpace(got.Text), want)) {
				on = true
			}
			claimed[key] = true
		}
		for _, k := range own {
			claimed[k] = true
		}
		// An untouched template placeholder is not a value: the user never set this app up.
		hasValue := false
		for _, k := range own {
			if v, ok := env.get(k); ok && (v.Retype || !IsPlaceholder(v.Text)) {
				hasValue = true
			}
		}
		if on || hasValue {
			found = append(found, foundApp{name: appName, spec: aspec, values: values, enabled: true})
		}
	}
	for _, key := range env.keys {
		value := env.values[key]
		if claimed[key] || matches(key, spec.IgnoreEnv) {
			continue
		}
		if !value.Retype {
			if t := strings.ToLower(strings.TrimSpace(value.Text)); t == "" || t == "false" {
				continue // blank or false: the old tool's way of saying "off"
			}
		}
		reason := notCarriedReason(key, spec)
		if reason == "" {
			reason = "CashPilot does not read this setting."
		}
		*skipped = append(*skipped, Skipped{Key: key, Reason: reason})
	}
	return found
}

// BuildPreview turns the files into one row per CashPilot service they configure,
// plus what was left out. extra holds values the user typed for fields the files
// did not fill: slug -> env key -> value.
func BuildPreview(files []*fileSettings, source string, getService func(string) (catalog.Service, bool), extra map[string]map[string]string) Preview {
	spec := sources.Tools.Values[source]
	var skipped []Skipped
	apps := append(appsFromJSON(files, spec, &skipped), appsFromEnv(files, spec, &skipped)...)

	rows := map[string]Row{}
	for _, app := range apps {
		slug := app.spec.Slug
		svc, ok := getService(slug)
		if _, dup := rows[slug]; !ok || dup || svc.Custom {
			continue
		}
		rows[slug] = buildRow(app, svc, extra[slug])
	}
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a != b {
			return a < b
		}
		return out[i].Slug < out[j].Slug
	})

	notices := []string{spec.Stop}
	for _, s := range skipped {
		if strings.Contains(s.Reason, "Proxies") {
			notices = append(notices, "Proxy settings were found and left out. Add each proxy in the Proxies section of the Fleet page.")
			break
		}
	}
	for _, fs := range files {
		if fs.kind == "devices" && fs.extraLines > 0 {
			notices = append(notices, fmt.Sprintf("%s lists %d more device(s) after the first. They belonged to "+
				"proxy copies and are not imported; only the first, your main device, is.", fs.name, fs.extraLines))
		}
	}
	if skipped == nil {
		skipped = []Skipped{}
	}
	return Preview{Source: source, SourceName: spec.Name, Rows: out, NotImported: skipped, Notices: notices}
}

func buildRow(app foundApp, svc catalog.Service, extra map[string]string) Row {
	name := svc.Name
	if name == "" {
		name = svc.Slug
	}
	row := Row{Slug: svc.Slug, Name: name, App: app.name, Selected: app.enabled, OffInSource: !app.enabled,
		Fields: []Field{}, Env: map[string]string{}}
	if app.spec.Unusable != "" {
		row.State, row.Reason, row.Selected = RowUnusable, app.spec.Unusable, false
		return row
	}
	if catalog.IsRetired(svc.Status) {
		row.State, row.Reason, row.Selected = RowNotDeployable, "This service is no longer available.", false
		return row
	}
	if svc.ManualOnly {
		row.State, row.Reason, row.Selected = RowNotDeployable, "CashPilot tracks this service, but there is nothing to deploy for it.", false
		return row
	}

	dockerEnv := map[string]catalog.EnvVar{}
	for _, v := range svc.Docker.Env {
		dockerEnv[v.Key] = v
	}
	keys := append([]string{}, app.spec.Fields.Keys...)
	for _, v := range svc.Docker.Env {
		if v.Required && v.Default == "" && !contains(keys, v.Key) {
			keys = append(keys, v.Key)
		}
	}
	anyInvalid, anyMissingRequired := false, false
	for _, key := range keys {
		v, ok := dockerEnv[key]
		if !ok {
			v = catalog.EnvVar{Key: key}
		}
		var text, state string
		value, read := app.values[key]
		if typed := strings.TrimSpace(extra[key]); typed != "" {
			text, state = typed, FieldImported
			if len(text) > MaxValueBytes {
				text, state = "", FieldInvalid
			}
		} else if !read {
			state = FieldMissing
		} else if value.Retype {
			state = FieldRetype
		} else if IsPlaceholder(value.Text) {
			state = FieldPlaceholder
		} else if strings.TrimSpace(value.Text) != value.Text {
			// Desktop stores and deploys a value as given, so spaces at an end are
			// not trimmed away silently: which one the old tool used is a guess.
			state = FieldRetype
		} else {
			text, state = value.Text, FieldImported
		}
		// A JSON "\u0000" decodes to a NUL after the file's own NUL check;
		// no env value can carry one.
		if text != "" && (!v.AcceptsValue(text) || strings.ContainsRune(text, 0)) {
			state = FieldInvalid
		}
		label := v.Label
		if label == "" {
			label = key
		}
		f := Field{Key: key, Label: label, Secret: v.Secret, Required: v.Required, State: state, Pattern: v.Pattern}
		if !v.Secret {
			f.Value = text
		}
		row.Fields = append(row.Fields, f)
		if state == FieldImported {
			row.Env[key] = text
		}
		if state == FieldInvalid {
			anyInvalid = true
		} else if state != FieldImported && f.Required {
			anyMissingRequired = true
		}
	}
	switch {
	case anyInvalid:
		row.State, row.Reason = RowInvalid, "A value does not have the format this service needs."
	case anyMissingRequired:
		row.State, row.Reason = RowMissingFields, "Fill in the fields marked below."
	default:
		row.State, row.Reason = RowReady, ""
	}
	if app.spec.Note != "" {
		row.Reason = app.spec.Note
	} else if row.State == RowMissingFields && app.spec.MissingNote != "" {
		row.Reason = app.spec.MissingNote
	}
	if row.OffInSource && row.State == RowReady {
		row.Reason = "Turned off in the old tool. Tick it to bring it over anyway."
	}
	return row
}

// Run reads files, works out the tool and builds the dry run in one call.
func Run(files []File, source string, getService func(string) (catalog.Service, bool), extra map[string]map[string]string) (Preview, error) {
	if source == "" {
		source = "auto"
	}
	if source != "auto" && !isSource(source) {
		return Preview{}, refuse("Unknown tool.")
	}
	read, err := ReadFiles(files, source)
	if err != nil {
		return Preview{}, err
	}
	detected, err := DetectSource(read, source)
	if err != nil {
		return Preview{}, err
	}
	return BuildPreview(read, detected, getService, extra), nil
}
