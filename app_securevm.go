package main

import (
	"context"
	"errors"
	"sync"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/securevm"
)

// The Secure VM bindings. The Security page renders securevm.Status and calls
// these; the engine is per platform (securevm_darwin.go, securevm_other.go).

var (
	secureVMOnce sync.Once
	secureVM     securevm.Engine
	secureVMErr  error
)

func (a *App) secureVMEngine() (securevm.Engine, error) {
	secureVMOnce.Do(func() { secureVM, secureVMErr = newSecureVMEngine(a) })
	if secureVMErr != nil {
		return nil, secureVMErr
	}
	if secureVM == nil {
		return nil, errNoSecureVM
	}
	return secureVM, nil
}

var errNoSecureVM = errors.New("the Secure VM is not available on this system yet")

// secureVMApps is every earner the Security page lists: those with a guest
// block, and those whose terms keep them on this computer.
func (a *App) secureVMApps() []secureVMApp {
	if a.catalog == nil {
		return nil
	}
	onHost := map[string]bool{}
	if a.store != nil {
		for _, d := range a.store.ListDeployments() {
			onHost[d.Slug] = true
		}
	}
	var out []secureVMApp
	for _, s := range a.catalog.List() {
		if s.Guest == nil && !s.Requirements.ContainerProhibited {
			continue
		}
		app := secureVMApp{Slug: s.Slug, Name: s.Name, OnHost: onHost[s.Slug]}
		if s.Guest != nil {
			app.Refused, app.MacOS = s.Guest.Refused, s.Guest.MacOS
		}
		if s.Requirements.ContainerProhibited {
			app.Refused = "terms"
		}
		out = append(out, app)
	}
	return out
}

type secureVMApp struct {
	Slug, Name, Refused string
	MacOS               *catalog.GuestMacOS
	OnHost              bool
}

// secureVMProgress sends an action's progress to the page.
func (a *App) secureVMProgress(step string, fraction float64) {
	a.emitEvent("securevm:progress", map[string]any{"step": step, "fraction": fraction})
}

// SecureVMStatus reads the Secure VM. It changes nothing.
func (a *App) SecureVMStatus() securevm.Status {
	e, err := a.secureVMEngine()
	if err != nil {
		return securevm.Status{Platform: goos(), Unavailable: err.Error(), Network: securevm.NetAbsent, Fence: securevm.FenceUnproven, Fraction: -1}
	}
	return e.Status(a.bg())
}

// SecureVMInstall makes the Secure VM, with one administrator prompt.
func (a *App) SecureVMInstall() error {
	e, err := a.secureVMEngine()
	if err != nil {
		return err
	}
	return e.Install(a.bg(), a.secureVMProgress)
}

// SecureVMVerify runs the fence proof again.
func (a *App) SecureVMVerify() (securevm.Proof, error) {
	e, err := a.secureVMEngine()
	if err != nil {
		return securevm.Proof{}, err
	}
	return e.Verify(a.bg(), a.secureVMProgress)
}

// SecureVMUndo removes the Secure VM and everything it set up.
func (a *App) SecureVMUndo() (securevm.RemoveResult, error) {
	e, err := a.secureVMEngine()
	if err != nil {
		return securevm.RemoveResult{}, err
	}
	res, err := e.Undo(a.bg(), a.secureVMProgress)
	if err != nil && len(res.Removed)+len(res.Kept)+len(res.Left) > 0 {
		// The page shows what is left; an error here would hide the list.
		return res, nil
	}
	return res, err
}

// SecureVMPlace puts an earner in the Secure VM ("vm") or takes it out ("host").
func (a *App) SecureVMPlace(slug, where string) error {
	e, err := a.secureVMEngine()
	if err != nil {
		return err
	}
	p := securevm.Placement(where)
	if p != securevm.VM && p != securevm.Host {
		return errors.New(`where must be "vm" or "host"`)
	}
	return e.Place(a.bg(), slug, p, a.secureVMProgress)
}

// SecureVMOpenScreen shows the Secure VM's screen, with slug's app in front.
func (a *App) SecureVMOpenScreen(slug string) error {
	e, err := a.secureVMEngine()
	if err != nil {
		return err
	}
	return e.OpenScreen(a.bg(), slug)
}

// SecureVMStart starts the Secure VM with its fence.
func (a *App) SecureVMStart() error {
	e, err := a.secureVMEngine()
	if err != nil {
		return err
	}
	return e.Start(a.bg())
}

// SecureVMStop stops the Secure VM until the Mac next starts.
func (a *App) SecureVMStop() error {
	e, err := a.secureVMEngine()
	if err != nil {
		return err
	}
	return e.Stop(a.bg())
}

// bg is the app's context, or Background before Startup.
func (a *App) bg() context.Context {
	if a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}
