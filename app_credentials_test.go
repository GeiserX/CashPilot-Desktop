package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/collectors"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

func TestGetCredentialsReportsOnlyPresenceForCatalogSecrets(t *testing.T) {
	app := newAppUnderTest(t)
	const secret = "private test credential"
	for _, svc := range app.catalog.List() {
		secrets := map[string]bool{}
		values := map[string]string{}
		for _, fields := range [][]catalog.EnvVar{svc.Docker.Env, svc.Native.Env} {
			for _, field := range fields {
				if field.Secret || field.SeedFile != nil {
					secrets[field.Key] = field.SeedFile != nil
				}
				values[field.Key] = "public test setting"
			}
		}
		for _, field := range collectors.Fields(svc.Slug) {
			if field.Secret {
				secrets[field.Key] = false
			}
			values[field.Key] = "public test setting"
		}
		if len(secrets) == 0 {
			continue
		}
		t.Run(svc.Slug, func(t *testing.T) {
			for key := range secrets {
				values[key] = secret
			}
			if err := app.store.SaveCredentials(svc.Slug, values); err != nil {
				t.Fatal(err)
			}
			shown, err := app.GetCredentials(svc.Slug)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(shown)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), secret) {
				t.Fatal("GetCredentials returned a saved secret")
			}
			var view struct {
				Values       map[string]string
				SavedSecrets map[string]bool
			}
			if err := json.Unmarshal(encoded, &view); err != nil {
				t.Fatal(err)
			}
			for key, want := range values {
				isSeed, private := secrets[key]
				if private {
					if _, ok := view.Values[key]; ok || view.SavedSecrets[key] == isSeed {
						t.Errorf("wrong field view for %s", key)
					}
				} else if view.Values[key] != want {
					t.Errorf("missing public setting %s", key)
				}
			}
		})
	}
}

func TestSiaSaveKeepsBlankPasswordAndReplacesTypedPassword(t *testing.T) {
	app := newAppUnderTest(t)
	const passwordKey = "HOSTD_API_PASSWORD"
	const pathKey = "SIA_STORAGE_DIR"
	initial := siaSettings()
	if _, err := app.DeployServiceWallet(siaSlug, initial, nil, nil, WalletChoice{Mode: runtime.WalletImport, Phrase: seedPhraseA}); err != nil {
		t.Fatal(err)
	}

	check := func(password, storage string) {
		t.Helper()
		values, err := app.store.GetCredentials(siaSlug)
		if err != nil {
			t.Fatal(err)
		}
		if values[passwordKey] != password || values[pathKey] != storage {
			t.Error("saved password or storage directory is incorrect")
		}
	}
	if err := app.SaveCredentials(siaSlug, map[string]string{passwordKey: "", pathKey: "/srv/updated-sia"}); err != nil {
		t.Fatal(err)
	}
	check(initial[passwordKey], "/srv/updated-sia")
	const replacement = "new test password"
	if err := app.SaveCredentials(siaSlug, map[string]string{passwordKey: replacement, pathKey: "/srv/updated-sia"}); err != nil {
		t.Fatal(err)
	}
	check(replacement, "/srv/updated-sia")
}

func TestSiaDeployKeepsBlankPasswordAndReplacesTypedPassword(t *testing.T) {
	app := newAppUnderTest(t)
	const passwordKey = "HOSTD_API_PASSWORD"
	initial := siaSettings()
	if err := app.SaveCredentials(siaSlug, initial); err != nil {
		t.Fatal(err)
	}
	deploy := func(password, want string) {
		t.Helper()
		if _, err := app.DeployServiceWallet(siaSlug, map[string]string{
			passwordKey: password, "SIA_STORAGE_DIR": initial["SIA_STORAGE_DIR"],
		}, nil, nil, WalletChoice{Mode: runtime.WalletImport, Phrase: seedPhraseA}); err != nil {
			t.Fatal(err)
		}
		saved, err := app.store.GetCredentials(siaSlug)
		if err != nil {
			t.Fatal(err)
		}
		container, ok := app.Docker.Container(siaContainer)
		if saved[passwordKey] != want || !ok || !slices.Contains(container.Env, passwordKey+"="+want) {
			t.Error("the deploy did not save and use the expected password")
		}
	}
	deploy("", initial[passwordKey])
	deploy("new test password", "new test password")
}

func TestOrdinarySettingsSaveKeepsLegacyCollectorToken(t *testing.T) {
	app := newAppUnderTest(t)
	const slug = "traffmonetizer"
	const token = "1|legacy-saved-test"
	if err := app.store.SaveCredentials(slug, map[string]string{
		"token": token, "TRAFFMONETIZER_DEVICE_NAME": "old-name",
	}); err != nil {
		t.Fatal(err)
	}
	checkCollector := func() {
		t.Helper()
		record, err := app.CollectService(slug)
		if err != nil || record.Error != "" || record.Balance != 7.25 {
			t.Fatalf("collector did not use the legacy token: %v, %s", err, record.Error)
		}
	}
	checkCollector()
	if err := app.SaveCredentials(slug, map[string]string{
		"TRAFFMONETIZER_TOKEN": "", "TRAFFMONETIZER_DEVICE_NAME": "new-name",
	}); err != nil {
		t.Fatal(err)
	}
	stored, err := app.store.GetCredentials(slug)
	if err != nil || stored["token"] != token || stored["TRAFFMONETIZER_TOKEN"] != "" ||
		stored["TRAFFMONETIZER_DEVICE_NAME"] != "new-name" {
		t.Fatalf("ordinary settings save did not preserve the legacy token: %v", err)
	}
	checkCollector()
}

func TestFirstLocalDeploySavesSecretOnlyAfterSuccess(t *testing.T) {
	app := newAppUnderTest(t)
	app.collectors = nil
	const slug = "honeygain"
	const password = "fresh-fixture-password"
	values := map[string]string{"HONEYGAIN_EMAIL": "test@example.test", "HONEYGAIN_PASSWORD": password}
	app.Docker.FailCreate("cashpilot-"+slug, "test capacity failure")
	if _, err := app.DeployService(slug, values, nil, nil); err == nil {
		t.Fatal("fixture deploy should fail")
	}
	view, err := app.GetCredentials(slug)
	if err != nil || view.SavedSecrets["HONEYGAIN_PASSWORD"] {
		t.Fatalf("failed deploy saved the password: %v", err)
	}
	if _, err := app.DeployService(slug, values, nil, nil); err != nil {
		t.Fatal(err)
	}
	stored, err := app.store.GetCredentials(slug)
	if err != nil || stored["HONEYGAIN_PASSWORD"] != password {
		t.Fatalf("successful retry did not save the password: %v", err)
	}
}
