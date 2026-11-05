package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/importers"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

func importApp(t *testing.T) (*App, *deployRecorder) {
	t.Helper()
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", t.TempDir())
	cfg, err := config.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cat, err := catalog.LoadEmbedded(serviceFiles)
	if err != nil {
		t.Fatal(err)
	}
	provider := &deployRecorder{}
	return &App{ctx: context.Background(), cfg: cfg, catalog: cat, store: st, runtime: provider,
		services: services.NewManager(provider, cat, st)}, provider
}

func importFixture(t *testing.T, rel string) importers.File {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("internal", "importers", "testdata", rel))
	if err != nil {
		t.Fatal(err)
	}
	return importers.File{Name: filepath.Base(rel), Content: string(raw)}
}

// THE RULE: no secret from an imported file reaches the frontend, through the
// preview binding's real JSON. The control is the save: the same secret does land in
// the store, so its absence from the preview is the masking and not a value that was
// never read.
func TestPreviewImportNeverSendsASecret(t *testing.T) {
	app, _ := importApp(t)
	files := []importers.File{importFixture(t, "money4band/user-config.json")}

	preview, err := app.PreviewImport(ImportRequest{Source: "auto", Files: files})
	if err != nil {
		t.Fatalf("PreviewImport: %v", err)
	}
	raw, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"fake-pass-honeygain", "fake-pass-$iproyal", "fake-pass-bitping", "11111111-2222-4333-8444-555555555555"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("the preview JSON holds %q", secret)
		}
	}
	if !strings.Contains(string(raw), "hg@example.com") {
		t.Error("a non-secret value is missing, so the preview may be empty and prove nothing")
	}

	if _, err := app.ApplyImport(ImportApplyRequest{Source: "auto", Files: files, Slugs: []string{"honeygain"}}); err != nil {
		t.Fatalf("ApplyImport: %v", err)
	}
	stored, err := app.store.GetCredentials("honeygain")
	if err != nil {
		t.Fatal(err)
	}
	if stored["HONEYGAIN_PASSWORD"] != "fake-pass-honeygain" || stored["HONEYGAIN_EMAIL"] != "hg@example.com" {
		t.Fatalf("control: the save did not store the secret: %v", stored)
	}
}

// A save is stored encrypted: the secret's bytes are nowhere in the database file.
func TestApplyImportStoresEncrypted(t *testing.T) {
	app, _ := importApp(t)
	files := []importers.File{importFixture(t, "cashfactory/.env.sample")}
	res, err := app.ApplyImport(ImportApplyRequest{Source: "auto", Files: files, Slugs: []string{"honeygain", "earnapp"}})
	if err != nil {
		t.Fatalf("ApplyImport: %v", err)
	}
	if len(res.Saved) != 2 {
		t.Fatalf("saved = %v", res.Saved)
	}
	if stored, _ := app.store.GetCredentials("honeygain"); stored["HONEYGAIN_PASSWORD"] != "fake-pass-honeygain" {
		t.Fatalf("control: honeygain was not saved: %v", stored)
	}
	_ = app.store.Close()
	entries, err := os.ReadDir(app.cfg.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	read := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(app.cfg.DataDir(), e.Name()))
		if err != nil {
			continue
		}
		read++
		if strings.Contains(string(raw), "fake-pass-honeygain") {
			t.Errorf("%s holds the password in clear", e.Name())
		}
	}
	if read == 0 {
		t.Fatal("no file in the data directory was read, so the check proves nothing")
	}
}

// Only ready rows are saved, and a refused save stores nothing at all.
func TestApplyImportRefusesARowThatIsNotReadyAndSavesNothing(t *testing.T) {
	app, _ := importApp(t)
	files := []importers.File{importFixture(t, "internetincome/properties.conf"), importFixture(t, "internetincome/earnapp.txt")}
	// earnapp is ready; repocket lacks its email, honeygain needs a retyped password.
	for _, slugs := range [][]string{{"earnapp", "repocket"}, {"earnapp", "honeygain"}, {"earnapp", "not-in-the-files"}} {
		if _, err := app.ApplyImport(ImportApplyRequest{Source: "auto", Files: files, Slugs: slugs}); err == nil {
			t.Errorf("%v: saved", slugs)
		}
		if stored, _ := app.store.GetCredentials("earnapp"); len(stored) != 0 {
			t.Fatalf("%v: a refused save stored earnapp: %v", slugs, stored)
		}
	}
	// The typed email makes repocket ready, and the save goes through.
	res, err := app.ApplyImport(ImportApplyRequest{Source: "auto", Files: files, Slugs: []string{"earnapp", "repocket"},
		Extra: map[string]map[string]string{"repocket": {"RP_EMAIL": "rp@example.com"}}})
	if err != nil || len(res.Saved) != 2 {
		t.Fatalf("with the email typed: %v %v", res, err)
	}
	if stored, _ := app.store.GetCredentials("repocket"); stored["RP_EMAIL"] != "rp@example.com" || stored["RP_API_KEY"] == "" {
		t.Errorf("repocket = %v", stored)
	}
}

