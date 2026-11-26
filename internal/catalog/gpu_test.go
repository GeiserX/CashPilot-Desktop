package catalog

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const gpuDigest = "@sha256:3333333333333333333333333333333333333333333333333333333333333333"

func validGPUService() Service {
	return Service{
		Slug:         "gpu-miner",
		Name:         "GPU Miner",
		Requirements: Requirements{GPU: true},
		Docker: DockerConfig{
			Image:   "example/gpu-miner" + gpuDigest,
			Runtime: "runc",
			GPU:     &GPU{Vendor: "nvidia"},
		},
	}
}

func TestGPUBlockDecodesStrictly(t *testing.T) {
	raw := `slug: gpu-miner
name: GPU Miner
requirements:
  gpu: true
docker:
  image: example/gpu-miner` + gpuDigest + `
  runtime: runc
  gpu:
    vendor: amd
    count: all
    rocm: true
`
	dec := yaml.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.KnownFields(true)
	var svc Service
	if err := dec.Decode(&svc); err != nil {
		t.Fatalf("strict decode: %v", err)
	}
	grant, ok := svc.Docker.GPU.Grant()
	if !ok || grant != (GPUGrant{Vendor: "amd", All: true, ROCm: true}) {
		t.Fatalf("grant = %+v %v", grant, ok)
	}
	if problems := GPUProblems(svc); len(problems) != 0 {
		t.Fatalf("a valid block has problems: %v", problems)
	}
}

func TestGPUGrant(t *testing.T) {
	cases := []struct {
		gpu  *GPU
		want GPUGrant
		ok   bool
	}{
		{nil, GPUGrant{}, false},
		{&GPU{Vendor: "nvidia"}, GPUGrant{Vendor: "nvidia"}, true},
		{&GPU{Vendor: "nvidia", Count: 1}, GPUGrant{Vendor: "nvidia"}, true},
		{&GPU{Vendor: "intel", Count: "all"}, GPUGrant{Vendor: "intel", All: true}, true},
		{&GPU{Vendor: "amd", ROCm: false}, GPUGrant{Vendor: "amd"}, true},
		{&GPU{Vendor: "apple"}, GPUGrant{}, false},
		{&GPU{Vendor: "nvidia", Count: 2}, GPUGrant{}, false},
		{&GPU{Vendor: "nvidia", Count: "1"}, GPUGrant{}, false},
		{&GPU{Vendor: "nvidia", Count: 1.0}, GPUGrant{}, false},
		{&GPU{Vendor: "amd", ROCm: "yes"}, GPUGrant{}, false},
		{&GPU{Vendor: "intel", ROCm: true}, GPUGrant{}, false},
	}
	for _, tc := range cases {
		got, ok := tc.gpu.Grant()
		if got != tc.want || ok != tc.ok {
			t.Errorf("%+v.Grant() = %+v %v, want %+v %v", tc.gpu, got, ok, tc.want, tc.ok)
		}
	}
}

