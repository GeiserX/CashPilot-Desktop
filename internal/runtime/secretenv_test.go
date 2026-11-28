package runtime

import (
	"archive/tar"
	"bytes"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/secretshim"
	"github.com/moby/moby/api/types/container"
)

const testShim = "#!fake start program"

func secretsOn(t *testing.T) {
	t.Helper()
	t.Setenv(EarnerSecretsEnv, "file")
	old := shimFor
	shimFor = func(family string) []byte {
		if family == "amd64" || family == "arm64" {
			return []byte(testShim)
		}
		return nil
	}
	t.Cleanup(func() { shimFor = old })
}

func honeygain() catalog.Service {
	return catalog.Service{Name: "Honeygain", Slug: "honeygain", Docker: catalog.DockerConfig{
		Image:   "honeygain/honeygain",
		Command: "-tou-accept -email ${HONEYGAIN_EMAIL} -pass ${HONEYGAIN_PASSWORD} -device ${HONEYGAIN_DEVICE_NAME}",
		Env: []catalog.EnvVar{
			{Key: "HONEYGAIN_EMAIL"},
			{Key: "HONEYGAIN_PASSWORD", Secret: true},
			{Key: "HONEYGAIN_DEVICE_NAME"},
		},
	}}
}

func honeygainEnv() map[string]string {
	return map[string]string{"HONEYGAIN_EMAIL": "me@example.com", "HONEYGAIN_PASSWORD": "p@ss word", "HONEYGAIN_DEVICE_NAME": "box"}
}

func honeygainPlan(t *testing.T, user string) *secretPlan {
	t.Helper()
	svc, env := honeygain(), honeygainEnv()
	cmd := buildCommandArgs(svc.Docker.Command, env)
	return planSecrets(svc, "honeygain", env, cmd, nil, imageRun{Entrypoint: []string{"./honeygain"}, User: user}, "amd64")
}

func TestNothingChangesUntilTheOwnerTurnsItOn(t *testing.T) {
	old := shimFor
	shimFor = func(string) []byte { return []byte(testShim) }
	t.Cleanup(func() { shimFor = old })
	for _, value := range []string{"", "env", "files", "1"} {
		t.Setenv(EarnerSecretsEnv, value)
		if p := honeygainPlan(t, ""); p != nil {
			t.Fatalf("%s=%q made a plan", EarnerSecretsEnv, value)
		}
	}
	t.Setenv(EarnerSecretsEnv, " FILE ")
	if honeygainPlan(t, "") == nil {
		t.Fatal("file (any case) is the setting")
	}
}

func TestThePasswordLeavesTheConfigAndTheEarnerGetsTheSameArguments(t *testing.T) {
	secretsOn(t)
	p := honeygainPlan(t, "appuser")
	if p == nil {
		t.Fatal("no plan")
	}
	cfg := &container.Config{Labels: map[string]string{LabelService: "honeygain"}}
	p.apply(cfg)
	for _, item := range append(slices.Clone(cfg.Env), append(cfg.Entrypoint, cfg.Cmd...)...) {
		if strings.Contains(item, "p@ss word") {
			t.Fatalf("the password is still in the config: %q", item)
		}
	}
	if !slices.Equal(cfg.Entrypoint, []string{secretShimPath}) {
		t.Fatalf("entrypoint %v", cfg.Entrypoint)
	}
	want := []string{"./honeygain", "-tou-accept", "-email", "me@example.com", "-pass", "p@ss word", "-device", "box"}
	vars, err := secretshim.Parse(p.file)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(cfg.Cmd))
	for i, arg := range cfg.Cmd {
		got[i] = secretshim.Expand(arg, vars)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("the earner would run %q, not %q", got, want)
	}
	if !slices.Contains(cfg.Env, "HONEYGAIN_EMAIL=me@example.com") || len(cfg.Env) != 2 {
		t.Fatalf("env %v", cfg.Env)
	}
	if cfg.Labels[labelSecretKeys] != "HONEYGAIN_PASSWORD" || cfg.Labels[labelSecretEntrypoint] != "1" || cfg.Labels[LabelService] != "honeygain" {
		t.Fatalf("labels %v", cfg.Labels)
	}
}

