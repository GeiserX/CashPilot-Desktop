package catalog

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// GPU is a catalog entry's docker.gpu block: the GPU the deploy passes in (web
// app/gpu.py, services/_schema.yml).
//
//	docker:
//	  runtime: runc
//	  gpu:
//	    vendor: nvidia     # nvidia | amd | intel
//	    count: 1           # 1 (default) | "all"
//	    rocm: false        # amd only: also pass /dev/kfd
//
// Count and ROCm are read as any so a wrong type is a reason the deploy refuses,
// not a YAML error that would stop the whole catalog from loading.
//
// The grant comes from this computer's own catalog, never from a deploy request:
// a custom service never gets one, and a proxied copy is refused one.
type GPU struct {
	Vendor string `json:"vendor" yaml:"vendor"`
	Count  any    `json:"count,omitempty" yaml:"count"`
	ROCm   any    `json:"rocm,omitempty" yaml:"rocm"`
}

// GPUGrant is a docker.gpu block that passed the web catalog's rules.
type GPUGrant struct {
	Vendor string
	// All is count "all"; otherwise one GPU.
	All  bool
	ROCm bool
}

// GPU vendors the catalog may name, in the order a refusal lists them.
var GPUVendors = []string{"nvidia", "amd", "intel"}

// GPUEnvPrefix starts the env keys CashPilot sets itself. A catalog entry, a custom
// service or a deploy request may not set one: every container without an NVIDIA
// grant gets NVIDIA_VISIBLE_DEVICES=void, and a request could undo that.
const GPUEnvPrefix = "NVIDIA_"

// KFDDevice is AMD's compute interface, which ROCm needs.
const KFDDevice = "/dev/kfd"

func gpuCountAll(count any) (all, ok bool) {
	switch c := count.(type) {
	case nil:
		return false, true
	case int:
		return false, c == 1
	case string:
		return c == "all", c == "all"
	}
	return false, false
}

// Grant is the block as a GPUGrant, or false when it is absent or breaks a rule of
// its own. A guess here would hand out a GPU, so a bad block grants nothing.
func (g *GPU) Grant() (GPUGrant, bool) {
	if g == nil || !slices.Contains(GPUVendors, g.Vendor) {
		return GPUGrant{}, false
	}
	all, ok := gpuCountAll(g.Count)
	if !ok {
		return GPUGrant{}, false
	}
	rocm, isBool := g.ROCm.(bool)
	if g.ROCm != nil && !isBool {
		return GPUGrant{}, false
	}
	if rocm && g.Vendor != "amd" {
		return GPUGrant{}, false
	}
	return GPUGrant{Vendor: g.Vendor, All: all, ROCm: rocm}, true
}

// GPUSpec is a grant in the web catalog's own shape (app/gpu.py spec_of): count
// is 1 or "all". A CashPilot server compares the heartbeat's gpu_grants with its
// own catalog and deploys a GPU entry to this computer only when they match.
type GPUSpec struct {
	Vendor string `json:"vendor"`
	Count  any    `json:"count"`
	ROCm   bool   `json:"rocm"`
}

// GPUGrants is the grant of every catalog entry this computer would honour, by
// slug: never a custom service, never a block that breaks a rule.
func GPUGrants(services []Service) map[string]GPUSpec {
	out := map[string]GPUSpec{}
	for _, svc := range services {
		if svc.Custom || IsCustomSlug(svc.Slug) {
			continue
		}
		grant, ok := svc.Docker.GPU.Grant()
		if !ok || len(GPUProblems(svc)) > 0 {
			continue
		}
		spec := GPUSpec{Vendor: grant.Vendor, Count: 1, ROCm: grant.ROCm}
		if grant.All {
			spec.Count = "all"
		}
		out[svc.Slug] = spec
	}
	return out
}

// GPUProblems is what is wrong with svc's docker.gpu and the keys around it, by the
// web catalog's rules (app/gpu.py spec_errors). The vendored catalog is held to it
// by a test, and a deploy of a GPU entry refuses on any of it.
func GPUProblems(svc Service) []string {
	dc := svc.Docker
	var problems []string
	for _, item := range dc.Env {
		if strings.HasPrefix(strings.ToUpper(item.Key), GPUEnvPrefix) {
			problems = append(problems, fmt.Sprintf("docker.env key %s: CashPilot sets the %s variables itself", item.Key, GPUEnvPrefix))
		}
	}
	for _, raw := range dc.Devices {
		path := strings.TrimRight(strings.SplitN(strings.TrimSpace(raw), ":", 2)[0], "/")
		if path == KFDDevice || path == "/dev/dri" || strings.HasPrefix(path, "/dev/dri/") {
			problems = append(problems, fmt.Sprintf("docker.devices %s: a GPU comes only through docker.gpu", path))
		}
	}
	g := dc.GPU
	if g == nil {
		return problems
	}
	if !slices.Contains(GPUVendors, g.Vendor) {
		problems = append(problems, fmt.Sprintf("docker.gpu.vendor must be one of %s, got %q", strings.Join(GPUVendors, ", "), g.Vendor))
	}
	if _, ok := gpuCountAll(g.Count); !ok {
		problems = append(problems, fmt.Sprintf("docker.gpu.count must be 1 or \"all\", got %v", g.Count))
	}
	if rocm, isBool := g.ROCm.(bool); g.ROCm != nil && !isBool {
		problems = append(problems, "docker.gpu.rocm must be true or false")
	} else if rocm && g.Vendor != "amd" {
		problems = append(problems, "docker.gpu.rocm is for vendor amd only")
	}
	if !svc.Requirements.GPU {
		problems = append(problems, "docker.gpu needs requirements.gpu: true")
	}
	images := []string{dc.Image}
	arches := make([]string, 0, len(dc.ImageByArch))
	for arch := range dc.ImageByArch {
		arches = append(arches, arch)
	}
	sort.Strings(arches)
	for _, arch := range arches {
		images = append(images, dc.ImageByArch[arch])
	}
	for _, image := range images {
		if !HasDigestPin(image) {
			problems = append(problems, "docker.gpu needs an image pinned by digest, including each architecture alternative")
			break
		}
	}
	if strings.TrimSpace(dc.Runtime) != "runc" {
		// gVisor cannot pass DRI devices through, and passes NVIDIA only with flags
		// CashPilot does not set.
		problems = append(problems, "docker.gpu needs docker.runtime: runc")
	}
	if strings.ToLower(strings.TrimSpace(dc.NetworkMode)) == "host" {
		problems = append(problems, "docker.gpu cannot be used with network_mode: host")
	}
	return problems
}
