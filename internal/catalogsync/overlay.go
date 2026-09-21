// Package catalogsync vendors the CashPilot web catalog into this repository and
// applies the small set of Desktop-only deltas declared in catalog-overlay/.
//
// The design constraint that shapes everything here: a vendored file must stay as
// close to the web original as possible, byte for byte, so that reading it tells the
// truth and so that reviewing a sync commit shows what the web catalog actually
// changed. Re-serialising the YAML would reflow every folded block and drop every
// blank line, burying one real change in five hundred cosmetic ones. So the upstream
// bytes are copied verbatim and the overlay is applied as two narrow, exactly
// specified edits:
//
//   - an image pin replaces the single physical line that holds docker.image, whose
//     position is found by parsing the YAML (never by pattern-matching the text);
//   - an append adds a whole top-level block to the end of the file.
//
// Anything a future slice needs beyond those two should be a third named operation
// with its own guard, not a general-purpose merge: a merge that can silently rewrite
// any field is exactly how a vendored copy drifts without anyone noticing.
package catalogsync

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// ImagePin is one declared Desktop-only image pin: the digest-pinned reference that
// replaces the web catalog's floating docker.image for a service.
type ImagePin struct {
	// Image is the full pinned reference, e.g. "repo/name:tag@sha256:<64 hex>".
	Image string `yaml:"image"`
	// Why records what the pin is for, so a reader of catalog-overlay/image-pins.yml
	// does not have to guess. Free text; not interpreted.
	Why string `yaml:"why"`
}

// Overlay is the parsed contents of catalog-overlay/: every Desktop-only difference
// from the web catalog, declared in one place.
type Overlay struct {
	// ImagePins maps a service slug to its digest-pinned image reference.
	ImagePins map[string]ImagePin
	// Appends maps a catalog-relative path ("bandwidth/mysterium.yml") to raw YAML
	// text appended to the vendored file. When the web catalog has no such file, the
	// text is written on its own as a Desktop-only entry.
	Appends map[string][]byte
}

type overlayPinsFile struct {
	Pins map[string]ImagePin `yaml:"pins"`
}

// LoadOverlay reads catalog-overlay/ from disk. A missing directory is an empty
// overlay, which is a legitimate state (no declared deltas), not an error.
func LoadOverlay(dir string) (*Overlay, error) {
	ov := &Overlay{ImagePins: map[string]ImagePin{}, Appends: map[string][]byte{}}

	raw, err := os.ReadFile(filepath.Join(dir, "image-pins.yml"))
	switch {
	case err == nil:
		var parsed overlayPinsFile
		if err := yaml.Unmarshal(raw, &parsed); err != nil {
			return nil, fmt.Errorf("parse %s/image-pins.yml: %w", dir, err)
		}
		for slug, pin := range parsed.Pins {
			ov.ImagePins[slug] = pin
		}
	case os.IsNotExist(err):
	default:
		return nil, err
	}

	appendRoot := filepath.Join(dir, "append")
	err = filepath.WalkDir(appendRoot, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".yml") {
			return nil
		}
		rel, err := filepath.Rel(appendRoot, p)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		ov.Appends[filepath.ToSlash(rel)] = body
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return ov, nil
}

