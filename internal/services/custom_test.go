package services

import (
	"context"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

func validCustom() CustomDefinition {
	return CustomDefinition{Name: "My node", Image: "publisher/node:1.4.2"}
}

func TestValidateCustomImage(t *testing.T) {
	for _, bad := range []string{"", "--privileged", "https://ghcr.io/a/b", "Org/Cli", "a/b:1 --rm"} {
		in := validCustom()
		in.Image = bad
		if _, _, err := ValidateCustom(in, nil); err == nil {
			t.Errorf("image %q accepted, want refused", bad)
		}
	}
	for image, warn := range map[string]bool{
		"busybox":                 true,
		"busybox:latest":          true,
		"ghcr.io/org/node:1.4.2":  false,
		"localhost:5000/org/node": true,
		"org/node@sha256:" + strings.Repeat("a", 64):        false,
		"org/node:latest@sha256:" + strings.Repeat("b", 64): false,
	} {
		in := validCustom()
		in.Image = image
		_, warnings, err := ValidateCustom(in, nil)
		if err != nil {
			t.Errorf("image %q refused: %v", image, err)
			continue
		}
		if got := len(warnings) == 1 && warnings[0] == CustomUnpinnedWarning; got != warn {
			t.Errorf("image %q: unpinned warning = %v, want %v", image, got, warn)
		}
	}
}

func TestValidateCustomFields(t *testing.T) {
	cases := map[string]func(*CustomDefinition){
		"empty name":         func(d *CustomDefinition) { d.Name = "  " },
		"long name":          func(d *CustomDefinition) { d.Name = strings.Repeat("n", 61) },
		"control in name":    func(d *CustomDefinition) { d.Name = "a\x07b" },
		"bad env key":        func(d *CustomDefinition) { d.Env = []CustomEnv{{Key: "1BAD", Value: "x"}} },
		"env key with dash":  func(d *CustomDefinition) { d.Env = []CustomEnv{{Key: "A-B", Value: "x"}} },
		"duplicate env key":  func(d *CustomDefinition) { d.Env = []CustomEnv{{Key: "A", Value: "1"}, {Key: "A", Value: "2"}} },
		"NUL in value":       func(d *CustomDefinition) { d.Env = []CustomEnv{{Key: "A", Value: "x\x00y"}} },
		"too many variables": func(d *CustomDefinition) { d.Env = make([]CustomEnv, 51) },
		"multi-line command": func(d *CustomDefinition) { d.Command = "run\n--evil" },
		"relative data path": func(d *CustomDefinition) { d.DataPath = "data" },
		"root data path":     func(d *CustomDefinition) { d.DataPath = "/" },
		"dot-dot data path":  func(d *CustomDefinition) { d.DataPath = "/data/../etc" },
		"unclean data path":  func(d *CustomDefinition) { d.DataPath = "/data/" },
		"colon in data path": func(d *CustomDefinition) { d.DataPath = "/data:/host" },
		"memory unit":        func(d *CustomDefinition) { d.MemLimit = "512mb" },
		"memory too small":   func(d *CustomDefinition) { d.MemLimit = "16m" },
		"memory too large":   func(d *CustomDefinition) { d.MemLimit = "17g" },
	}
	for name, mutate := range cases {
		in := validCustom()
		mutate(&in)
		if _, _, err := ValidateCustom(in, nil); err == nil {
			t.Errorf("%s: accepted, want refused", name)
		}
	}
	got, _, err := ValidateCustom(CustomDefinition{
		Name: " Node ", Image: " org/node:1 ", Command: " run --flag ${TOKEN} ", DataPath: "/data", MemLimit: "2G",
		Env: []CustomEnv{{Key: " TOKEN ", Value: "abc", Secret: true}},
	}, nil)
	if err != nil {
		t.Fatalf("valid definition refused: %v", err)
	}
	if got.Name != "Node" || got.Image != "org/node:1" || got.Command != "run --flag ${TOKEN}" || got.MemLimit != "2g" ||
		len(got.Env) != 1 || got.Env[0].Key != "TOKEN" {
		t.Fatalf("normalised definition = %+v", got)
	}
	if def, _, _ := ValidateCustom(validCustom(), nil); def.MemLimit != CustomDefaultMemLimit {
		t.Fatalf("default memory limit = %q, want %q", def.MemLimit, CustomDefaultMemLimit)
	}
}

// A stored secret is never sent back to the UI, so a blank box on an edit keeps it;
// a typed value replaces it, and a blank plain value stays blank.
func TestValidateCustomKeepsABlankSecret(t *testing.T) {
	previous := CustomDefinition{Env: []CustomEnv{{Key: "TOKEN", Value: "s3cret", Secret: true}, {Key: "PLAIN", Value: "p"}}}
	in := validCustom()
	in.Env = []CustomEnv{{Key: "TOKEN", Value: "", Secret: true}, {Key: "PLAIN", Value: ""}}
	got, _, err := ValidateCustom(in, &previous)
	if err != nil {
		t.Fatal(err)
	}
	if got.Env[0].Value != "s3cret" {
		t.Fatalf("blank secret on edit = %q, want the stored one", got.Env[0].Value)
	}
	if got.Env[1].Value != "" {
		t.Fatalf("blank plain value = %q, want blank", got.Env[1].Value)
	}
	in.Env[0].Value = "new"
	if got, _, _ = ValidateCustom(in, &previous); got.Env[0].Value != "new" {
		t.Fatalf("typed secret = %q, want the new one", got.Env[0].Value)
	}
}

// Copy N of a service runs as cashpilot-<slug>-N, so no slug may end in -<digits>,
// and a slug ever used (deleted included) is never handed out again.
func TestMakeCustomSlug(t *testing.T) {
	taken := map[string]bool{}
	var got []string
	for _, name := range []string{"Foo", "Foo", "Foo 2", "Node-12", "!!!", strings.Repeat("x", 80)} {
		slug := MakeCustomSlug(name, taken)
		taken[slug] = true
		got = append(got, slug)
	}
	want := []string{"custom-foo", "custom-foo-v2", "custom-foo2", "custom-node12", "custom-service", "custom-" + strings.Repeat("x", 40)}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("slugs = %v, want %v", got, want)
	}
}

