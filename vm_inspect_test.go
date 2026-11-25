package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/e2e/harness/hvfake"
	"github.com/GeiserX/CashPilot-Desktop/internal/winvm"
)

func inspectFixture(t *testing.T) (*hvfake.HyperV, winvm.Plan, string) {
	t.Helper()
	f := hvfake.New()
	p, err := f.BuildFenced(true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "desired.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return f, p, path
}

func inspect(t *testing.T, r winvm.Reader, path string) (int, map[string]json.RawMessage, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := runVMInspect(context.Background(), r, path, &out, &errOut)
	var rep map[string]json.RawMessage
	if out.Len() > 0 {
		if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
			t.Fatalf("the report is not JSON: %v\n%s", err, out.String())
		}
	}
	return code, rep, errOut.String()
}

// --vm-inspect reads only. It takes a winvm.Reader, so it cannot call a
// method that changes anything, and on the fake it calls only reads.
func TestVMInspectReadsOnly(t *testing.T) {
	if got, want := reflect.TypeOf(runVMInspect).In(1), reflect.TypeFor[winvm.Reader](); got != want {
		t.Fatalf("runVMInspect takes %v, want exactly %v", got, want)
	}
	f, _, path := inspectFixture(t)
	before := len(f.Calls())
	code, rep, errs := inspect(t, f, path)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	reads := []string{"ReadLive", "ReadHostNet", "ReadNetwork", "DropCounters", "Usage"}
	for _, c := range f.Calls()[before:] {
		if !slices.Contains(reads, c) {
			t.Errorf("--vm-inspect called %s", c)
		}
	}
	if string(rep["changes"]) != "[]" {
		t.Errorf("a matching host shows changes: %s", rep["changes"])
	}
	for _, k := range []string{"live", "hostNet", "network", "drops", "usage", "dynamic"} {
		if string(rep[k]) == "null" || rep[k] == nil {
			t.Errorf("%s is missing from the report", k)
		}
	}
}

// A changed fence shows in the report's changes.
func TestVMInspectShowsDrift(t *testing.T) {
	f, p, path := inspectFixture(t)
	if err := f.RemoveDeny(context.Background(), p.VMID, p.Owner, []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}); err != nil {
		t.Fatal(err)
	}
	code, rep, _ := inspect(t, f, path)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var cs []winvm.Change
	json.Unmarshal(rep["changes"], &cs)
	if !slices.ContainsFunc(cs, func(c winvm.Change) bool { return c.Field == "acl" && strings.Contains(c.Want, "10.0.0.0/8") }) {
		t.Fatalf("the missing Deny is not in the changes: %+v", cs)
	}
}

// A drop counter no source answered is null in the report, never 0, and the
// exit code says a reading failed.
func TestVMInspectUnknownDropsAreNull(t *testing.T) {
	f, _, path := inspectFixture(t)
	f.FailNext("DropCounters", winvm.ErrNoDropCounter)
	code, rep, _ := inspect(t, f, path)
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if string(rep["drops"]) != "null" {
		t.Fatalf("drops = %s, want null", rep["drops"])
	}
	var errs map[string]string
	json.Unmarshal(rep["errors"], &errs)
	if errs["drops"] == "" {
		t.Fatal("the drop counter's error is not reported")
	}
}

// A plan that cannot be read, or that has no VM yet, stops before any read.
func TestVMInspectRefusesBadPlans(t *testing.T) {
	f, p, _ := inspectFixture(t)
	dir := t.TempDir()
	noID := p
	noID.VMID = ""
	b, _ := noID.Marshal()
	os.WriteFile(filepath.Join(dir, "noid.json"), b, 0o600)
	good, _ := p.Marshal()
	os.WriteFile(filepath.Join(dir, "extra.json"), bytes.Replace(good, []byte(`"version"`), []byte(`"surprise": 1, "version"`), 1), 0o600)
	os.WriteFile(filepath.Join(dir, "big.json"), bytes.Repeat([]byte(" "), 64<<10+2), 0o600)
	for _, name := range []string{"missing.json", "noid.json", "extra.json", "big.json"} {
		before := len(f.Calls())
		code, _, errs := inspect(t, f, filepath.Join(dir, name))
		if code != 2 || errs == "" {
			t.Errorf("%s: exit %d (%q), want 2 with a reason", name, code, errs)
		}
		if len(f.Calls()) != before {
			t.Errorf("%s: read the host before refusing the plan", name)
		}
	}
}
