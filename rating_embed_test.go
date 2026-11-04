package main

import (
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
	svc, ok := cat.Get("honeygain")
	if !ok {
		t.Fatal("honeygain is not in the embedded catalog")
	}
	if svc.RatingResult == nil || !svc.RatingResult.Rated {
		t.Fatalf("honeygain is not graded from the embedded files: %+v", svc.RatingResult)
	}
}