func TestTheShapeIsTheEarnersOwn(t *testing.T) {
	secretsOn(t)
	svc, env := honeygain(), honeygainEnv()
	cmd := buildCommandArgs(svc.Docker.Command, env)
	plain := &container.Config{Env: envSlice(env), Cmd: cmd, Labels: map[string]string{}}
	shimmed := &container.Config{Env: envSlice(env), Cmd: cmd, Labels: map[string]string{}}
	honeygainPlan(t, "").apply(shimmed)
	a, b := shapeOf(container.InspectResponse{Config: plain}), shapeOf(container.InspectResponse{Config: shimmed})
	if !slices.Equal(a.EnvKeys, b.EnvKeys) || !slices.Equal(a.Entrypoint, nil) {
		t.Fatalf("env keys %v vs %v, entrypoint %v", a.EnvKeys, b.EnvKeys, a.Entrypoint)
	}
	// The catalog sets no entrypoint, so the earner's (the image's ./honeygain) is
	// reported as its own, as Docker reports an image entrypoint on a plain container.
	if !slices.Equal(b.Entrypoint, []string{"./honeygain"}) {
		t.Fatalf("entrypoint %v", b.Entrypoint)
	}
	if drift := ShapeDrift(svc, b, ""); len(drift) > 0 {
		t.Fatalf("drift on a shimmed earner: %v", drift)
	}
}

func TestTheFilesBelongToTheEarnersUser(t *testing.T) {
	secretsOn(t)
	for user, want := range map[string]struct {
		uid  int
		mode int64
	}{
		"":          {0, 0o400},
		"root":      {0, 0o400},
		"0:0":       {0, 0o400},
		"1000":      {1000, 0o400},
		"1000:1000": {1000, 0o400},
		"appuser":   {0, 0o444},
	} {
		p := honeygainPlan(t, user)
		files := map[string]*tar.Header{}
		tr := tar.NewReader(bytes.NewReader(p.archive()))
		var env []byte
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			files[h.Name] = h
			if h.Name == "run/cashpilot-secret/env" {
				env, _ = io.ReadAll(tr)
			}
		}
		if len(files) != 3 || files["run/"] != nil {
			t.Fatalf("%q: entries %v; the image's /run must keep its own owner and mode", user, files)
		}
		e, s := files["run/cashpilot-secret/env"], files["run/cashpilot-secret/cashpilot-env"]
		if e.Uid != want.uid || e.Mode != want.mode || s.Mode != 0o555 || s.Uid != 0 {
			t.Fatalf("%q: env %d %o, program %d %o", user, e.Uid, e.Mode, s.Uid, s.Mode)
		}
		if string(env) != "HONEYGAIN_PASSWORD=p@ss word\x00" {
			t.Fatalf("%q: env file %q", user, env)
		}
	}
}

