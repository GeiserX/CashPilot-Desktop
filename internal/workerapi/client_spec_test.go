package workerapi

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

func service() catalog.Service {
	return catalog.Service{
		Name: "Example", Slug: "example", Status: "active",
		Docker: catalog.DockerConfig{
			Image:       "repo/example:1.2.3@sha256:" + strings.Repeat("a", 64),
			ImageByArch: map[string]string{"arm64": "repo/example:1.2.3-arm64"},
			Env: []catalog.EnvVar{
				{Key: "TOKEN", Label: "API token", Required: true, Secret: true},
				{Key: "DEVICE_NAME", Default: "cashpilot-{hostname}"},
				{Key: "DATA_DIR", Default: "/srv/example"},
				{Key: "PORT", Default: "9000", Pattern: `\d+`},
				{Key: "EMPTY"},
			},
			Ports:       []string{"9000:9000", "127.0.0.1:9001:9001/udp"},
			Volumes:     []string{"${DATA_DIR}:/data", "example-id:/id:ro"},
			Command:     "--token ${TOKEN} --name ${DEVICE_NAME} --keep ${UNKNOWN}",
			CapAdd:      []string{"NET_ADMIN"},
			Devices:     []string{"/dev/net/tun"},
			StopTimeout: 60,
		},
	}
}

func TestTheSpecIsWhatTheWebUISends(t *testing.T) {
	spec, err := BuildDeploySpec(service(), SpecInput{
		Typed:         map[string]string{"TOKEN": "  typed-token  ", "DEVICE_NAME": "cashpilot-mydesk", "DATA_DIR": ""},
		Stored:        map[string]string{"TOKEN": "stored-token", "DATA_DIR": "/mnt/stored"},
		LocalHostname: "mydesk",
		DeviceName:    "cashpilot-77cd8c68",
		WorkerArch:    "x86_64",
	})
	if err != nil {
		t.Fatalf("BuildDeploySpec: %v", err)
	}
	wantEnv := map[string]string{
		"TOKEN": "typed-token", // typed wins, stripped
		// This computer's name is the form's prefill, so the template gets the
		// worker's own name instead, as the web app fills it ("cashpilot-" + name).
		"DEVICE_NAME": "cashpilot-cashpilot-77cd8c68",
		"DATA_DIR":    "/mnt/stored", // a blank box keeps the stored value
		"PORT":        "9000",
		"EMPTY":       "",
	}
	if !reflect.DeepEqual(spec.Env, wantEnv) {
		t.Fatalf("env = %v, want %v", spec.Env, wantEnv)
	}
	if spec.Image != service().Docker.Image {
		t.Fatalf("image = %q", spec.Image)
	}
	wantPorts := map[string]any{"9000/tcp": 9000, "9001/udp": []any{"127.0.0.1", 9001}}
	if !reflect.DeepEqual(spec.Ports, wantPorts) {
		t.Fatalf("ports = %#v", spec.Ports)
	}
	wantVolumes := map[string]Mount{"/mnt/stored": {Bind: "/data", Mode: "rw"}, "example-id": {Bind: "/id", Mode: "ro"}}
	if !reflect.DeepEqual(spec.Volumes, wantVolumes) {
		t.Fatalf("volumes = %v", spec.Volumes)
	}
	if spec.Command == nil || *spec.Command != "--token typed-token --name cashpilot-cashpilot-77cd8c68 --keep ${UNKNOWN}" {
		t.Fatalf("command = %v", spec.Command)
	}
	if spec.Hostname != nil || spec.NetworkMode != nil || spec.Privileged {
		t.Fatalf("hostname %v, network %v, privileged %v", spec.Hostname, spec.NetworkMode, spec.Privileged)
	}
	if !reflect.DeepEqual(spec.CapAdd, []string{"NET_ADMIN"}) || !reflect.DeepEqual(spec.Devices, []string{"/dev/net/tun"}) {
		t.Fatalf("cap_add %v devices %v", spec.CapAdd, spec.Devices)
	}
	if len(spec.MovedMounts) != 0 {
		t.Fatalf("moved = %v: nothing typed names a mount", spec.MovedMounts)
	}
}

