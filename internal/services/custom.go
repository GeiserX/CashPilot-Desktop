package services

// Services the user adds from an image reference, without editing the catalog. The
// web app (app/custom_services.py) leads; this is its port.
//
// The catalog (services/*.yml) is reviewed and ships with CashPilot. A custom service
// is the user's own: a name, an image, some environment variables, an optional
// command, an optional data path and a memory limit, stored encrypted in the app's
// database and turned here into the catalog.Service shape the deploy path already
// reads.
//
// That shape is deliberately narrow. It never carries ports, capabilities, devices,
// a network mode, privileged mode or an entrypoint, so a custom service gets exactly
// the hardening every catalog service gets (runtime.applyHardening) and nothing a
// catalog entry can be granted. Its only mount is a named volume of its own.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

const (
	customMaxName     = 60
	customMaxEnv      = 50
	customMaxEnvValue = 4096
	customMaxCommand  = 1024
	// CustomDefaultMemLimit is the memory limit a custom service gets when none is given.
	CustomDefaultMemLimit = "512m"
	customMemMinMB        = 32
	customMemMaxMB        = 16 * 1024
)

// CustomUnpinnedWarning is shown for an image with no tag, or the latest tag.
const CustomUnpinnedWarning = "No version tag: every redeploy pulls whatever the publisher pushed last. Pin a tag or digest."

var (
	customEnvKey   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	customDataPath = regexp.MustCompile(`^/[A-Za-z0-9._/-]{1,200}$`)
	customMemLimit = regexp.MustCompile(`^(\d+)([mg])$`)
	slugUnsafe     = regexp.MustCompile(`[^a-z0-9]+`)
	slugCopySuffix = regexp.MustCompile(`-(\d+)$`)
)

// CustomEnv is one environment variable of a custom service. Value is the plain
// value: it is encrypted at rest with the rest of the definition, and a Secret one
// never leaves the backend again once saved.
type CustomEnv struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Secret bool   `json:"secret"`
}

// CustomDefinition is what the user typed, and what is stored, for one custom service.
type CustomDefinition struct {
	Name     string      `json:"name"`
	Image    string      `json:"image"`
	Env      []CustomEnv `json:"env"`
	Command  string      `json:"command"`
	DataPath string      `json:"dataPath"`
	MemLimit string      `json:"memLimit"`
}

// CustomEnvView is a variable as the editor may see it: a secret's value is blank,
// and IsSet says whether one is stored.
type CustomEnvView struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Secret bool   `json:"secret"`
	IsSet  bool   `json:"isSet"`
}

// CustomServiceView is a custom service as the UI receives it.
type CustomServiceView struct {
	Slug     string          `json:"slug"`
	Name     string          `json:"name"`
	Image    string          `json:"image"`
	Env      []CustomEnvView `json:"env"`
	Command  string          `json:"command"`
	DataPath string          `json:"dataPath"`
	MemLimit string          `json:"memLimit"`
	// Volume is the named volume the data path is kept in, "" with no data path.
	Volume   string   `json:"volume"`
	Warnings []string `json:"warnings"`
}

// splitImageRef splits an image reference into repository, tag and digest. A ':' is
// a tag only after the last '/'; before it, it is a registry port.
func splitImageRef(ref string) (repo, tag, digest string) {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref, digest = ref[:i], ref[i+1:]
	}
	repo = ref
	if colon := strings.LastIndex(ref, ":"); colon > strings.LastIndex(ref, "/") {
		repo, tag = ref[:colon], ref[colon+1:]
	}
	return repo, tag, digest
}

// CustomImageWarnings is what is worth telling the user about an image reference.
func CustomImageWarnings(image string) []string {
	_, tag, digest := splitImageRef(image)
	if digest == "" && (tag == "" || tag == "latest") {
		return []string{CustomUnpinnedWarning}
	}
	return []string{}
}

