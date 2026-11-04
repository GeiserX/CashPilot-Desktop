package main

import (
	"errors"
	"fmt"
	"sort"

	"github.com/GeiserX/CashPilot-Desktop/internal/importers"
)

// ImportRequest is the files the import page read, and the tool the user picked
// ("auto" lets the files say).
type ImportRequest struct {
	Source string           `json:"source"`
	Files  []importers.File `json:"files"`
}

// ImportApplyRequest is a save: the same files again (a masked value never has to
// come back from the page), the ticked services, and the values typed for fields
// the files did not fill (slug -> env key -> value).
type ImportApplyRequest struct {
	Source string                       `json:"source"`
	Files  []importers.File             `json:"files"`
	Slugs  []string                     `json:"slugs"`
	Extra  map[string]map[string]string `json:"extra"`
}

// ImportResult says what a save stored. Kept lists, per service, the fields that
// already had a value here and were left as they were.
type ImportResult struct {
	Saved []string            `json:"saved"`
	Kept  map[string][]string `json:"kept"`
}

// PreviewImport is the dry run: what the files of money4band, CashFactory or
// InternetIncome would bring over, checked against the catalog, with secrets shown
// only as set. Nothing is saved, run or fetched, and the files are not kept.
func (a *App) PreviewImport(req ImportRequest) (importers.Preview, error) {
	if err := a.ready(); err != nil {
		return importers.Preview{}, err
	}
	preview, err := importers.Run(req.Files, req.Source, a.catalog.Get, nil)
	if err != nil {
		return importers.Preview{}, err
	}
	for i := range preview.Rows {
		row := &preview.Rows[i]
		stored, err := a.store.GetCredentials(row.Slug)
		row.Existing.Configured = err == nil && len(stored) > 0
		_, deployed, _ := a.store.GetDeployment(row.Slug)
		row.Existing.Deployed = deployed
		row.Env = nil
	}
	return preview, nil
}

// ApplyImport saves the ticked services' credentials, encrypted like every other
// credential, after parsing the files again here. Only rows that are ready are
// saved, and every one is checked before any is written, so a refused save stores
// nothing. A field that already has a value on this computer keeps it: an import
// fills what is missing and never replaces working credentials, which the earnings
// collectors read too. Deploying is a separate step through DeployService, so every
// check a normal deploy runs still runs.
func (a *App) ApplyImport(req ImportApplyRequest) (ImportResult, error) {
	if err := a.ready(); err != nil {
		return ImportResult{}, err
	}
	if len(req.Slugs) == 0 {
		return ImportResult{}, errors.New("Choose at least one service.")
	}
	preview, err := importers.Run(req.Files, req.Source, a.catalog.Get, req.Extra)
	if err != nil {
		return ImportResult{}, err
	}
	bySlug := map[string]importers.Row{}
	for _, row := range preview.Rows {
		bySlug[row.Slug] = row
	}
	type pending struct {
		slug   string
		values map[string]string
		kept   []string
	}
	var todo []pending
	seen := map[string]bool{}
	for _, slug := range req.Slugs {
		if seen[slug] {
			continue
		}
		seen[slug] = true
		row, ok := bySlug[slug]
		if !ok {
			return ImportResult{}, fmt.Errorf("%.60s is not in these files.", slug)
		}
		if row.State != importers.RowReady {
			return ImportResult{}, fmt.Errorf("%s cannot be imported yet: %s", row.Name, row.Reason)
		}
		stored, err := a.store.GetCredentials(slug)
		if err != nil {
			return ImportResult{}, fmt.Errorf("could not read the saved credentials of %s: %w", row.Name, err)
		}
		merged := make(map[string]string, len(stored)+len(row.Env))
		for k, v := range stored {
			merged[k] = v
		}
		var kept []string
		for k, v := range row.Env {
			if merged[k] != "" {
				if merged[k] != v {
					kept = append(kept, k)
				}
				continue
			}
			merged[k] = v
		}
		sort.Strings(kept)
		if err := a.services.ValidateCredentials(slug, merged); err != nil {
			return ImportResult{}, fmt.Errorf("%s: %w", row.Name, err)
		}
		todo = append(todo, pending{slug: slug, values: merged, kept: kept})
	}
	result := ImportResult{Saved: []string{}, Kept: map[string][]string{}}
	for _, p := range todo {
		if err := a.store.SaveCredentials(p.slug, p.values); err != nil {
			return result, err
		}
		result.Saved = append(result.Saved, p.slug)
		if len(p.kept) > 0 {
			result.Kept[p.slug] = p.kept
		}
	}
	return result, nil
}