func TestATypedPathIsAMove(t *testing.T) {
	spec, err := BuildDeploySpec(service(), SpecInput{Typed: map[string]string{"TOKEN": "t", "DATA_DIR": "/mnt/new"}, DeviceName: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(spec.MovedMounts, []string{"/data"}) {
		t.Fatalf("moved = %v", spec.MovedMounts)
	}
}

func TestAChosenDeviceNameIsKept(t *testing.T) {
	spec, err := BuildDeploySpec(service(), SpecInput{Typed: map[string]string{"TOKEN": "t", "DEVICE_NAME": "garage-box"}, LocalHostname: "mydesk", DeviceName: "cashpilot-x"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Env["DEVICE_NAME"] != "garage-box" {
		t.Fatalf("DEVICE_NAME = %q", spec.Env["DEVICE_NAME"])
	}
}

func TestTheWorkersArchitecturePicksTheImage(t *testing.T) {
	spec, err := BuildDeploySpec(service(), SpecInput{Typed: map[string]string{"TOKEN": "t"}, WorkerArch: "aarch64"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Image != "repo/example:1.2.3-arm64" {
		t.Fatalf("image = %q", spec.Image)
	}
	spec, err = BuildDeploySpec(service(), SpecInput{Typed: map[string]string{"TOKEN": "t"}, WorkerArch: "aarch64", CustomImage: "mine/img:2"})
	if err != nil || spec.Image != "mine/img:2" {
		t.Fatalf("custom image: %q, %v", spec.Image, err)
	}
}

func TestMissingAndInvalidValuesAreRefused(t *testing.T) {
	_, err := BuildDeploySpec(service(), SpecInput{})
	if err == nil || !strings.Contains(err.Error(), "missing required fields: API token") {
		t.Fatalf("err = %v", err)
	}
	_, err = BuildDeploySpec(service(), SpecInput{Typed: map[string]string{"TOKEN": "t", "PORT": "9000 --evil"}})
	if err == nil || !strings.Contains(err.Error(), "invalid value for: PORT") || strings.Contains(err.Error(), "evil") {
		t.Fatalf("err = %v", err)
	}
}

func TestWhatAWorkerCannotRunIsRefused(t *testing.T) {
	for name, edit := range map[string]func(*catalog.Service){
		"custom":  func(s *catalog.Service) { s.Custom = true },
		"manual":  func(s *catalog.Service) { s.ManualOnly = true },
		"noimage": func(s *catalog.Service) { s.Docker.Image = "" },
		"retired": func(s *catalog.Service) { s.Status = "dead" },
		// A wallet seed kept in a file never travels to a worker, stored or typed.
		"seedfile": func(s *catalog.Service) {
			s.Docker.Env = append(s.Docker.Env, catalog.EnvVar{Key: "SEED", Secret: true, SeedFile: &catalog.SeedFile{Path: "/data/seed.yml", Key: "phrase"}})
		},
	} {
		svc := service()
		edit(&svc)
		if _, err := BuildDeploySpec(svc, SpecInput{Typed: map[string]string{"TOKEN": "t"}}); err == nil {
			t.Errorf("%s: deployed", name)
		}
	}
}

func TestAFenceGatedServiceNeedsAWorkerThatCanFenceIt(t *testing.T) {
	svc := service()
	svc.Docker.FenceGated = true
	svc.Docker.NetworkMode = "cashpilot-fenced"
	svc.Docker.UIPort = &catalog.UIPort{Env: "UI", Port: 4449}
	svc.Docker.UDPPortRange = &catalog.UDPPortRange{Env: "UDP"}
	svc.Docker.Env = append(svc.Docker.Env, catalog.EnvVar{Key: "UI", Default: "127.0.0.1,10.0.0.5"}, catalog.EnvVar{Key: "UDP", Default: "50000:50002"})
	in := SpecInput{Typed: map[string]string{"TOKEN": "t"}}
	if _, err := BuildDeploySpec(svc, in); err == nil || !strings.Contains(err.Error(), "too old") {
		t.Fatalf("err = %v", err)
	}
	in.WorkerFeatures = []string{"instances", "fence_gated"}
	spec, err := BuildDeploySpec(svc, in)
	if err != nil {
		t.Fatal(err)
	}
	if spec.NetworkMode == nil || *spec.NetworkMode != "cashpilot-fenced" {
		t.Fatalf("network = %v", spec.NetworkMode)
	}
	if !reflect.DeepEqual(spec.Ports["4449/tcp"], []any{[]any{"127.0.0.1", 4449}, []any{"10.0.0.5", 4449}}) || spec.Ports["50001/udp"] != 50001 {
		t.Fatalf("ports = %#v", spec.Ports)
	}
	in.CustomImage = "mine/myst:1"
	if _, err := BuildDeploySpec(svc, in); err == nil || !strings.Contains(err.Error(), "only the image the catalog pins") {
		t.Fatalf("custom image on a fenced node: %v", err)
	}
}

func TestPickWorker(t *testing.T) {
	if id, err := PickWorker(0, []int64{4}); err != nil || id != 4 {
		t.Fatalf("one online: %d, %v", id, err)
	}
	if _, err := PickWorker(0, nil); !errors.Is(err, ErrNoWorker) {
		t.Fatalf("none online: %v", err)
	}
	if _, err := PickWorker(0, []int64{4, 5}); !errors.Is(err, ErrChooseWorker) {
		t.Fatalf("several online: %v", err)
	}
	if id, err := PickWorker(5, []int64{4, 5}); err != nil || id != 5 {
		t.Fatalf("chosen: %d, %v", id, err)
	}
}

// The device name, the approval target and the signature are compared with what the
// web app's own code computes (app/main.py _device_hostname, app/step_up.py), run on
// the same inputs; a worker refuses anything that differs.
func TestDeviceNameMatchesTheWebApp(t *testing.T) {
	if got := DeviceName("cashpilot", "client-abc", 7); got != "cashpilot-77cd8c68" {
		t.Fatalf("got %q", got)
	}
	if got := DeviceName("", "", 7); got != "cashpilot-9c1df8a1" {
		t.Fatalf("got %q", got)
	}
	if got := DeviceName("My Box!!", "client-abc", 7); got != "my-box-77cd8c68" {
		t.Fatalf("got %q", got)
	}
}

func TestDeployTargetMatchesTheWebApp(t *testing.T) {
	command := "--x ${A}\n"
	env := map[string]string{"TOKEN": "s3crét \"q\" <a&b> \U0001F600", "DEVICE": "cashpilot-1a2b3c4d", "EMPTY": ""}
	volumes := map[string]Mount{"storj-identity": {Bind: "/app/identity", Mode: "rw"}, "/mnt/data/storj": {Bind: "/app/config", Mode: "rw"}}
	got := DeployTarget("storj", "storjlabs/storagenode:v1@sha256:"+strings.Repeat("a", 64), &command, env, volumes)
	want := "storj|storjlabs/storagenode:v1@sha256:" + strings.Repeat("a", 64) + "|57ebc77d59e5fb4dd9a80a4d89b753bf0ca1668527d4b368de7d01b7c7393891"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if got := DeployTarget("x", "img:1", nil, nil, nil); got != "x|img:1|0fd2aa93f4bd130a7383e56df545b1501c8902be52f3ba09507130def55be516" {
		t.Fatalf("empty spec: %s", got)
	}
}

func TestSignMatchesTheWebApp(t *testing.T) {
	got := signWith("worker-key-1", ActionCustomImage, "x|img:1|abc", time.Unix(1700000000, 0), "00112233445566778899aabbccddeeff")
	want := "eyJhIjoiZGVwbG95LWN1c3RvbS1pbWFnZSIsImV4cCI6MTcwMDAwMDMwMCwibiI6IjAwMTEyMjMzNDQ1NTY2Nzc4ODk5YWFiYmNjZGRlZWZmIiwidCI6Inh8aW1nOjF8YWJjIn0.7cee5f8734c4cb9585ed7f512dcdc205de85bc794d064587c2b822c802dd13a2"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if _, err := Sign("", ActionCustomImage, "t", time.Now()); err == nil {
		t.Fatal("no own key, no approval")
	}
}