// One case per refusal of the web loader (app/gpu.py spec_errors).
func TestGPUProblems(t *testing.T) {
	if problems := GPUProblems(validGPUService()); len(problems) != 0 {
		t.Fatalf("the valid entry has problems: %v", problems)
	}
	cases := map[string]func(*Service){
		"CashPilot sets the NVIDIA_ variables itself": func(s *Service) { s.Docker.Env = []EnvVar{{Key: "nvidia_visible_devices"}} },
		"docker.devices /dev/dri/renderD128":          func(s *Service) { s.Docker.Devices = []string{"/dev/dri/renderD128:/dev/dri/renderD128"} },
		"docker.devices /dev/dri:":                    func(s *Service) { s.Docker.Devices = []string{"/dev/dri/"} },
		"docker.devices /dev/kfd":                     func(s *Service) { s.Docker.Devices = []string{"/dev/kfd"} },
		"vendor must be one of":                       func(s *Service) { s.Docker.GPU.Vendor = "apple" },
		`count must be 1 or "all"`:                    func(s *Service) { s.Docker.GPU.Count = 4 },
		"rocm must be true or false":                  func(s *Service) { s.Docker.GPU.ROCm = "no" },
		"rocm is for vendor amd only":                 func(s *Service) { s.Docker.GPU.ROCm = true },
		"needs requirements.gpu: true":                func(s *Service) { s.Requirements.GPU = false },
		"pinned by digest":                            func(s *Service) { s.Docker.Image = "example/gpu-miner:1.0" },
		"each architecture alternative":               func(s *Service) { s.Docker.ImageByArch = map[string]string{"arm64": "example/gpu-miner:arm"} },
		"docker.runtime: runc":                        func(s *Service) { s.Docker.Runtime = "runsc" },
		"network_mode: host":                          func(s *Service) { s.Docker.NetworkMode = "HOST" },
	}
	for want, mutate := range cases {
		svc := validGPUService()
		mutate(&svc)
		want = strings.TrimSuffix(want, ":")
		got := strings.Join(GPUProblems(svc), " | ")
		if !strings.Contains(got, want) {
			t.Errorf("GPUProblems = %q, want it to mention %q", got, want)
		}
	}
	// The env and device rules hold for an entry without docker.gpu too.
	plain := Service{Docker: DockerConfig{Devices: []string{"/dev/kfd"}, Env: []EnvVar{{Key: "NVIDIA_DRIVER_CAPABILITIES"}}}}
	if got := GPUProblems(plain); len(got) != 2 {
		t.Fatalf("GPUProblems(plain) = %v, want the device and the env key", got)
	}
}

// THE RULE: every vendored entry passes the web catalog's GPU rules, as the web
// loader requires. A GPU entry that breaks one would be refused at every deploy.
func TestEveryVendoredEntryPassesTheGPURules(t *testing.T) {
	cat, err := LoadEmbedded(os.DirFS(filepath.Join("..", "..")))
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	checked := 0
	for _, svc := range cat.List() {
		checked++
		if problems := GPUProblems(svc); len(problems) > 0 {
			t.Errorf("%s: %s", svc.Slug, strings.Join(problems, "; "))
		}
		if svc.Docker.GPU != nil {
			if _, ok := svc.Docker.GPU.Grant(); !ok {
				t.Errorf("%s: docker.gpu grants nothing", svc.Slug)
			}
		}
	}
	if checked < 20 {
		t.Fatalf("checked only %d entries; the catalog did not load", checked)
	}
}

// The heartbeat's gpu_grants must equal the web's spec_of for the same entry, or
// the server refuses every GPU deploy to this computer.
func TestGPUGrantsIsTheWebShape(t *testing.T) {
	nvidia := validGPUService()
	amd := validGPUService()
	amd.Slug = "gpu-amd"
	amd.Docker.GPU = &GPU{Vendor: "amd", Count: "all", ROCm: true}
	custom := validGPUService()
	custom.Slug = "custom-gpu"
	saved := validGPUService()
	saved.Slug = "saved-gpu"
	saved.Custom = true
	broken := validGPUService()
	broken.Slug = "gpu-broken"
	broken.Docker.Runtime = ""
	plain := Service{Slug: "plain", Name: "Plain"}
	got, err := json.Marshal(GPUGrants([]Service{nvidia, amd, custom, saved, broken, plain}))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"gpu-amd":{"vendor":"amd","count":"all","rocm":true},"gpu-miner":{"vendor":"nvidia","count":1,"rocm":false}}`
	if string(got) != want {
		t.Fatalf("GPUGrants = %s, want %s", got, want)
	}
	if empty, _ := json.Marshal(GPUGrants(nil)); string(empty) != "{}" {
		t.Fatalf("no GPU entry: %s, want {} as the web worker sends", empty)
	}
}