// MakeCustomSlug is a new custom- slug for name that taken does not hold.
//
// Fixed at creation: the container name, the data volume and every record key hang
// on it, so renaming the service later never moves anything.
//
// A slug never ends in -<digits>. Copy N of a service runs as cashpilot-<slug>-N, so
// a slug like custom-foo-2 would share its container name with copy 2 of custom-foo,
// and a deploy of one would replace the other. "Foo 2" becomes custom-foo2, and a
// second "Foo" becomes custom-foo-v2.
//
// taken must hold every slug ever used, deleted ones included: a reused slug would
// mount the data volume a deleted service left behind.
func MakeCustomSlug(name string, taken map[string]bool) string {
	base := strings.Trim(slugUnsafe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(base) > 40 {
		base = strings.Trim(base[:40], "-")
	}
	if base == "" {
		base = "service"
	}
	base = slugCopySuffix.ReplaceAllString(base, "$1")
	slug := catalog.CustomPrefix + base
	for n := 2; taken[slug]; n++ {
		slug = fmt.Sprintf("%s%s-v%d", catalog.CustomPrefix, base, n)
	}
	return slug
}

// CustomVolumeName is the named volume a custom service keeps its data in. Named, not
// a host path, so it can never reach a host directory, and deterministic, so every
// redeploy and a change of data path land on the same volume.
func CustomVolumeName(slug string) string {
	return "cashpilot-" + slug + "-data"
}

func printable(s string) bool {
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// ValidateCustom is the stored definition for in, and the warnings worth showing. It
// returns an error meant for the user. previous is the stored definition on an edit,
// so a secret left blank keeps its value.
func ValidateCustom(in CustomDefinition, previous *CustomDefinition) (CustomDefinition, []string, error) {
	name := strings.TrimSpace(in.Name)
	if n := len([]rune(name)); n < 1 || n > customMaxName || !printable(name) {
		return CustomDefinition{}, nil, fmt.Errorf("name must be 1 to %d printable characters", customMaxName)
	}
	image := strings.TrimSpace(in.Image)
	if image == "" || ValidateImage(image) != nil {
		return CustomDefinition{}, nil, errors.New("image is not a valid image reference (repository[:tag][@digest])")
	}
	env, err := validateCustomEnv(in.Env, previous)
	if err != nil {
		return CustomDefinition{}, nil, err
	}
	command := strings.TrimSpace(in.Command)
	if len(command) > customMaxCommand || strings.ContainsAny(command, "\x00\n\r") {
		return CustomDefinition{}, nil, fmt.Errorf("command must be one line of at most %d characters", customMaxCommand)
	}
	dataPath := strings.TrimSpace(in.DataPath)
	if dataPath != "" && (!customDataPath.MatchString(dataPath) || path.Clean(dataPath) != dataPath || dataPath == "/" ||
		strings.Contains("/"+dataPath+"/", "/../")) {
		return CustomDefinition{}, nil, errors.New("data path must be an absolute path inside the container, like /data, with no '..'")
	}
	mem, err := validateCustomMem(in.MemLimit)
	if err != nil {
		return CustomDefinition{}, nil, err
	}
	def := CustomDefinition{Name: name, Image: image, Env: env, Command: command, DataPath: dataPath, MemLimit: mem}
	return def, CustomImageWarnings(image), nil
}

func validateCustomEnv(in []CustomEnv, previous *CustomDefinition) ([]CustomEnv, error) {
	if len(in) > customMaxEnv {
		return nil, fmt.Errorf("at most %d environment variables", customMaxEnv)
	}
	stored := map[string]string{}
	if previous != nil {
		for _, e := range previous.Env {
			if e.Secret {
				stored[e.Key] = e.Value
			}
		}
	}
	out := make([]CustomEnv, 0, len(in))
	seen := map[string]bool{}
	for _, item := range in {
		key := strings.TrimSpace(item.Key)
		if !customEnvKey.MatchString(key) {
			return nil, errors.New("variable names start with a letter or _, then letters, digits or _ (at most 128 characters)")
		}
		if seen[key] {
			return nil, fmt.Errorf("%s is set twice", key)
		}
		seen[key] = true
		value := item.Value
		if old, ok := stored[key]; ok && value == "" {
			// A stored secret is never sent back to the UI, so a blank box on an edit
			// means "unchanged", not "erase it". That holds when the Secret tick is
			// cleared too: the box was blank because the value was hidden.
			value = old
		}
		if len(value) > customMaxEnvValue || strings.ContainsRune(value, 0) {
			return nil, fmt.Errorf("the value of %s is too long or holds a NUL character", key)
		}
		out = append(out, CustomEnv{Key: key, Value: value, Secret: item.Secret})
	}
	return out, nil
}

func validateCustomMem(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		value = CustomDefaultMemLimit
	}
	match := customMemLimit.FindStringSubmatch(value)
	if match == nil {
		return "", errors.New("memory limit must look like 512m or 2g")
	}
	mb, err := strconv.Atoi(match[1])
	if err != nil {
		return "", errors.New("memory limit must be between 32m and 16g")
	}
	if match[2] == "g" {
		mb *= 1024
	}
	if mb < customMemMinMB || mb > customMemMaxMB {
		return "", errors.New("memory limit must be between 32m and 16g")
	}
	return value, nil
}

// CustomAsService is the catalog-shaped service for a custom definition.
//
// reveal=true puts the real env values in Docker.Env as defaults, for the deploy
// path only; it must never reach the UI. reveal=false leaves Docker.Env empty, so the
// deploy form shows no inputs, and is what the catalog overlay holds.
//
// Nothing here can grant anything: no ports, capabilities, devices, network mode,
// privileged mode, entrypoint or host path. Those fields stay at their zero values.
func CustomAsService(slug string, def CustomDefinition, reveal bool) catalog.Service {
	docker := catalog.DockerConfig{
		Image:     def.Image,
		Command:   def.Command,
		Resources: catalog.ResourceLimits{MemLimit: def.MemLimit},
	}
	if docker.Resources.MemLimit == "" {
		docker.Resources.MemLimit = CustomDefaultMemLimit
	}
	if def.DataPath != "" {
		docker.Volumes = []string{CustomVolumeName(slug) + ":" + def.DataPath}
	}
	if reveal {
		for _, e := range def.Env {
			docker.Env = append(docker.Env, catalog.EnvVar{Key: e.Key, Label: e.Key, Default: e.Value, Secret: e.Secret})
		}
	} else {
		// The overlay is read by the UI, so the command, which often carries a token,
		// is not shown there either; Deploy reads the revealed definition.
		docker.Command = ""
	}
	return catalog.Service{
		Name:             def.Name,
		Slug:             slug,
		Category:         "custom",
		Status:           "custom",
		Description:      "Added by you from " + def.Image + ". CashPilot has not reviewed this image.",
		ShortDescription: "Your image: " + def.Image,
		Docker:           docker,
		Custom:           true,
	}
}

func customView(slug string, def CustomDefinition) CustomServiceView {
	view := CustomServiceView{
		Slug: slug, Name: def.Name, Image: def.Image, Command: def.Command,
		DataPath: def.DataPath, MemLimit: def.MemLimit, Env: []CustomEnvView{},
		Warnings: CustomImageWarnings(def.Image),
	}
	if def.DataPath != "" {
		view.Volume = CustomVolumeName(slug)
	}
	for _, e := range def.Env {
		value := e.Value
		if e.Secret {
			value = ""
		}
		view.Env = append(view.Env, CustomEnvView{Key: e.Key, Value: value, Secret: e.Secret, IsSet: e.Value != ""})
	}
	return view
}

func (m *Manager) customDefinition(slug string) (CustomDefinition, bool, error) {
	row, ok, err := m.store.GetCustomService(slug)
	if err != nil || !ok {
		return CustomDefinition{}, ok, err
	}
	var def CustomDefinition
	if err := json.Unmarshal(row.Definition, &def); err != nil {
		return CustomDefinition{}, false, fmt.Errorf("custom service %s: %w", slug, err)
	}
	return def, true, nil
}

// customDefinitions is every live custom service's definition, by slug order.
func (m *Manager) customDefinitions() ([]string, []CustomDefinition, error) {
	rows, err := m.store.ListCustomServices()
	if err != nil {
		return nil, nil, err
	}
	slugs := make([]string, 0, len(rows))
	defs := make([]CustomDefinition, 0, len(rows))
	for _, row := range rows {
		var def CustomDefinition
		if err := json.Unmarshal(row.Definition, &def); err != nil {
			return nil, nil, fmt.Errorf("custom service %s: %w", row.Slug, err)
		}
		slugs = append(slugs, row.Slug)
		defs = append(defs, def)
	}
	return slugs, defs, nil
}

// LoadCustom puts the stored custom services into the catalog overlay, masked, so
// every lookup by slug (names, the dashboard, stop, remove) finds them.
func (m *Manager) LoadCustom() error {
	slugs, defs, err := m.customDefinitions()
	if err != nil {
		return err
	}
	services := make([]catalog.Service, 0, len(slugs))
	for i, slug := range slugs {
		services = append(services, CustomAsService(slug, defs[i], false))
	}
	m.catalog.SetCustom(services)
	return nil
}

// ListCustom returns every custom service as the editor may see it.
func (m *Manager) ListCustom() ([]CustomServiceView, error) {
	slugs, defs, err := m.customDefinitions()
	if err != nil {
		return nil, err
	}
	out := make([]CustomServiceView, 0, len(slugs))
	for i, slug := range slugs {
		out = append(out, customView(slug, defs[i]))
	}
	return out, nil
}

// SaveCustom creates a custom service (slug "") or replaces one's definition. A
// running service keeps what it was deployed with until it is redeployed.
func (m *Manager) SaveCustom(slug string, in CustomDefinition) (CustomServiceView, error) {
	m.customMu.Lock()
	defer m.customMu.Unlock()
	var previous *CustomDefinition
	if slug != "" {
		if !catalog.IsCustomSlug(slug) {
			return CustomServiceView{}, fmt.Errorf("%s is not a custom service", slug)
		}
		def, ok, err := m.customDefinition(slug)
		if err != nil {
			return CustomServiceView{}, err
		}
		if !ok {
			return CustomServiceView{}, fmt.Errorf("unknown custom service: %s", slug)
		}
		previous = &def
	}
	def, _, err := ValidateCustom(in, previous)
	if err != nil {
		return CustomServiceView{}, err
	}
	raw, err := json.Marshal(def)
	if err != nil {
		return CustomServiceView{}, err
	}
	if slug == "" {
		taken, err := m.store.CustomServiceSlugs()
		if err != nil {
			return CustomServiceView{}, err
		}
		slug = MakeCustomSlug(def.Name, taken)
		if err := m.store.InsertCustomService(slug, raw); err != nil {
			return CustomServiceView{}, err
		}
		m.store.RecordEvent(slug, "custom_created", def.Image)
	} else {
		if err := m.store.UpdateCustomService(slug, raw); err != nil {
			return CustomServiceView{}, err
		}
		m.store.RecordEvent(slug, "custom_updated", def.Image)
	}
	if err := m.LoadCustom(); err != nil {
		return CustomServiceView{}, err
	}
	return customView(slug, def), nil
}

// DeleteCustom deletes a custom service's definition. It is refused while this
// machine runs or records it: remove it from here first, which is also where the
// user decides about its data volume.
func (m *Manager) DeleteCustom(ctx context.Context, slug string) error {
	m.customMu.Lock()
	defer m.customMu.Unlock()
	if !catalog.IsCustomSlug(slug) {
		return fmt.Errorf("%s is not a custom service", slug)
	}
	if _, ok, err := m.store.GetDeployment(slug); err != nil {
		return err
	} else if ok {
		return errors.New("remove it from this computer first; it is still deployed here")
	}
	infos, err := m.List(ctx)
	if err != nil {
		return fmt.Errorf("cannot tell whether it still runs here: %w", err)
	}
	for _, info := range infos {
		if info.Slug == slug {
			return errors.New("remove it from this computer first; a container of it still exists here")
		}
	}
	if err := m.store.DeleteCustomService(slug); err != nil {
		return err
	}
	m.store.RecordEvent(slug, "custom_deleted", "")
	return m.LoadCustom()
}

// revealCustom is the custom service with its real env values and command, for a
// deploy. It never leaves this package.
func (m *Manager) revealCustom(slug string) (catalog.Service, error) {
	def, ok, err := m.customDefinition(slug)
	if err != nil {
		return catalog.Service{}, err
	}
	if !ok {
		return catalog.Service{}, fmt.Errorf("unknown service: %s", slug)
	}
	return CustomAsService(slug, def, true), nil
}
