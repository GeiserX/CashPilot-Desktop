package shimbin

import (
	"bytes"
	"debug/elf"
	"os"
	"testing"
)

// In CI the workflow runs go generate first, as the release does, so a missing
// program there means the step went missing and a release would ship without it.
func TestTheProgramIsAStaticLinuxBinaryForEachArchitecture(t *testing.T) {
	want := map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}
	for family, machine := range want {
		data := For(family)
		if data == nil {
			if os.Getenv("CI") != "" {
				t.Fatalf("no program for %s: run go generate ./internal/shimbin before the build", family)
			}
			t.Skipf("no program for %s; run go generate ./internal/shimbin", family)
		}
		f, err := elf.NewFile(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("%s: %v", family, err)
		}
		if f.Machine != machine || f.Type != elf.ET_EXEC {
			t.Fatalf("%s: machine %v type %v", family, f.Machine, f.Type)
		}
		for _, p := range f.Progs {
			if p.Type == elf.PT_INTERP {
				t.Fatalf("%s: dynamically linked, so it cannot run in an image without libc", family)
			}
		}
	}
	if For("arm") != nil || For("") != nil {
		t.Fatal("only amd64 and arm64 are built")
	}
}