// The catalog-shaped service grants nothing a catalog entry could: no ports,
// capabilities, devices, network mode, privilege, entrypoint or host path; its only
// mount is its own named volume, and it always carries a memory limit.
func TestCustomAsServiceGrantsNothing(t *testing.T) {
	def := CustomDefinition{Name: "N", Image: "org/n:1", Command: "run ${TOKEN}", DataPath: "/data", MemLimit: "1g",
		Env: []CustomEnv{{Key: "TOKEN", Value: "s3cret", Secret: true}, {Key: "MODE", Value: "x"}}}
	for _, reveal := range []bool{true, false} {
		svc := CustomAsService("custom-n", def, reveal)
		dc := svc.Docker
		if !svc.Custom || svc.Category != "custom" || svc.ManualOnly {
			t.Fatalf("reveal=%v: not marked custom: %+v", reveal, svc)
		}
		if len(dc.Ports) != 0 || len(dc.CapAdd) != 0 || len(dc.Devices) != 0 || dc.NetworkMode != "" || dc.Privileged ||
			len(dc.Entrypoint) != 0 || len(dc.ImageByArch) != 0 || svc.HasNative() {
			t.Fatalf("reveal=%v: grants something: %+v", reveal, dc)
		}
		if strings.Join(dc.Volumes, ",") != "cashpilot-custom-n-data:/data" {
			t.Fatalf("reveal=%v: volumes = %v, want only its named volume", reveal, dc.Volumes)
		}
		if dc.Resources.MemLimit != "1g" {
			t.Fatalf("reveal=%v: mem limit = %q", reveal, dc.Resources.MemLimit)
		}
	}
	masked := CustomAsService("custom-n", def, false)
	if len(masked.Docker.Env) != 0 || masked.Docker.Command != "" {
		t.Fatalf("the masked service carries env or the command: %+v", masked.Docker)
	}
	revealed := CustomAsService("custom-n", def, true)
	if len(revealed.Docker.Env) != 2 || revealed.Docker.Env[0].Default != "s3cret" || revealed.Docker.Command != "run ${TOKEN}" {
		t.Fatalf("the revealed service lost env or command: %+v", revealed.Docker)
	}
	if v := CustomAsService("custom-n", CustomDefinition{Name: "N", Image: "a/b:1"}, true); len(v.Docker.Volumes) != 0 || v.Docker.Resources.MemLimit != CustomDefaultMemLimit {
		t.Fatalf("no data path: %+v", v.Docker)
	}
}

