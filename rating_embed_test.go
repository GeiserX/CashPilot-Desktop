package main

import (
	"io/fs"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// The released app loads the catalog from the files embedded in the binary, not from
// disk. If the embed pattern leaves out services/_ratings.yml the app still starts,
// and every provider quietly shows "Not rated yet".
func TestTheEmbeddedCatalogCarriesTheGradingMethod(t *testing.T) {
	cat, err := catalog.LoadEmbedded(serviceFiles)
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	// LoadEmbedded falls back to the files on disk when the embed holds no service,
	// so check the embed itself too.
	for _, name := range []string{"services/_ratings.yml", "services/bandwidth/honeygain.yml"} {
		if _, err := fs.Stat(serviceFiles, name); err != nil {
			t.Fatalf("%s is not embedded: %v", name, err)
		}
	}
	svc, ok := cat.Get("honeygain")
	if !ok {
		t.Fatal("honeygain is not in the embedded catalog")
	}
	if svc.RatingResult == nil || !svc.RatingResult.Rated {
		t.Fatalf("honeygain is not graded from the embedded files: %+v", svc.RatingResult)
	}
}
