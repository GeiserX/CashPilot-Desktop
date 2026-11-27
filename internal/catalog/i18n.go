package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"path"
	"reflect"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// I18nDir holds the catalog's translations, vendored from the web repository with
// the service files: one <locale>.yml each, keyed "<slug>.<field path>". en.yml is
// the English source. A target file maps each key to {text, source}, where source
// is the first 12 hex digits of the SHA-256 of the English it was made from.
const I18nDir = "_i18n"

// translation is one entry of a target locale file.
type translation struct {
	Text   string `yaml:"text"`
	Source string `yaml:"source"`
}

// loadTranslations reads root/_i18n. A translation whose source hash is not the
// hash of today's English, or that is empty, is left out, so the English shows.
// A missing or unreadable folder means no translations, never a failed catalog.
func loadTranslations(fsys fs.FS, root string) map[string]map[string]string {
	dir := path.Join(root, I18nDir)
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil
	}
	var english map[string]string
	if raw, err := fs.ReadFile(fsys, path.Join(dir, "en.yml")); err != nil || yaml.Unmarshal(raw, &english) != nil {
		return nil
	}
	out := map[string]map[string]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".yml") || name == "en.yml" {
			continue
		}
		raw, err := fs.ReadFile(fsys, path.Join(dir, name))
		if err != nil {
			continue
		}
		var target map[string]translation
		if yaml.Unmarshal(raw, &target) != nil {
			continue
		}
		m := map[string]string{}
		for key, tr := range target {
			en, ok := english[key]
			if !ok || tr.Text == "" || tr.Source != sourceHash(en) {
				continue
			}
			m[key] = tr.Text
		}
		out[strings.TrimSuffix(name, ".yml")] = m
	}
	return out
}

func sourceHash(english string) string {
	sum := sha256.Sum256([]byte(english))
	return hex.EncodeToString(sum[:])[:12]
}

// Localized returns copies of services with their text in locale where the
// catalog has a current translation, and in English elsewhere. The catalog's own
// entries are never changed: the copies own every slice, map and pointer the
// translation writes into. Logic must keep reading the English entries; only text
// for the screen goes through here.
func (c *Catalog) Localized(services []Service, locale string) []Service {
	texts := c.translations[locale]
	if len(texts) == 0 {
		return services
	}
	out := make([]Service, len(services))
	for i, svc := range services {
		out[i] = localize(svc, texts)
	}
	return out
}

// LocalizedOne is Localized for one service.
func (c *Catalog) LocalizedOne(svc Service, locale string) Service {
	if texts := c.translations[locale]; len(texts) > 0 {
		return localize(svc, texts)
	}
	return svc
}

func localize(svc Service, texts map[string]string) Service {
	prefix := svc.Slug + "."
	var rating map[string]string // "cons.0" -> text
	for key, text := range texts {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		field := strings.TrimPrefix(key, prefix)
		if rest, ok := strings.CutPrefix(field, "rating."); ok {
			if rating == nil {
				rating = map[string]string{}
			}
			rating[strings.TrimSuffix(rest, ".text")] = text
			continue
		}
		setPath(reflect.ValueOf(&svc).Elem(), strings.Split(field, "."), text)
	}
	if rating != nil && svc.RatingResult != nil {
		svc.RatingResult = localizeRating(*svc.RatingResult, rating)
	}
	return svc
}

// localizeRating translates the computed pros and cons, which keep the order of
// the entry's rating block. TopCons are cons too, matched by their English text.
func localizeRating(r RatingResult, texts map[string]string) *RatingResult {
	byEnglish := map[string]string{}
	tr := func(items []RatingItem, kind string) []RatingItem {
		if items == nil {
			return nil
		}
		out := append([]RatingItem(nil), items...)
		for i := range out {
			if text, ok := texts[kind+"."+strconv.Itoa(i)]; ok {
				byEnglish[out[i].Text] = text
				out[i].Text = text
			}
		}
		return out
	}
	r.Cons = tr(r.Cons, "cons")
	r.Pros = tr(r.Pros, "pros")
	if r.TopCons != nil {
		top := append([]RatingItem(nil), r.TopCons...)
		for i := range top {
			if text, ok := byEnglish[top[i].Text]; ok {
				top[i].Text = text
			}
		}
		r.TopCons = top
	}
	return &r
}

// setPath writes text at the field path, walking structs by their yaml names and
// lists by an item's key or id, or by index. It copies every slice, map and
// pointer it passes through, so the value it started from shares nothing it wrote.
// A path that names nothing is ignored.
func setPath(v reflect.Value, segs []string, text string) {
	if len(segs) == 0 {
		if v.Kind() == reflect.String && v.CanSet() {
			v.SetString(text)
		}
		return
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() || !v.CanSet() {
			return
		}
		clone := reflect.New(v.Elem().Type())
		clone.Elem().Set(v.Elem())
		v.Set(clone)
		setPath(clone.Elem(), segs, text)
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			name := strings.Split(t.Field(i).Tag.Get("yaml"), ",")[0]
			if name == segs[0] && name != "" && name != "-" {
				setPath(v.Field(i), segs[1:], text)
				return
			}
		}
	case reflect.Slice:
		if v.IsNil() || !v.CanSet() {
			return
		}
		clone := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		reflect.Copy(clone, v)
		v.Set(clone)
		idx := -1
		for i := 0; i < clone.Len(); i++ {
			if itemName(clone.Index(i)) == segs[0] {
				idx = i
				break
			}
		}
		if idx < 0 {
			if n, err := strconv.Atoi(segs[0]); err == nil && n >= 0 && n < clone.Len() {
				idx = n
			}
		}
		if idx >= 0 {
			setPath(clone.Index(idx), segs[1:], text)
		}
	case reflect.Map:
		if v.IsNil() || !v.CanSet() || v.Type().Key().Kind() != reflect.String || v.Type().Elem().Kind() != reflect.String || len(segs) != 1 {
			return
		}
		clone := reflect.MakeMapWithSize(v.Type(), v.Len())
		iter := v.MapRange()
		for iter.Next() {
			clone.SetMapIndex(iter.Key(), iter.Value())
		}
		key := reflect.ValueOf(segs[0]).Convert(v.Type().Key())
		if clone.MapIndex(key).IsValid() {
			clone.SetMapIndex(key, reflect.ValueOf(text).Convert(v.Type().Elem()))
		}
		v.Set(clone)
	}
}

// itemName is a list item's own name: its key (env vars) or id (obligations).
func itemName(v reflect.Value) string {
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return ""
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return ""
	}
	for _, name := range []string{"key", "id"} {
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			if strings.Split(t.Field(i).Tag.Get("yaml"), ",")[0] == name && v.Field(i).Kind() == reflect.String {
				return v.Field(i).String()
			}
		}
	}
	return ""
}