// Apply returns what the vendored services/ tree must contain: every upstream file
// with its declared overlay applied, plus any Desktop-only entry that exists only in
// the overlay. Keys are slash-separated paths relative to the catalog root.
//
// It fails rather than producing a plausible-looking tree when the overlay and the
// web catalog disagree, because every such disagreement is a way for the vendored
// copy to go quietly wrong:
//
//   - a pin for a slug the web catalog no longer ships, or that is retired, or that
//     upstream already pins itself — the pin is stale and would pin nothing;
//   - a pin whose repository or tag differs from the web entry's image — upstream
//     moved the image (the ProxyBase migration did exactly this) and the pin would
//     hold Desktop on the retired one, which looks healthy and earns nothing;
//   - a live upstream image with no pin at all — that is the digest-pin rule being
//     dropped on the floor, and it would only surface later as a failing test.
func Apply(upstream map[string][]byte, overlay *Overlay) (map[string][]byte, error) {
	if overlay == nil {
		overlay = &Overlay{ImagePins: map[string]ImagePin{}, Appends: map[string][]byte{}}
	}

	out := make(map[string][]byte, len(upstream)+len(overlay.Appends))
	pathBySlug := map[string]string{}
	var problems []string

	paths := sortedKeys(upstream)
	for _, rel := range paths {
		body := upstream[rel]
		if strings.HasPrefix(filepath.Base(rel), "_") {
			out[rel] = body
			continue
		}
		var svc struct {
			Slug   string `yaml:"slug"`
			Status string `yaml:"status"`
			Docker struct {
				Image string `yaml:"image"`
			} `yaml:"docker"`
		}
		if err := yaml.Unmarshal(body, &svc); err != nil {
			return nil, fmt.Errorf("parse upstream %s: %w", rel, err)
		}
		if svc.Slug != "" {
			pathBySlug[svc.Slug] = rel
		}

		pin, pinned := overlay.ImagePins[svc.Slug]
		upstreamImage := strings.TrimSpace(svc.Docker.Image)
		// The pin requirement is catalog.IsRetired, the same rule the UI uses to hide
		// a service, so a status that stops a service being deployable cannot also
		// demand a digest for an image nobody will ever pull.
		needsPin := upstreamImage != "" &&
			!strings.Contains(upstreamImage, "@sha256:") &&
			!catalog.IsRetired(svc.Status)

		switch {
		case pinned && upstreamImage == "":
			problems = append(problems, fmt.Sprintf("%s: pinned in the overlay but the web entry declares no docker.image", svc.Slug))
		case pinned && !needsPin:
			problems = append(problems, fmt.Sprintf("%s: pinned in the overlay but the web entry needs no pin (status %q, image %q); drop the pin", svc.Slug, svc.Status, upstreamImage))
		case pinned:
			if err := pinMatchesUpstream(pin.Image, upstreamImage); err != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", svc.Slug, err))
				break
			}
			replaced, err := replaceDockerImage(body, pin.Image)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", svc.Slug, err))
				break
			}
			body = replaced
		case needsPin:
			problems = append(problems, fmt.Sprintf("%s: live web entry ships the floating image %q and the overlay declares no digest pin for it", svc.Slug, upstreamImage))
		}
		out[rel] = body
	}

	for _, rel := range sortedKeys(overlay.Appends) {
		extra := overlay.Appends[rel]
		base, ok := out[rel]
		if !ok {
			// Desktop-only entry: the overlay file is the whole file.
			out[rel] = normaliseTrailingNewline(extra)
			continue
		}
		out[rel] = appendBlock(base, extra)
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, fmt.Errorf("catalog-overlay disagrees with the web catalog:\n  %s", strings.Join(problems, "\n  "))
	}

	for slug := range overlay.ImagePins {
		if _, ok := pathBySlug[slug]; !ok {
			return nil, fmt.Errorf("catalog-overlay pins %q but the web catalog has no such service; drop the pin", slug)
		}
	}
	return out, nil
}

// DriftKind says how a vendored file differs from what the overlay-applied web
// catalog says it should be.
type DriftKind string

const (
	// DriftMissing: the web catalog has this entry and the vendored copy does not.
	DriftMissing DriftKind = "missing"
	// DriftExtra: the vendored copy has an entry the web catalog no longer ships.
	DriftExtra DriftKind = "extra"
	// DriftChanged: both have the entry and the bytes differ.
	DriftChanged DriftKind = "changed"
)

// Drift is one vendored file that does not match the web catalog.
type Drift struct {
	Path string
	Kind DriftKind
}

