package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
	"github.com/GeiserX/CashPilot-Desktop/internal/workerapi"
)

// A lead Desktop commands the CashPilot Docker workers that report to it, as the web
// UI does: it deploys a service to a chosen worker and stops, starts, restarts or
// removes a copy there, through the worker API (internal/workerapi). The worker keeps
// no credentials, so each deploy carries the env, built from the form and the
// credentials this Desktop keeps.

// WorkerTarget is one Docker worker the deploy form's Where box offers.
type WorkerTarget struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Online bool   `json:"online"`
	// Reason is why the worker cannot take commands now, in plain words; "" when it can.
	Reason string `json:"reason"`
	// Services are the services the worker runs now, by its last heartbeat.
	Services []string `json:"services"`
}

// WorkerCommandsView is what the Fleet page shows under one worker: its copies and,
// when it cannot take commands now, why.
type WorkerCommandsView struct {
	Reason string                 `json:"reason"`
	Copies []store.FleetContainer `json:"copies"`
}

// WorkerDeployRequest is one deploy to a worker from the deploy form.
type WorkerDeployRequest struct {
	// WorkerID is the worker chosen in the Where box; 0 picks the one online worker.
	WorkerID int64             `json:"workerId"`
	Values   map[string]string `json:"values"`
	// Image replaces the catalog image; "" runs the catalog's.
	Image string `json:"image"`
	// Confirmed is the owner's yes to running another image than the catalog's on a
	// service that keeps a wallet, a node identity or a device id.
	Confirmed bool `json:"confirmed"`
	// AllowSharedIdentity is the owner's yes to a device id that already runs on
	// another box.
	AllowSharedIdentity bool `json:"allowSharedIdentity"`
	// ReplaceExisting is the owner's yes to replacing a managed copy on this worker.
	ReplaceExisting bool `json:"replaceExisting"`
	// ExpectedContainer is the container that yes was for: the full Docker ID the
	// replace question named (WorkerDeployResult.ExpectedContainer). "" for a
	// worker that cannot name it.
	ExpectedContainer string `json:"expectedContainer"`
	// WorkerIdentity is the worker the yes answers: the WorkerDeployResult.WorkerIdentity
	// of the question. Every yes counts only for that worker.
	WorkerIdentity string `json:"workerIdentity"`
}

// WorkerDeployResult is what a deploy to a worker did. With Ask set it did nothing:
// the owner must answer Message first, and the form sends the deploy again with the
// matching approval for the image, identity, or replacement.
type WorkerDeployResult struct {
	Ask               string   `json:"ask"`
	Message           string   `json:"message"`
	Worker            string   `json:"worker"`
	ContainerID       string   `json:"containerId"`
	Notes             []string `json:"notes"`
	CredentialWarning string   `json:"credentialWarning,omitempty"`
	// ExpectedContainer is, with Ask replace_existing, the container the question
	// is about: "none" or its full Docker ID, "" when the worker cannot name it. A
	// yes sends it back, and the worker replaces only that container.
	ExpectedContainer string `json:"expectedContainer,omitempty"`
	// WorkerIdentity names the worker a question is about: its client ID and address.
	// The form sends it back with the yes.
	WorkerIdentity string `json:"workerIdentity,omitempty"`
}

const (
	askStepUp   = "step_up_required"
	askIdentity = "identity_in_use"
	askReplace  = "replace_existing"
	// askWorkerChanged: another worker reports under the selected worker's name now,
	// so the yes given so far was about a different worker.
	askWorkerChanged = "worker_changed"
)

// leadOnlyError is why this computer sends no commands to workers, or nil.
func (a *App) leadOnlyError() error {
	if a.joined() {
		return errors.New("this computer is joined to your CashPilot, which sends the commands to its workers")
	}
	if !a.cfg.Config().FleetServerEnabled {
		return errors.New("this Desktop does not accept workers. Turn on \"Accept workers\" in Settings first")
	}
	return nil
}