func newCustomManager(t *testing.T) (*Manager, *fakeProvider, *store.Store) {
	t.Helper()
	st := newTestStore(t)
	fake := &fakeProvider{deployResult: runtime.ContainerInfo{ContainerID: "c1", Name: "cashpilot-custom-my-node", Status: "running", Image: "publisher/node:1.4.2"}}
	return NewManager(fake, newTestCatalog(t), st), fake, st
}

// The full path: create, list (secrets masked), deploy (the saved definition reaches
// the runtime, the form's credentials and Image box do not), edit (blank secret kept),
// delete refused while deployed, then allowed, and the slug never reused.
func TestCustomServiceLifecycle(t *testing.T) {
	m, fake, st := newCustomManager(t)
	ctx := context.Background()
	view, err := m.SaveCustom("", CustomDefinition{
		Name: "My node", Image: "publisher/node:1.4.2", Command: "run --token ${TOKEN}", DataPath: "/data",
		Env: []CustomEnv{{Key: "TOKEN", Value: "s3cret", Secret: true}, {Key: "MODE", Value: "fast"}},
	})
	if err != nil {
		t.Fatalf("SaveCustom create: %v", err)
	}
	if view.Slug != "custom-my-node" || view.Volume != "cashpilot-custom-my-node-data" {
		t.Fatalf("created view = %+v", view)
	}
	views, err := m.ListCustom()
	if err != nil || len(views) != 1 {
		t.Fatalf("ListCustom = %v, %v", views, err)
	}
	if env := views[0].Env; env[0].Value != "" || !env[0].IsSet || env[1].Value != "fast" {
		t.Fatalf("listed env = %+v, want the secret masked and the plain value shown", env)
	}

	// The overlay finds it by slug, masked.
	svc, ok := m.catalog.Get("custom-my-node")
	if !ok || !svc.Custom || len(svc.Docker.Env) != 0 || svc.Docker.Command != "" {
		t.Fatalf("catalog overlay = %+v, %v", svc, ok)
	}

	if _, err := m.DeployImage(ctx, "custom-my-node", nil, strPtr("evil/image:1")); err == nil {
		t.Fatal("a deploy-form image was accepted for a custom service")
	}
	if fake.deployCalls != 0 {
		t.Fatal("the refused deploy reached the runtime")
	}
	dep, err := m.DeployWith(ctx, "custom-my-node", map[string]string{"TOKEN": "typed", "EXTRA": "x"}, DeployOptions{})
	if err != nil {
		t.Fatalf("DeployWith: %v", err)
	}
	if dep.CustomImage != "" {
		t.Fatalf("deployment records custom image %q", dep.CustomImage)
	}
	spec := fake.deploySpecs[0]
	if spec.Service.Docker.Image != "publisher/node:1.4.2" || spec.Service.Docker.Command != "run --token ${TOKEN}" {
		t.Fatalf("runtime got %+v", spec.Service.Docker)
	}
	if len(spec.Env) != 0 {
		t.Fatalf("form credentials reached the runtime: %v", spec.Env)
	}
	env := map[string]string{}
	for _, e := range spec.Service.Docker.Env {
		env[e.Key] = e.Default
	}
	if env["TOKEN"] != "s3cret" || env["MODE"] != "fast" || len(env) != 2 {
		t.Fatalf("runtime env = %v, want the saved definition", env)
	}

	// Edit with the secret left blank: kept.
	if _, err := m.SaveCustom("custom-my-node", CustomDefinition{
		Name: "Renamed", Image: "publisher/node:1.5.0", Env: []CustomEnv{{Key: "TOKEN", Secret: true}},
	}); err != nil {
		t.Fatalf("SaveCustom edit: %v", err)
	}
	if svc, _ := m.catalog.Get("custom-my-node"); svc.Name != "Renamed" || svc.Docker.Image != "publisher/node:1.5.0" {
		t.Fatalf("overlay after edit = %+v", svc)
	}
	revealed, _ := m.revealCustom("custom-my-node")
	if revealed.Docker.Env[0].Default != "s3cret" {
		t.Fatalf("secret after a blank edit = %q", revealed.Docker.Env[0].Default)
	}

	if err := m.DeleteCustom(ctx, "custom-my-node"); err == nil {
		t.Fatal("delete accepted while deployed")
	}

	// Removing it with its data needs only the user's "delete its data" answer: the
	// overlay knows the service and declares nothing irreplaceable, so its volume is
	// not treated as the unknown data of a service that left the catalog.
	if err := m.Remove(ctx, "custom-my-node", true, false); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if opts := fake.removeOpts[len(fake.removeOpts)-1]; opts.Critical == nil || len(opts.Critical) != 0 || !opts.DeleteData {
		t.Fatalf("Remove options = %+v, want DeleteData and an empty, known critical list", opts)
	}
	if _, found, _ := st.GetDeployment("custom-my-node"); found {
		t.Fatal("the deployment row survived Remove")
	}
	fake.listResult = []runtime.ContainerInfo{{Slug: "custom-my-node", Instance: 1}}
	if err := m.DeleteCustom(ctx, "custom-my-node"); err == nil {
		t.Fatal("delete accepted while a container of it exists")
	}
	fake.listResult = nil
	if err := m.DeleteCustom(ctx, "custom-my-node"); err != nil {
		t.Fatalf("DeleteCustom: %v", err)
	}
	if _, ok := m.catalog.Get("custom-my-node"); ok {
		t.Fatal("deleted service still in the catalog overlay")
	}
	again, err := m.SaveCustom("", CustomDefinition{Name: "My node", Image: "publisher/node:1.4.2"})
	if err != nil {
		t.Fatal(err)
	}
	if again.Slug != "custom-my-node-v2" {
		t.Fatalf("recreated slug = %q, want a fresh one, never the deleted service's", again.Slug)
	}
	if _, err := m.SaveCustom("custom-my-node", validCustom()); err == nil {
		t.Fatal("an edit of a deleted service was accepted")
	}
	if _, err := m.SaveCustom("example", validCustom()); err == nil {
		t.Fatal("an edit of a catalog slug was accepted")
	}
}