func TestTheEarnerKeepsItsEnvironmentWhenThePlanCannotWork(t *testing.T) {
	secretsOn(t)
	run := imageRun{Entrypoint: []string{"./honeygain"}}
	cases := map[string]func(svc *catalog.Service, env map[string]string, cmd *[]string, entrypoint *[]string, family *string){
		"custom service": func(svc *catalog.Service, _ map[string]string, _ *[]string, _ *[]string, _ *string) {
			svc.Custom = true
		},
		"opted out": func(svc *catalog.Service, _ map[string]string, _ *[]string, _ *[]string, _ *string) {
			f := false
			svc.Docker.SecretsInFile = &f
		},
		"no secret value": func(_ *catalog.Service, env map[string]string, _ *[]string, _ *[]string, _ *string) {
			env["HONEYGAIN_PASSWORD"] = ""
		},
		"no program for this machine": func(_ *catalog.Service, _ map[string]string, _ *[]string, _ *[]string, family *string) {
			*family = "arm"
		},
		"a NUL in the value": func(_ *catalog.Service, env map[string]string, _ *[]string, _ *[]string, _ *string) {
			env["HONEYGAIN_PASSWORD"] = "a\x00b"
		},
		"the command already holds the placeholder": func(_ *catalog.Service, _ map[string]string, cmd *[]string, _ *[]string, _ *string) {
			*cmd = append(*cmd, "${HONEYGAIN_PASSWORD}")
		},
		"the entrypoint holds the password": func(_ *catalog.Service, _ map[string]string, _ *[]string, entrypoint *[]string, _ *string) {
			*entrypoint = []string{"/run", "p@ss word"}
		},
		"another variable holds the password": func(_ *catalog.Service, env map[string]string, _ *[]string, _ *[]string, _ *string) {
			env["HONEYGAIN_DEVICE_NAME"] = "box-p@ss word"
		},
	}
	for name, change := range cases {
		svc, env := honeygain(), honeygainEnv()
		cmd := buildCommandArgs(svc.Docker.Command, env)
		var entrypoint []string
		family := "amd64"
		change(&svc, env, &cmd, &entrypoint, &family)
		slug := "honeygain"
		if svc.Custom {
			slug = catalog.CustomPrefix + "honeygain"
		}
		if p := planSecrets(svc, slug, env, cmd, entrypoint, run, family); p != nil {
			t.Errorf("%s: planned %v", name, p.cmd)
		}
	}
	// The positive control: unchanged, it plans.
	svc, env := honeygain(), honeygainEnv()
	if planSecrets(svc, "honeygain", env, buildCommandArgs(svc.Docker.Command, env), nil, run, "amd64") == nil {
		t.Fatal("the unchanged case made no plan, so the refusals above prove nothing")
	}
}

func TestTheImageCommandIsKeptWhenTheCatalogSetsNone(t *testing.T) {
	secretsOn(t)
	svc := catalog.Service{Slug: "repocket", Docker: catalog.DockerConfig{Env: []catalog.EnvVar{{Key: "RP_API_KEY", Secret: true}}}}
	env := map[string]string{"RP_API_KEY": "0f3c9a1e-5b2d-4c6e-9a7f-1d2e3f4a5b6c"}
	run := imageRun{Entrypoint: []string{"/usr/local/bin/repocket"}, Cmd: []string{"--serve"}}
	p := planSecrets(svc, "repocket", env, nil, nil, run, "arm64")
	if p == nil || !slices.Equal(p.cmd, []string{"/usr/local/bin/repocket", "--serve"}) || p.entrypointLen != 1 || len(p.env) != 0 {
		t.Fatalf("%+v", p)
	}
	// With a catalog entrypoint Docker drops the image's CMD, so the plan does too.
	p = planSecrets(svc, "repocket", env, nil, []string{"/bin/run"}, run, "arm64")
	if p == nil || !slices.Equal(p.cmd, []string{"/bin/run"}) {
		t.Fatalf("%+v", p)
	}
}

func TestTheHeartbeatReportsTheSetting(t *testing.T) {
	old := shimFor
	t.Cleanup(func() { shimFor = old })
	shimFor = func(string) []byte { return nil }
	t.Setenv(EarnerSecretsEnv, "file")
	if got := EarnerSecretsReport(); got != "unavailable" {
		t.Fatalf("a build without the program: %q", got)
	}
	shimFor = func(string) []byte { return []byte(testShim) }
	if got := EarnerSecretsReport(); got != "file" {
		t.Fatalf("%q", got)
	}
	t.Setenv(EarnerSecretsEnv, "")
	if got := EarnerSecretsReport(); got != "env" {
		t.Fatalf("%q", got)
	}
}
