package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/config"
)

// startupConfigSavers may write the whole config with cfg.Save. They run once in
// Startup, before the window or the fleet server can change the config.
var startupConfigSavers = map[string]bool{"ensureFleetAPIKey": true}

// THE RULE: a binding changes the config through cfg.Update, never by reading a copy
// and saving the whole copy back. A whole-copy save put back whatever another writer
// changed in between: a session consent withdrawn while the user clicked Ignore on a
// drift warning came back. This scans every non-test file of the app for the old shape.
func TestBindingsNeverSaveAWholeConfigCopy(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	scanned := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		scanned++
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || startupConfigSavers[fn.Name.Name] {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Save" {
					return true
				}
				if inner, ok := sel.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "cfg" {
					t.Errorf("%s: %s writes the whole config with cfg.Save; use cfg.Update", fset.Position(call.Pos()), fn.Name.Name)
				}
				return true
			})
		}
	}
	if scanned < 10 {
		t.Fatalf("CONTROL: scanned only %d files; is the test running in the package directory?", scanned)
	}
}

// IgnoreDrift and CompleteOnboarding write their own field and leave the others,
// a session consent included.
func TestIgnoreDriftAndOnboardingKeepAConsent(t *testing.T) {
	app := newPayloadTestApp(t, nil)
	consent := config.SessionConsent{TextVersion: "2026-11-24", AcceptedAt: time.Now().UTC().Truncate(time.Second)}
	if err := app.cfg.Update(func(c *config.AppConfig) {
		c.SessionConsent = map[string]config.SessionConsent{"wipter": consent}
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.IgnoreDrift("earnapp"); err != nil {
		t.Fatalf("IgnoreDrift: %v", err)
	}
	if err := app.CompleteOnboarding(); err != nil {
		t.Fatalf("CompleteOnboarding: %v", err)
	}
	got := app.cfg.Config()
	if got.SessionConsent["wipter"] != consent {
		t.Errorf("the consent changed: %+v", got.SessionConsent)
	}
	if got.DriftIgnored["earnapp"] == "" || !got.FirstRunComplete {
		t.Errorf("the writers' own fields were not saved: drift %v, first run %v", got.DriftIgnored, got.FirstRunComplete)
	}
}