// workerReason is why device cannot take commands now, "" when it can. keyKnown is
// false for a worker that confirmed a key this Desktop does not hold.
func workerReason(device store.FleetDevice, online, keyKnown bool) string {
	switch {
	case !online:
		return fmt.Sprintf("%s is offline, so it cannot take commands now.", device.Name)
	case strings.TrimSpace(device.Endpoint) == "":
		return fmt.Sprintf("%s only reports to this Desktop: it set no CASHPILOT_WORKER_URL, so it cannot take commands.", device.Name)
	case !keyKnown:
		return fmt.Sprintf("%s joined before this Desktop could send commands. Remove it on the Fleet page: it joins again by itself within about 10 minutes, and then takes commands.", device.Name)
	}
	if _, err := workerapi.ValidBaseURL(device.Endpoint); err != nil {
		return fmt.Sprintf("%s: %v.", device.Name, err)
	}
	return ""
}

// keyKnown reports whether this Desktop can authenticate to the worker: it holds the
// worker's confirmed key, or the worker has not confirmed one yet and still takes the
// shared fleet key.
func (a *App) keyKnown(id int64) bool {
	key, confirmed, err := a.store.FleetCommandKey(id)
	return err == nil && (key != "" || !confirmed)
}

func (a *App) workerOnline(device store.FleetDevice) bool {
	return store.EffectiveFleetStatus(device.Status, device.LastSeen, fleetOfflineAfterFor(device.Kind)) == "online"
}

// workerTargets are the Docker workers for the deploy form's Where box; nil when this
// computer sends no commands (joined, or not accepting workers).
func (a *App) workerTargets() []WorkerTarget {
	if a.store == nil || a.cfg == nil || a.leadOnlyError() != nil {
		return nil
	}
	devices, err := a.store.FleetDevices()
	if err != nil {
		return nil
	}
	out := []WorkerTarget{}
	for _, d := range devices {
		if d.Kind != "worker" {
			continue
		}
		online := a.workerOnline(d)
		out = append(out, WorkerTarget{
			ID:       d.ID,
			Name:     d.Name,
			Online:   online,
			Reason:   workerReason(d, online, a.keyKnown(d.ID)),
			Services: append([]string{}, d.Services...),
		})
	}
	return out
}

// workerCommandsView is, per worker id, the copies the Fleet page shows with their
// buttons, and why the worker takes no commands now.
func (a *App) workerCommandsView(devices []store.FleetDevice) map[string]WorkerCommandsView {
	out := map[string]WorkerCommandsView{}
	if a.store == nil {
		return out
	}
	reports, _ := a.store.FleetReports()
	lead := a.leadOnlyError()
	for _, d := range devices {
		if d.Kind != "worker" || d.ID <= 0 {
			continue
		}
		view := WorkerCommandsView{Copies: []store.FleetContainer{}}
		if report, ok := reports[d.ID]; ok {
			view.Copies = append(view.Copies, report.Containers...)
		}
		if lead != nil {
			view.Reason = upperFirst(lead.Error()) + "."
		} else {
			view.Reason = workerReason(d, a.workerOnline(d), a.keyKnown(d.ID))
		}
		out[fmt.Sprint(d.ID)] = view
	}
	return out
}