// A custom service with a data path keeps it in a volume two copies would share, so
// it runs once; one without can run several.
func TestCustomServiceScaling(t *testing.T) {
	m, _, _ := newCustomManager(t)
	with := CustomAsService("custom-a", CustomDefinition{Name: "A", Image: "a/a:1", DataPath: "/data"}, false)
	without := CustomAsService("custom-b", CustomDefinition{Name: "B", Image: "b/b:1"}, false)
	if m.ScalingLimit(with).Max != 1 || m.ScalingLimit(without).Max != MaxInstances {
		t.Fatalf("scaling: with data %d, without %d", m.ScalingLimit(with).Max, m.ScalingLimit(without).Max)
	}
}

// THE RULE: a memory limit past 16g is refused however many digits it has. Before
// the bound, 18014398509481985g wrapped to 1024 MB when multiplied and passed. 16g
// itself is the positive control.
func TestValidateCustomMemRefusesAnOverflowingLimit(t *testing.T) {
	for _, raw := range []string{"18014398509481985g", "17g", "16385m", "99999999999999999999g"} {
		if got, err := validateCustomMem(raw); err == nil {
			t.Errorf("validateCustomMem(%q) = %q, want a refusal", raw, got)
		}
	}
	for _, raw := range []string{"16g", "16384m", "32m"} {
		if _, err := validateCustomMem(raw); err != nil {
			t.Errorf("validateCustomMem(%q): %v", raw, err)
		}
	}
}

// THE RULE: a custom service cannot be deleted while it is being deployed. Before,
// a delete in the deploy window found no deployment row and no container, removed
// the definition, and the deploy then recorded a container nothing could redeploy.
// The delete after the deploy is the control: it is refused for the usual reason,
// the deployment row, not the in-flight mark.
func TestDeleteCustomRefusedWhileItsDeployRuns(t *testing.T) {
	m, fake, _ := newCustomManager(t)
	ctx := context.Background()
	view, err := m.SaveCustom("", validCustom())
	if err != nil {
		t.Fatalf("SaveCustom: %v", err)
	}
	var during error
	fake.duringDeploy = func() { during = m.DeleteCustom(ctx, view.Slug) }
	if _, err := m.DeployWith(ctx, view.Slug, nil, DeployOptions{}); err != nil {
		t.Fatalf("DeployWith: %v", err)
	}
	if during == nil {
		t.Fatal("the definition was deleted while its deploy was running")
	}
	if _, ok, err := m.customDefinition(view.Slug); err != nil || !ok {
		t.Fatalf("definition after the deploy: ok=%v err=%v", ok, err)
	}
	fake.duringDeploy = nil
	if err := m.DeleteCustom(ctx, view.Slug); err == nil || !strings.Contains(err.Error(), "still deployed here") {
		t.Fatalf("delete after the deploy = %v, want the deployed-here refusal", err)
	}
}
