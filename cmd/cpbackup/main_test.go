package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The command opens a backup the web app made and writes one tar per mount, and
// refuses to overwrite an earlier restore.
func TestDecryptWritesOneTarPerMount(t *testing.T) {
	raw, err := os.ReadFile("../../internal/statebackup/testdata/web_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Passphrase       string `json:"passphrase"`
		PassphraseBundle string `json:"passphrase_bundle"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	bundle, _ := base64.StdEncoding.DecodeString(v.PassphraseBundle)
	dir := t.TempDir()
	file := filepath.Join(dir, "b.cpbackup")
	if err := os.WriteFile(file, bundle, 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	secret := func(string) (string, error) { return v.Passphrase, nil }

	var stdout, stderr bytes.Buffer
	if code := run([]string{"decrypt", file, "--out", out}, &stdout, &stderr, secret); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	for _, name := range []string{"identity.tar", "mysterium-node.tar"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Errorf("%s not written: %v", name, err)
		}
	}
	if !strings.Contains(stdout.String(), "restores /var/lib/mysterium-node") {
		t.Errorf("stdout = %s", stdout.String())
	}

	if code := run([]string{"decrypt", file, "--out", out}, &stdout, &stderr, secret); code == 0 {
		t.Fatal("a second decrypt overwrote the first")
	}
	wrong := func(string) (string, error) { return "not the passphrase", nil }
	if code := run([]string{"decrypt", file, "--out", filepath.Join(dir, "o2")}, &stdout, &stderr, wrong); code == 0 {
		t.Fatal("a wrong passphrase decrypted")
	}
}
