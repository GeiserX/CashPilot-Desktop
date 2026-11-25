package main

import (
	"errors"
	"fmt"
	goruntime "runtime"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// What happens to a Docker app on a computer with no Docker engine that answers.
//
// First run lets a person open the dashboard before Docker is set up, because a
// native app (MystNodes on Windows) runs without it. Docker apps cannot, so the
// app has to say so wherever a Docker app is set up or deployed: the setup card's
// "Before you deploy" panel, the deploy itself, which refuses
// before it saves or changes anything, and the Alerts list. The frontend's half,
// with the Set up Docker button, is render/runtime_state.ts.

// dockerGap says why svc cannot run on this computer right now, in plain words, or
// "" when it can: it runs on the native runtime here, it has no Docker image, or a
// Docker-compatible engine answers. goos picks "PC" or "computer".
func dockerGap(svc catalog.Service, native bool, status runtime.Status, goos string) string {
	if native || status.Available || strings.TrimSpace(svc.Docker.Image) == "" {
		return ""
	}
	where := "this computer"
	if goos == "windows" {
		where = "this PC"
	}
	name := strings.TrimSpace(svc.Name)
	if name == "" {
		name = svc.Slug
	}
	switch status.Reason {
	case runtime.ReasonNotInstalled, "":
		return fmt.Sprintf("Docker is not set up on %s yet, so %s cannot run here. Set up Docker shows how.", where, name)
	}
	return fmt.Sprintf("Docker is not ready on %s yet, so %s cannot run here. Set up Docker shows what is missing.", where, name)
}

// dockerGapFor is dockerGap for slug on this computer, read now. A slug the catalog
// does not know has no gap here: the deploy reports it unknown.
func (a *App) dockerGapFor(slug string) string {
	if a.catalog == nil || a.runtime == nil || a.services == nil {
		return ""
	}
	svc, ok := a.catalog.Get(slug)
	if !ok {
		return ""
	}
	native := len(a.services.NativeServices([]catalog.Service{svc})) > 0
	if native || strings.TrimSpace(svc.Docker.Image) == "" {
		return ""
	}
	return dockerGap(svc, native, a.runtime.Status(a.ctx), goruntime.GOOS)
}

// refuseWithoutDocker stops a deploy of a Docker app while no engine answers,
// before anything is saved or changed, with the reason in plain words.
func (a *App) refuseWithoutDocker(slug string) error {
	gap := a.dockerGapFor(slug)
	if gap == "" {
		return nil
	}
	err := errors.New(gap + " Nothing was deployed.")
	a.emitError("deploy", err)
	return err
}

func runtimeNotification(status runtime.Status) *Notification {
	if !status.Available && !status.NativeAvailable {
		return &Notification{Runtime: true, Level: "warning", Title: "Runtime offline", Message: status.Message}
	}
	if notice, ok := dockerNotice(status); ok {
		notice.Runtime = true
		return &notice
	}
	return nil
}

// dockerNotice keeps local setup visible when an engine answers elsewhere, or
// explains what runs on a computer where only native apps can start.
func dockerNotice(status runtime.Status) (Notification, bool) {
	if status.Available && status.SetupTask != "" {
		return Notification{
			Level:   "warning",
			Title:   "Docker setup needs you",
			Message: status.SetupTask + " Use Set up Docker on the Dashboard or in Settings.",
		}, true
	}
	if status.Available || !status.NativeAvailable {
		return Notification{}, false
	}
	only := "a few apps"
	if names := status.NativeServices; len(names) == 1 {
		only = names[0]
	} else if len(names) > 1 {
		only = strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
	return Notification{
		Level:   "warning",
		Title:   "Docker is not ready",
		Message: "Without Docker only " + only + " can run on this computer. Use Set up Docker on the Dashboard or in Settings.",
	}, true
}

// hasCredentialValue is true when at least one value is not blank. A form sent with
// every box empty holds no credential.
func hasCredentialValue(values map[string]string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}
