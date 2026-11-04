// Package importers reads another tool's config files into catalog fields, and
// nothing more. It is the Desktop port of the web app's app/importers.
//
// A pasted file is untrusted text. Nothing in it is run, expanded or fetched, and a
// value whose meaning depends on a shell is not guessed at: it is marked for
// retyping. The package does no I/O beyond reading its embedded sources.yml.
//
// The flow is ReadFiles (text to per-file settings), DetectSource (which tool wrote
// them), then BuildPreview (settings to one row per CashPilot service, checked
// against the catalog). The App bindings do the rest.
package importers

import (
	"bytes"
	_ "embed"
	"fmt"

	"gopkg.in/yaml.v3"
)

// sources.yml is a byte-for-byte copy of the web repository's
// app/importers/sources.yml: how each tool's files map onto the catalog. It is data
// only; this package runs nothing from it. TestEveryMappedSlugAndFieldIsInTheCatalog
// ties every slug and env key in it to the vendored catalog.
//
//go:embed sources.yml
var sourcesYAML []byte

// ordered is a YAML mapping read in file order. Order matters where the first match
// wins (an app's env aliases, not_carried patterns) and for a stable listing.
type ordered[T any] struct {
	Keys   []string
	Values map[string]T
}

func (o *ordered[T]) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: expected a mapping", node.Line)
	}
	o.Values = make(map[string]T, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		var value T
		if err := node.Content[i+1].Decode(&value); err != nil {
			return err
		}
		if _, seen := o.Values[key]; !seen {
			o.Keys = append(o.Keys, key)
		}
		o.Values[key] = value
	}
	return nil
}

type fieldSpec struct {
	JSON string   `yaml:"json"`
	Env  []string `yaml:"env"`
}

type appSpec struct {
	Slug        string             `yaml:"slug"`
	Fields      ordered[fieldSpec] `yaml:"fields"`
	OtherEnv    []string           `yaml:"other_env"`
	OnEnv       ordered[string]    `yaml:"on_env"`
	Unusable    string             `yaml:"unusable"`
	Note        string             `yaml:"note"`
	MissingNote string             `yaml:"missing_note"`
}

type markers struct {
	Keys   []string          `yaml:"keys"`
	Values map[string]string `yaml:"values"`
}

type toolSpec struct {
	Name       string           `yaml:"name"`
	Stop       string           `yaml:"stop"`
	Markers    markers          `yaml:"markers"`
	Apps       ordered[appSpec] `yaml:"apps"`
	IgnoreEnv  []string         `yaml:"ignore_env"`
	IgnoreJSON []string         `yaml:"ignore_json"`
	NotCarried ordered[string]  `yaml:"not_carried"`
}

type sourcesFile struct {
	// CollectorLinks is the web app's map from a deploy login to the earnings login
	// it also fills. Desktop keeps one credential set per service, read by both the
	// container and the collector, so saving the deploy values already fills it.
	CollectorLinks map[string]map[string]string `yaml:"collector_links"`
	Tools          ordered[toolSpec]            `yaml:"tools"`
}

var sources = mustLoadSources()

func mustLoadSources() sourcesFile {
	var out sourcesFile
	dec := yaml.NewDecoder(bytes.NewReader(sourcesYAML))
	dec.KnownFields(true)
	if err := dec.Decode(&out); err != nil {
		panic(fmt.Sprintf("importers: embedded sources.yml: %v", err))
	}
	return out
}

// Sources are the tools an import can come from, in the order the picker lists them.
var Sources = []string{"money4band", "cashfactory", "internetincome"}

func isSource(name string) bool {
	for _, s := range Sources {
		if s == name {
			return true
		}
	}
	return false
}