// Diff reports every file in which have (the vendored tree) differs from want (the
// web catalog with the declared overlay applied), sorted by path.
func Diff(want, have map[string][]byte) []Drift {
	var out []Drift
	for _, rel := range sortedKeys(want) {
		switch existing, ok := have[rel]; {
		case !ok:
			out = append(out, Drift{Path: rel, Kind: DriftMissing})
		case !bytes.Equal(existing, want[rel]):
			out = append(out, Drift{Path: rel, Kind: DriftChanged})
		}
	}
	for _, rel := range sortedKeys(have) {
		if _, ok := want[rel]; !ok {
			out = append(out, Drift{Path: rel, Kind: DriftExtra})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// splitImageRef splits a Docker image reference into repository, tag and digest. A
// ':' is a tag only when it comes after the last '/'; before it, it is a registry
// port (localhost:5000/img).
func splitImageRef(ref string) (repo, tag, digest string) {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref, digest = ref[:i], ref[i+1:]
	}
	repo = ref
	if lastColon := strings.LastIndex(ref, ":"); lastColon > strings.LastIndex(ref, "/") {
		repo, tag = ref[:lastColon], ref[lastColon+1:]
	}
	return repo, tag, digest
}

// pinMatchesUpstream checks that a declared pin pins the image the web catalog
// actually names. The repository and the tag must both match: a tag change is how
// upstream ships a new generation of a client (urnetwork's g4-latest), and silently
// keeping the old digest under a new tag would leave Desktop running the previous
// generation while the entry claims otherwise.
func pinMatchesUpstream(pinned, upstream string) error {
	pRepo, pTag, pDigest := splitImageRef(pinned)
	uRepo, uTag, _ := splitImageRef(upstream)
	if pDigest == "" {
		return fmt.Errorf("overlay pin %q carries no @sha256: digest", pinned)
	}
	if pRepo != uRepo || pTag != uTag {
		return fmt.Errorf("overlay pins %q but the web entry now uses %q; re-resolve the digest for the new reference and update catalog-overlay/image-pins.yml", pinned, upstream)
	}
	return nil
}

// replaceDockerImage rewrites the single physical line that carries docker.image,
// preserving every other byte of the file. The line number comes from the YAML
// parser, so an "image:" appearing in a comment or in a description cannot be hit by
// mistake, and the value's own indentation and key text are reused verbatim.
func replaceDockerImage(body []byte, image string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("not a YAML mapping")
	}
	docker := mapValue(doc.Content[0], "docker")
	if docker == nil {
		return nil, fmt.Errorf("no docker: block to pin")
	}
	value := mapValue(docker, "image")
	if value == nil {
		return nil, fmt.Errorf("no docker.image to pin")
	}
	// A folded or multi-line image value would span lines this single-line rewrite
	// cannot express. It has never occurred and would be nonsense, but guessing is
	// how a rewrite corrupts a file.
	if value.Style == yaml.FoldedStyle || value.Style == yaml.LiteralStyle {
		return nil, fmt.Errorf("docker.image uses a block scalar, which the pin rewrite cannot rewrite safely")
	}

	lines := strings.SplitAfter(string(body), "\n")
	idx := value.Line - 1
	if idx < 0 || idx >= len(lines) {
		return nil, fmt.Errorf("docker.image reported at line %d, which is outside the file", value.Line)
	}
	line := lines[idx]
	eol := ""
	if strings.HasSuffix(line, "\n") {
		line, eol = strings.TrimSuffix(line, "\n"), "\n"
	}
	key := strings.Index(line, "image:")
	if key < 0 {
		return nil, fmt.Errorf("line %d does not hold the image key: %q", value.Line, line)
	}
	lines[idx] = line[:key] + "image: " + quoteImage(image) + eol
	return []byte(strings.Join(lines, "")), nil
}

// quoteImage double-quotes a pinned reference. A digest pin contains ':' twice and is
// long enough to wrap awkwardly; quoting keeps it one unambiguous scalar and matches
// how the web catalog writes its own pinned entries.
func quoteImage(image string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(image) + `"`
}

func mapValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

// appendBlock joins a vendored file and an overlay block with exactly one blank line
// between them, so repeated syncs of unchanged inputs produce identical bytes.
func appendBlock(base, extra []byte) []byte {
	var buf bytes.Buffer
	buf.Write(bytes.TrimRight(base, "\n"))
	buf.WriteString("\n\n")
	buf.Write(normaliseTrailingNewline(extra))
	return buf.Bytes()
}

func normaliseTrailingNewline(b []byte) []byte {
	return append(bytes.TrimRight(b, "\n"), '\n')
}

func sortedKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
