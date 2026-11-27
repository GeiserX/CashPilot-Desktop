package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The installer ships the committed file, so it must be what the locale files say.
func TestCommittedFileIsCurrent(t *testing.T) {
	root := filepath.Join("..", "..")
	want, err := generate(filepath.Join(root, "frontend", "src", "locales"))
	if err != nil {
		t.Fatal(err)
	}
	have, err := os.ReadFile(filepath.Join(root, "build", "windows", "installer", "i18n.nsh"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(have, want) {
		t.Fatal("build/windows/installer/i18n.nsh is out of date: run go run ./cmd/i18nnsis")
	}
}

func TestNSISString(t *testing.T) {
	got, err := nsisString(`Remove "it" from {dir} for $5 (result: {result})`)
	if err != nil {
		t.Fatal(err)
	}
	if want := `Remove $\"it$\" from $INSTDIR for $$5 (result: $0)`; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	if _, err := nsisString("Hello {name}"); err == nil || !strings.Contains(err.Error(), "cannot fill") {
		t.Fatalf("an unknown argument must fail, got %v", err)
	}
}