// withWorkerCommands is state with each worker's copies and command reason added.
func (a *App) withWorkerCommands(state FleetState) FleetState {
	state.WorkerCommands = a.workerCommandsView(state.Devices)
	return state
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// commandTarget is a worker ready to be called.
type commandTarget struct {
	device store.FleetDevice
	report store.FleetReport
	client *workerapi.Client
	// ownKey is the worker's confirmed own key, which signs approvals; "" while the
	// worker is still on the shared key.
	ownKey string
}

// identity names the worker behind the fleet row: its client ID and its address.
// A worker that takes over the row while it enrols (same name, another client)
// changes it, and so does a new address, since that is where the deploy goes.
func (t commandTarget) identity() string {
	return strings.TrimSpace(t.report.ClientID) + "@" + strings.TrimRight(strings.TrimSpace(t.device.Endpoint), "/")
}

// workerFor resolves the worker a command goes to: the one asked for, or with id 0
// the one online worker (workerapi.PickWorker), and checks it can take commands now.
func (a *App) workerFor(id int64) (commandTarget, error) {
	if err := a.leadOnlyError(); err != nil {
		return commandTarget{}, err
	}
	devices, err := a.store.FleetDevices()
	if err != nil {
		return commandTarget{}, err
	}
	var online []int64
	for _, d := range devices {
		if d.Kind == "worker" && a.workerOnline(d) && strings.TrimSpace(d.Endpoint) != "" {
			online = append(online, d.ID)
		}
	}
	id, err = workerapi.PickWorker(id, online)
	if err != nil {
		return commandTarget{}, err
	}
	var device store.FleetDevice
	found := false
	for _, d := range devices {
		if d.ID == id {
			device, found = d, true
		}
	}
	if !found || device.Kind != "worker" {
		return commandTarget{}, fmt.Errorf("no Docker worker %d reports to this Desktop", id)
	}
	key, confirmed, err := a.store.FleetCommandKey(id)
	if err != nil {
		return commandTarget{}, err
	}
	if reason := workerReason(device, a.workerOnline(device), key != "" || !confirmed); reason != "" {
		return commandTarget{}, errors.New(reason)
	}
	auth, own := key, key
	if !confirmed {
		// It has not used its own key yet, so it may not have it: it takes the shared
		// key until then, as the web UI falls back to it.
		auth, own = a.fleetKey, ""
	}
	reports, err := a.store.FleetReports()
	if err != nil {
		return commandTarget{}, fmt.Errorf("could not read the selected worker's report: %w", err)
	}
	return commandTarget{
		device: device,
		report: reports[id],
		client: &workerapi.Client{BaseURL: device.Endpoint, Key: auth},
		ownKey: own,
	}, nil
}

// DeployToWorker deploys slug to a Docker worker that reports here, with the spec the
// web UI sends. A deploy that needs the owner's yes first comes back with Ask set
// and leaves the worker's container unchanged.
func (a *App) DeployToWorker(slug string, req WorkerDeployRequest) (WorkerDeployResult, error) {
	if err := a.ready(); err != nil {
		return WorkerDeployResult{}, err
	}
	a.workerDeployMu.Lock()
	defer a.workerDeployMu.Unlock()
	target, err := a.workerFor(req.WorkerID)
	if err != nil {
		return WorkerDeployResult{}, err
	}
	svc, ok := a.catalog.Get(slug)
	if !ok {
		return WorkerDeployResult{}, fmt.Errorf("unknown service: %s", slug)
	}
	result := WorkerDeployResult{Worker: target.device.Name, WorkerIdentity: target.identity()}
	// Every yes was given about one worker. Another one that took over the row (a
	// worker that enrols under the same name) gets nothing on the strength of it,
	// not even a create-only deploy: its owner is asked first. A yes to that
	// question names the worker it was about and no other flag, so the identity is
	// checked whenever the form sends one, not only with a flag.
	if (req.ReplaceExisting || req.AllowSharedIdentity || req.Confirmed || req.WorkerIdentity != "") && req.WorkerIdentity != result.WorkerIdentity {
		result.Ask = askWorkerChanged
		result.Message = fmt.Sprintf("%s is not the worker you answered about: another CashPilot worker now reports under that name, at %s. Your answers so far do not count for it. Deploy %s to it?",
			target.device.Name, target.device.Endpoint, svc.Name)
		if clientID := strings.TrimSpace(target.report.ClientID); clientID != "" && strings.HasPrefix(req.WorkerIdentity, clientID+"@") {
			// The same worker, at a new address: that is where the deploy would go.
			result.Message = fmt.Sprintf("%s now reports at a new address, %s. Your answers so far do not count for it. Deploy %s to it?",
				target.device.Name, target.device.Endpoint, svc.Name)
		}
		return result, nil
	}
	image := strings.TrimSpace(req.Image)
	if err := services.ValidateImage(image); err != nil {
		return WorkerDeployResult{}, err
	}
	stored, err := a.store.GetCredentials(slug)
	if err != nil {
		return WorkerDeployResult{}, err
	}
	spec, err := workerapi.BuildDeploySpec(svc, workerapi.SpecInput{
		Typed:           req.Values,
		Stored:          stored,
		LocalHostname:   runtime.DeviceHostname(),
		DeviceName:      workerapi.DeviceName(a.cfg.Config().HostnamePrefix, target.report.ClientID, target.device.ID),
		WorkerArch:      target.device.Arch,
		WorkerFeatures:  target.report.Features,
		WorkerGPUGrants: target.report.GPUGrants,
		CustomImage:     image,
	})
	if err != nil {
		return WorkerDeployResult{}, err
	}
	ctx := workerContext(a.ctx)
	state, err := target.client.DeployState(ctx, slug)
	if err != nil {
		return WorkerDeployResult{}, fmt.Errorf("could not check for an existing %s container on %s: %w", svc.Name, target.device.Name, err)
	}
	checked, exists := state.Checked, state.Exists
	// named: the worker names the container a deploy replaces (CashPilot #831), and
	// replaces only the one the deploy expects.
	named := state.Container != ""
	// legacyYes is a yes to the older-worker question, which named no container. A
	// yes that named one covers only that container, never the bare flag: if the
	// worker stops naming it (rolled back while the dialog was open), it asks again.
	legacyYes := req.ReplaceExisting && req.ExpectedContainer == ""
	replaceQuestion := fmt.Sprintf("Redeploy %s on %s? The existing container will be replaced, but its volumes are kept.", svc.Name, target.device.Name)
	if named {
		// The owner's yes covers only the container its question named.
		if state.Container != workerapi.NoContainer && (!req.ReplaceExisting || req.ExpectedContainer != state.Container) {
			result.Ask = askReplace
			result.ExpectedContainer = state.Container
			result.Message = namedReplaceQuestion(svc.Name, target.device.Name, state.Container)
			if req.ReplaceExisting && req.ExpectedContainer != "" {
				result.Message = fmt.Sprintf("The %s container on %s changed since you approved replacing it.\n\n%s", svc.Name, target.device.Name, result.Message)
			}
			return result, nil
		}
	} else if !legacyYes {
		if !checked {
			exists, err = target.client.LegacyDeployTargetExists(ctx, slug)
			if err != nil {
				return WorkerDeployResult{}, fmt.Errorf("could not check for an existing %s container on %s: %w", svc.Name, target.device.Name, err)
			}
		}
		if exists || !checked {
			result.Ask = askReplace
			result.Message = replaceQuestion
			if !exists {
				// An empty inventory can also mean a previous request is still deploying.
				result.Message = fmt.Sprintf("Deploy %s on %s? This worker cannot confirm that the container is absent. If one exists when the deploy starts, it will be replaced and its volumes will be kept.", svc.Name, target.device.Name)
			}
			result.Message += fmt.Sprintf(" Update CashPilot on %s so it can confirm the exact container before replacing it.", target.device.Name)
			if req.ReplaceExisting {
				result.Message = fmt.Sprintf("%s no longer names the %s container you approved replacing, so that yes does not count.\n\n%s", target.device.Name, svc.Name, result.Message)
			}
			return result, nil
		}
	}
	carriesID := false
	for _, key := range workerapi.PerDeviceKeys(svc) {
		if strings.TrimSpace(spec.Env[key]) == "" {
			continue
		}
		carriesID = true
		if req.AllowSharedIdentity {
			continue
		}
		if others := a.boxesRunning(slug, target.device.ID); len(others) > 0 {
			result.Ask = askIdentity
			result.Message = fmt.Sprintf("The %s for %s already runs on %s. Two boxes on one device id look to the provider like one device, which it may merge or ban.",
				envLabel(svc.Docker.Env, key), svc.Name, strings.Join(others, ", "))
			return result, nil
		}
	}
	critical := workerapi.KeepsIdentity(svc)
	if image != "" && (critical || carriesID) && !req.Confirmed {
		what := "runs with a device id the provider pays by"
		if critical {
			what = "keeps a wallet or node identity"
		}
		result.Ask = askStepUp
		result.Message = fmt.Sprintf("%s %s, so running another image than the catalog's needs your yes. Whatever runs in its container can read or replace it.", svc.Name, what)
		return result, nil
	}
	if critical && req.Confirmed {
		// Signed after the image is final: the approval covers the spec as sent.
		approval, err := workerapi.Sign(target.ownKey, workerapi.ActionCustomImage,
			workerapi.DeployTarget(slug, spec.Image, spec.Command, spec.Env, spec.Volumes), time.Now())
		if err != nil {
			return WorkerDeployResult{}, err
		}
		spec.Approval = approval
	}
	if named {
		spec.ExpectedContainer = state.Container
	} else {
		spec.ReplaceExisting = legacyYes
	}
	timeout := workerapi.DeployTimeout(svc.Docker.StopTimeout)
	var answer workerapi.DeployResult
	if checked {
		answer, err = target.client.DeployChecked(ctx, slug, spec, timeout)
	} else {
		answer, err = target.client.Deploy(ctx, slug, spec, timeout)
	}
	if err != nil {
		var refused *workerapi.Error
		if errors.As(err, &refused) && (refused.Code == askReplace || refused.Code == codeTargetChanged) {
			// The container, or the worker itself, changed after the lookup. A
			// worker that names the container now is asked about the one there,
			// even if it was updated in between and its refusal says to update
			// Desktop: the yes was for another container, or for none.
			current := refused.CurrentContainer
			fresh, ferr := target.client.DeployState(ctx, slug)
			freshNamed := ferr == nil && fresh.Container != ""
			if freshNamed {
				current = fresh.Container
			}
			if (named || freshNamed) && current != "" {
				question := namedReplaceQuestion(svc.Name, target.device.Name, current)
				if current == workerapi.NoContainer {
					question = fmt.Sprintf("Deploy %s on %s? No %s container is there now.", svc.Name, target.device.Name, svc.Name)
				}
				result.Ask = askReplace
				result.ExpectedContainer = current
				result.Message = fmt.Sprintf("%s refused the deploy (%s): %s\n\n%s", target.device.Name, refused.Code, refused.Message, question)
				if !named {
					result.Message = fmt.Sprintf("CashPilot on %s was updated during this deploy and now names the container it would replace.\n\n%s", target.device.Name, question)
				}
				return result, nil
			}
			if refused.Code == askReplace && !legacyYes {
				// A prior request can finish after the state read, even if its reply was lost.
				result.Ask = askReplace
				result.Message = replaceQuestion + fmt.Sprintf(" Update CashPilot on %s so it can confirm the exact container before replacing it.", target.device.Name)
				return result, nil
			}
			return WorkerDeployResult{}, fmt.Errorf("%s: %s (%s)", target.device.Name, refused.Message, refused.Code)
		}
		if errors.As(err, &refused) && refused.Code == askStepUp && !req.Confirmed && critical {
			// The worker's own catalog sees a difference (a worker on another
			// release): its message says which, and a yes sends it signed.
			result.Ask = askStepUp
			result.Message = refused.Message
			return result, nil
		}
		return WorkerDeployResult{}, fmt.Errorf("%s: %w", target.device.Name, err)
	}
	result.ContainerID = answer.ContainerID
	for _, k := range answer.KeptMounts {
		result.Notes = append(result.Notes, fmt.Sprintf("%s stays on %s, where the running container keeps it, instead of %s", k.Target, k.Kept, k.Requested))
	}
	if err := a.store.MarkFleetCopy(target.device.ID, slug, 1, "running"); err != nil {
		a.emitError("worker-deploy", err)
	}
	if err := a.saveWorkerCredentials(slug, req.Values); err != nil {
		result.CredentialWarning = "CashPilot could not save the credentials on this computer."
		a.emitError("credentials", err)
	}
	return result, nil
}

// codeTargetChanged is the worker's refusal of a deploy whose expected container
// is no longer the one there (CashPilot #831).
const codeTargetChanged = "deploy_target_changed"

// namedReplaceQuestion asks to replace the container id (a full Docker ID), named
// by its short ID as docker ps shows it.
func namedReplaceQuestion(service, worker, id string) string {
	return fmt.Sprintf("Redeploy %s on %s? The existing container (%s) will be replaced, but its volumes are kept.", service, worker, id[:min(12, len(id))])
}

// boxesRunning names the other boxes that run slug: this computer, and every worker
// whose last heartbeat lists it. They run the device id this Desktop keeps for it.
func (a *App) boxesRunning(slug string, except int64) []string {
	var names []string
	if _, ok, err := a.store.GetDeployment(slug); err == nil && ok {
		names = append(names, "this computer")
	}
	devices, err := a.store.FleetDevices()
	if err != nil {
		return names
	}
	reports, _ := a.store.FleetReports()
	for _, d := range devices {
		if d.ID == except || d.Kind != "worker" {
			continue
		}
		for _, c := range reports[d.ID].Containers {
			if c.Slug == slug && c.Managed {
				names = append(names, d.Name)
				break
			}
		}
	}
	return names
}

func envLabel(vars []catalog.EnvVar, key string) string {
	for _, v := range vars {
		if v.Key == key && v.Label != "" {
			return v.Label
		}
	}
	return key
}

// CommandWorkerCopy stops, starts, restarts or removes copy instance of slug on a
// worker, and answers what happened in plain words. Removing copy 1 removes the
// service on that worker, every copy of it, and keeps its data, as the web UI does.
func (a *App) CommandWorkerCopy(workerID int64, slug, command string, instance int) (string, error) {
	if err := a.ready(); err != nil {
		return "", err
	}
	if command != workerapi.CommandStop && command != workerapi.CommandStart && command != workerapi.CommandRestart && command != workerapi.CommandRemove {
		return "", fmt.Errorf("unknown command %q: use stop, start, restart or remove", command)
	}
	target, err := a.workerFor(workerID)
	if err != nil {
		return "", err
	}
	svc, _ := a.catalog.Get(slug)
	instance = max(instance, 1)
	timeout := workerapi.CommandTimeout(command, svc.Docker.StopTimeout, svc.Docker.FenceGated)
	if err := target.client.Command(workerContext(a.ctx), slug, command, instance, timeout); err != nil {
		return "", fmt.Errorf("%s: %w", target.device.Name, err)
	}
	status := map[string]string{workerapi.CommandStop: "exited", workerapi.CommandStart: "running", workerapi.CommandRestart: "running"}[command]
	if err := a.store.MarkFleetCopy(target.device.ID, slug, instance, status); err != nil {
		a.emitError("worker-command", err)
	}
	done := map[string]string{
		workerapi.CommandStop:    "stopped",
		workerapi.CommandStart:   "started",
		workerapi.CommandRestart: "restarted",
		workerapi.CommandRemove:  "removed; its data is kept for a later deploy",
	}[command]
	what := slug
	if instance > 1 {
		what = fmt.Sprintf("Copy %d of %s", instance, slug)
	}
	return fmt.Sprintf("%s on %s %s.", what, target.device.Name, done), nil
}

// workerContext bounds a call made with no app context (tests).
func workerContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
