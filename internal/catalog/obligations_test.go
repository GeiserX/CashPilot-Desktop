package catalog

import "testing"

// The web catalog's reminders (`obligations:`) are carried, not acted on, until
// Desktop ports the behaviour. This guards that they parse into the struct the port
// will read, so a field that stops decoding is noticed now rather than then.
func TestObligationsParse(t *testing.T) {
	cat := realCatalog(t)

	grass := mustGet(t, cat, "grass")
	if len(grass.Obligations) == 0 {
		t.Fatal("grass carries no obligations; the claim-stage2-usdc rule did not decode")
	}
	claim := grass.Obligations[0]
	if claim.ID != "claim-stage2-usdc" || claim.Action != "claim" || claim.When.Date != "2027-01-22" {
		t.Errorf("grass obligation decoded as %+v", claim)
	}
	if len(claim.WarnDays) != 4 || claim.Source == "" || claim.Checked == "" {
		t.Errorf("grass obligation lost its warn_days, source or checked: %+v", claim)
	}

	// The monthly form's day: `on` is a boolean key in YAML 1.1, so the web catalog
	// quotes it; it must still land in On here.
	pool := mustGet(t, cat, "mysterium").Obligations
	if len(pool) == 0 || pool[0].When.Every != "month" || pool[0].When.On != 1 || pool[0].When.DueDays != 30 {
		t.Errorf("mysterium monthly obligation decoded as %+v", pool)
	}

	honeygain := mustGet(t, cat, "honeygain").Obligations
	if len(honeygain) != 2 || honeygain[0].When.Since != "over_minimum" || honeygain[0].When.Days != 730 {
		t.Errorf("honeygain obligations decoded as %+v", honeygain)
	}
}