// An import fills what is missing and never replaces a value already set here.
func TestApplyImportKeepsValuesAlreadySet(t *testing.T) {
	app, _ := importApp(t)
	if err := app.store.SaveCredentials("honeygain", map[string]string{"HONEYGAIN_EMAIL": "mine@example.com", "HONEYGAIN_PASSWORD": "my-working-pass"}); err != nil {
		t.Fatal(err)
	}
	if err := app.store.SaveCredentials("earnapp", map[string]string{"EARNAPP_OAUTH_TOKEN": "cookie-kept"}); err != nil {
		t.Fatal(err)
	}
	files := []importers.File{importFixture(t, "money4band/user-config.json")}
	preview, err := app.PreviewImport(ImportRequest{Source: "auto", Files: files})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range preview.Rows {
		if row.Slug == "honeygain" && !row.Existing.Configured {
			t.Error("the preview does not say honeygain is already set up")
		}
	}
	res, err := app.ApplyImport(ImportApplyRequest{Source: "auto", Files: files, Slugs: []string{"honeygain", "earnapp"}})
	if err != nil {
		t.Fatal(err)
	}
	hg, _ := app.store.GetCredentials("honeygain")
	if hg["HONEYGAIN_PASSWORD"] != "my-working-pass" || hg["HONEYGAIN_EMAIL"] != "mine@example.com" {
		t.Errorf("an import replaced working credentials: %v", hg)
	}
	if strings.Join(res.Kept["honeygain"], ",") != "HONEYGAIN_EMAIL,HONEYGAIN_PASSWORD" {
		t.Errorf("kept = %v", res.Kept)
	}
	ea, _ := app.store.GetCredentials("earnapp")
	if ea["EARNAPP_OAUTH_TOKEN"] != "cookie-kept" || ea["EARNAPP_UUID"] != "sdk-node-0123456789abcdef0123456789abcdef" {
		t.Errorf("earnapp = %v, want the cookie kept and the device id filled", ea)
	}
}

// Deploying what was imported goes through the normal deploy with the saved values.
func TestImportedServiceDeploysThroughTheNormalDeploy(t *testing.T) {
	app, provider := importApp(t)
	files := []importers.File{importFixture(t, "money4band/user-config.json")}
	if _, err := app.ApplyImport(ImportApplyRequest{Source: "auto", Files: files, Slugs: []string{"honeygain"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DeployService("honeygain", map[string]string{}, nil, nil); err != nil {
		t.Fatalf("DeployService: %v", err)
	}
	if len(provider.specs) != 1 || provider.specs[0].Env["HONEYGAIN_PASSWORD"] != "fake-pass-honeygain" {
		t.Fatalf("deployed specs = %+v", provider.specs)
	}
}

// A row the import cannot bring over is refused even when what is already saved
// would pass the deploy check: ProxyBase from money4band is unusable (its old user
// id is not the Access Token), and a saved token must not make it look imported.
func TestApplyImportRefusesAnUnusableRowWhateverIsSaved(t *testing.T) {
	app, _ := importApp(t)
	if err := app.store.SaveCredentials("proxybase", map[string]string{"ID": "saved-token"}); err != nil {
		t.Fatal(err)
	}
	if err := app.services.ValidateCredentials("proxybase", map[string]string{"ID": "saved-token"}); err != nil {
		t.Fatalf("control: the saved token should pass the deploy check, or this test proves nothing: %v", err)
	}
	files := []importers.File{importFixture(t, "money4band/user-config.json")}
	res, err := app.ApplyImport(ImportApplyRequest{Source: "auto", Files: files, Slugs: []string{"proxybase"}})
	if err == nil {
		t.Fatalf("an unusable row was saved: %+v", res)
	}
	if !strings.Contains(err.Error(), "cannot be imported yet") {
		t.Errorf("error = %v", err)
	}
}

// A saved email that differs from the file, next to a password filled from the
// file, would save one account's email with another's password: refused, and
// nothing changes.
func TestApplyImportRefusesToMixTwoLogins(t *testing.T) {
	app, _ := importApp(t)
	saved := map[string]string{"HONEYGAIN_EMAIL": "mine@example.com"}
	if err := app.store.SaveCredentials("honeygain", saved); err != nil {
		t.Fatal(err)
	}
	files := []importers.File{importFixture(t, "money4band/user-config.json")}
	res, err := app.ApplyImport(ImportApplyRequest{Source: "auto", Files: files, Slugs: []string{"repocket", "honeygain"}})
	if err == nil || !strings.Contains(err.Error(), "mix two logins") {
		t.Fatalf("err = %v, res = %+v", err, res)
	}
	hg, _ := app.store.GetCredentials("honeygain")
	if len(hg) != 1 || hg["HONEYGAIN_EMAIL"] != "mine@example.com" {
		t.Errorf("honeygain = %v, want it untouched", hg)
	}
	if rp, _ := app.store.GetCredentials("repocket"); len(rp) != 0 {
		t.Errorf("repocket was saved before the refusal: %v", rp)
	}
}
