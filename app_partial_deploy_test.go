package main

import (
	"slices"
	"strings"
	"testing"
)

func TestLocalPartialDeployKeepsRunningToken(t *testing.T) {
	for _, scenario := range []string{"replacement", "first install"} {
		t.Run(scenario, func(t *testing.T) {
			app := newAppUnderTest(t)
			app.collectors = nil
			const slug, key = "traffmonetizer", "TRAFFMONETIZER_TOKEN"
			const oldToken, newToken = "1|previous-test", "2|replacement-test"
			const name = "cashpilot-" + slug
			hasToken := func(container, want string) bool {
				c, ok := app.Docker.Container(container)
				return ok && c.State == "running" && slices.Contains(c.Cmd, want)
			}
			if scenario == "replacement" {
				if _, err := app.DeployService(slug, map[string]string{key: oldToken}, nil, nil); err != nil {
					t.Fatal(err)
				}
			}
			app.Docker.FailCreate(name+"-2", "fixture failure starting copy two")
			two := 2
			_, err := app.DeployService(slug, map[string]string{key: newToken}, nil, &two)
			if err == nil || !strings.Contains(err.Error(), "copy 2 of Traffmonetizer failed to start") {
				t.Fatalf("expected copy two failure, got %v", err)
			}
			reply, ok := formatBindingError(err).(string)
			if !ok || !strings.HasPrefix(reply, "[deploy_credentials_saved] ") || !strings.Contains(reply, "will use them on retry") {
				t.Fatal("expected Setup to get a saved-credentials notice")
			}
			dep, ok, err := app.store.GetDeployment(slug)
			if err != nil || !ok || dep.Instances != 2 || !hasToken(name, newToken) {
				t.Fatalf("partial deploy did not leave copy one running with the new token; deployment=%t err=%v", ok, err)
			}
			saved, err := app.store.GetCredentials(slug)
			if err != nil {
				t.Fatal(err)
			}
			if saved[key] != newToken {
				t.Error("expected the successful copy's token in storage")
			}
			if _, err := app.DeployService(slug, nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			if !hasToken(name, newToken) || !hasToken(name+"-2", newToken) {
				t.Fatal("expected both copies to run with the replacement token after retry")
			}
		})
	}
}

func TestLocalDeployKeepsSavedTokenWhenNoCopyStarts(t *testing.T) {
	app := newAppUnderTest(t)
	app.collectors = nil
	const slug, key = "traffmonetizer", "TRAFFMONETIZER_TOKEN"
	const oldToken, newToken = "1|previous-test", "2|replacement-test"
	if _, err := app.DeployService(slug, map[string]string{key: oldToken}, nil, nil); err != nil {
		t.Fatal(err)
	}
	app.Docker.FailCreate("cashpilot-"+slug, "fixture failure starting copy one")
	two := 2
	_, err := app.DeployService(slug, map[string]string{key: newToken}, nil, &two)
	if err == nil || !strings.Contains(err.Error(), "fixture failure starting copy one") {
		t.Fatalf("expected copy one failure, got %v", err)
	}
	if formatBindingError(err) != err.Error() {
		t.Fatal("expected an ordinary failure when no copy started")
	}
	saved, err := app.store.GetCredentials(slug)
	if err != nil {
		t.Fatal(err)
	}
	if saved[key] != oldToken {
		t.Fatal("expected the old token in storage")
	}
}
