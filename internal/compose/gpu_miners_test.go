package compose

import (
	"os"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// SRBMiner-Multi exits at once with an empty log when it is PID 1 (found by web
// #843), so the exported file starts it behind Docker's init, as a deploy here
// does. Kryptex's KRig gets the same, and both keep their one NVIDIA GPU.
func TestGPUMinerExportsRunBehindDockersInit(t *testing.T) {
	cat, err := catalog.LoadEmbedded(os.DirFS("../.."))
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	for _, slug := range []string{"srbminer-multi", "kryptex-pearl"} {
		text, err := Generate(cat, []string{slug}, Options{})
		if err != nil {
			t.Fatalf("%s: Generate: %v", slug, err)
		}
		block := serviceBlockOf(t, parse(t, text), "cashpilot-"+slug)
		if block["init"] != true {
			t.Errorf("%s: init = %v, want true", slug, block["init"])
		}
		devices, _ := mapOf(t, mapOf(t, mapOf(t, block, "deploy"), "resources"), "reservations")["devices"].([]any)
		if len(devices) != 1 || devices[0].(map[string]any)["driver"] != "nvidia" {
			t.Errorf("%s: GPU reservation %v, want one nvidia device", slug, devices)
		}
	}
}

// An entry whose own entrypoint or command starts an init gets no second one in
// front of it: s6-overlay's /init exits when it is not PID 1.
func TestExportSkipsInitForAnEntryThatStartsItsOwn(t *testing.T) {
	for name, mutate := range map[string]func(*catalog.Service){
		"tini entrypoint": func(s *catalog.Service) { s.Docker.Entrypoint = []string{"/sbin/tini", "--", "run"} },
		"s6 command":      func(s *catalog.Service) { s.Docker.Command = "/init" },
	} {
		svc := honeygain()
		mutate(&svc)
		block := serviceBlockOf(t, parse(t, generate(t, fakeCatalog{"honeygain": svc}, []string{"honeygain"}, Options{Hostname: "box"})), "cashpilot-honeygain")
		if _, ok := block["init"]; ok {
			t.Errorf("%s: init = %v, want none", name, block["init"])
		}
	}
	block := serviceBlockOf(t, parse(t, generate(t, fakeCatalog{"honeygain": honeygain()}, []string{"honeygain"}, Options{Hostname: "box"})), "cashpilot-honeygain")
	if block["init"] != true {
		t.Errorf("control: a plain entry exports init = %v, want true", block["init"])
	}
}
