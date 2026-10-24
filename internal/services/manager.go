package services

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	goruntime "runtime"
	"sort"
	"strings"
	"sync"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// defaultRuntimeKind is the runtime-kind key the Docker provider is registered
// under. It is exactly the value config.RuntimeProvider defaults to and the value
// every existing deployment row carries in its Runtime column, so resolving an
// empty or unknown kind to this provider leaves all of today's data and behavior
// unchanged while giving the app a registry that can hold more than one runtime
// (e.g. a future native process runtime) later.
const defaultRuntimeKind = "existing-docker"

// nativeRuntimeKind is the registry key the NativeProcessProvider registers under. A
// service that declares a native binary for THIS host is routed here (see
// kindForService); image-only services stay on the Docker default.
const nativeRuntimeKind = runtime.NativeRuntimeKind

// Manager owns the runtime provider registry and reconciles it against the store.
// providers is keyed by runtime kind; defaultKind is the kind used when a
// deployment or service does not name one — which is every service today, since
// Docker is the only registered runtime.
type Manager struct {
	providers   map[string]runtime.Provider
	defaultKind string
	catalog     *catalog.Catalog
	store       *store.Store

	// copies holds, per slug, the units of copies 2..N as the last Refresh (or a
	// deploy since) saw them. The store keeps one row per service, the first copy's,
	// so this is where the dashboard reads the others from.
	mu     sync.Mutex
	copies map[string][]runtime.ContainerInfo
}

// NewManager builds a Manager whose registry holds the given provider under
// defaultRuntimeKind, which also becomes the default kind. The signature is
// unchanged: callers still pass the single Docker provider, so the app stays
// single-runtime in behavior until a second provider is registered.
func NewManager(provider runtime.Provider, cat *catalog.Catalog, st *store.Store) *Manager {
	return &Manager{
		providers:   map[string]runtime.Provider{defaultRuntimeKind: provider},
		defaultKind: defaultRuntimeKind,
		catalog:     cat,
		store:       st,
	}
}

// Register adds a provider under the given runtime kind. It is the registration point
// the app uses to plug in additional runtimes (e.g. the native process runtime) after
// construction. Call it during Startup before the manager is shared with background
// reconciliation goroutines; it is not synchronized against a running Refresh.
func (m *Manager) Register(kind string, provider runtime.Provider) {
	m.providers[kind] = provider
}

// HasNativeRuntime reports whether a provider is registered under the native
// runtime kind. Native execution has no external dependency (no daemon or socket
// to reach), so a registered native provider is by construction available — the app
// surfaces this alongside Docker's Status so onboarding can proceed on either
// signal. It reflects registration only; it never probes a running process.
func (m *Manager) HasNativeRuntime() bool {
	_, ok := m.providers[nativeRuntimeKind]
	return ok
}

// kindForService picks the runtime kind a service should deploy onto. A service that
// declares a native binary for THIS host (GOOS/GOARCH) is preferred onto the native
// runtime when that provider is registered; every other service — image-only, or with
// no native binary for this platform — resolves to the default Docker runtime. This is
// the minimal, documented native-vs-docker selection for Phase 2: it never routes an
// image service away from Docker, and it is a no-op when the native provider is absent
// (e.g. single-runtime tests), so Docker routing is unchanged there.
func (m *Manager) kindForService(svc catalog.Service) string {
	if _, ok := m.providers[nativeRuntimeKind]; ok {
		if _, has := svc.NativeBinaryFor(goruntime.GOOS, goruntime.GOARCH); has {
			return nativeRuntimeKind
		}
	}
	return ""
}

// resolveProvider returns the provider registered for kind together with the kind
// actually used. An empty or unregistered kind falls back to the default provider
// (existing-docker) — which is every deployment today — so callers can persist the
// returned kind instead of a hardcoded literal and always record the runtime that
// really served the request.
func (m *Manager) resolveProvider(kind string) (runtime.Provider, string) {
	if kind != "" {
		if p, ok := m.providers[kind]; ok {
			return p, kind
		}
	}
	return m.providers[m.defaultKind], m.defaultKind
}

// providerForSlug resolves the provider that owns a deployed service from the
// deployment's recorded runtime kind, defaulting to the Docker provider when the
// deployment is absent or its kind is unset/unknown. With only Docker registered
// this always returns the Docker provider, so lifecycle operations are unchanged.
func (m *Manager) providerForSlug(slug string) runtime.Provider {
	kind := ""
	if dep, ok, err := m.store.GetDeployment(slug); err == nil && ok {
		kind = dep.Runtime
	}
	provider, _ := m.resolveProvider(kind)
	return provider
}

// refuseRetired rejects a service the catalog has retired (dead, dropped or broken).
//
// Retired is the same rule that hides a service from the UI, and the reason is the
// same one: the programme is gone, or the client no longer works, so deploying it
// burns a container and a signup on something that cannot pay. The card is hidden, so
// the only way here is a stale slug — a saved wizard selection, a direct binding call
// — and until now nothing on the deploy path looked at status at all.
//
// It is also what makes a retired entry's floating image harmless: the catalog stops
// pinning a digest for an image nobody will ever pull, and this is the check that
// keeps "nobody will ever pull it" true.
func refuseRetired(svc catalog.Service) error {
	if !catalog.IsRetired(svc.Status) {
		return nil
	}
	return fmt.Errorf("%s is retired (status %q) and can no longer be deployed", svc.Name, svc.Status)
}

// Deploy deploys slug from the image its deployment already uses: the catalog's, or
// the one the user chose on an earlier deploy. It is DeployImage with the image left
// out, which is what every caller that never shows an Image box should mean.
func (m *Manager) Deploy(ctx context.Context, slug string, credentials map[string]string) (store.Deployment, error) {
	return m.DeployImage(ctx, slug, credentials, nil)
}

// imageRef is a plain image reference: repository[:tag][@sha256:digest], with an
// optional registry host (and port) in front. No scheme, no spaces, nothing Docker
// would read as an option. The web app refuses the same shapes.
var imageRef = regexp.MustCompile(`^` +
	`(?:[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?(?::[0-9]{1,5})?/)?` +
	`[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*` +
	`(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*` +
	`(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?` +
	`(?:@sha256:[a-f0-9]{64})?` +
	`$`)

// ValidateImage refuses a typed image that is not an image reference. "" is valid:
// it means the catalog image.
func ValidateImage(image string) error {
	image = strings.TrimSpace(image)
	if image != "" && (len(image) > 255 || !imageRef.MatchString(image)) {
		return errors.New("image is not a valid image reference (repository[:tag][@digest])")
	}
	return nil
}

// customImage is the image this deploy runs instead of the catalog's, or "" for the
// catalog's. A typed image wins; nil (the box left out) keeps the recorded choice.
func customImage(requested *string, recorded string) (string, error) {
	if requested == nil {
		return recorded, nil
	}
	if err := ValidateImage(*requested); err != nil {
		return "", err
	}
	return strings.TrimSpace(*requested), nil
}

// DeployImage deploys slug, running image instead of the catalog's when one is given.
// nil keeps the image the deployment already records, "" goes back to the catalog,
// and anything else must pass ValidateImage before the runtime is touched. The choice
// is recorded with the deployment, so every later redeploy keeps it until cleared.
//
// Only the image changes. Capabilities, devices, ports, the entrypoint and the stop
// timeout still come from the catalog entry, so a different image gains nothing the
// service does not already have.
func (m *Manager) DeployImage(ctx context.Context, slug string, credentials map[string]string, image *string) (store.Deployment, error) {
	return m.DeployWith(ctx, slug, credentials, DeployOptions{Image: image})
}

// DeployOptions are the deploy form's choices beyond the credentials. A nil field
// means the box was left alone, and the deployment keeps what it already records.
type DeployOptions struct {
	// Image: see DeployImage.
	Image *string
	// Instances is how many copies of the service to run; see ScalingLimit.
	Instances *int
}

// DeployWith deploys slug with the given options. The first copy is deployed exactly
// as a single-copy service always was; copies 2..N follow it, and copies above N that
// are still running are removed, highest first.
func (m *Manager) DeployWith(ctx context.Context, slug string, credentials map[string]string, opts DeployOptions) (store.Deployment, error) {
	svc, ok := m.catalog.Get(slug)
	if !ok {
		return store.Deployment{}, fmt.Errorf("unknown service: %s", slug)
	}
	if svc.ManualOnly {
		return store.Deployment{}, fmt.Errorf("%s is tracked manually and has no Docker image", svc.Name)
	}
	if err := refuseRetired(svc); err != nil {
		return store.Deployment{}, err
	}
	if err := validateRequired(svc, credentials); err != nil {
		return store.Deployment{}, err
	}
	recorded, recordedCopies := "", 1
	if dep, ok, err := m.store.GetDeployment(slug); err == nil && ok {
		recorded = dep.CustomImage
		recordedCopies = max(dep.Instances, 1)
	}
	custom, err := customImage(opts.Image, recorded)
	if err != nil {
		return store.Deployment{}, err
	}
	instances := recordedCopies
	if opts.Instances != nil {
		instances = *opts.Instances
	}
	if instances < 1 || instances > MaxInstances {
		return store.Deployment{}, fmt.Errorf("instances must be between 1 and %d", MaxInstances)
	}
	if limit := m.ScalingLimit(svc); instances > limit.Max {
		if opts.Instances != nil {
			return store.Deployment{}, errors.New(limit.Reason)
		}
		// The recorded count is no longer allowed (the catalog changed since). A
		// redeploy that did not ask for a count runs what is allowed, and the
		// scale-down below removes the excess, instead of refusing a deploy the
		// locked box gives the user no way to fix.
		instances = limit.Max
	}

	// Route the service to its runtime: native when it declares a native binary for
	// this host and the native provider is registered, else the default Docker
	// runtime. Record whatever kind actually served the deploy so the persisted
	// Runtime is derived rather than a hardcoded literal.
	kind := m.kindForService(svc)
	if custom != "" {
		// The chosen image replaces the catalog's and every per-architecture
		// override with it: it is the user's answer for this machine. An image only
		// runs on Docker, so it also keeps the service off the native runtime.
		svc.Docker.Image = custom
		svc.Docker.ImageByArch = nil
		kind = ""
	}
	provider, runtimeKind := m.resolveProvider(kind)
	// Copies are read only when copies are involved, so a plain deploy asks the
	// runtime nothing it did not ask before.
	copier, _ := provider.(runtime.CopyProvider)
	if instances > 1 && copier == nil {
		return store.Deployment{}, fmt.Errorf("%s cannot run more than once on the %s runtime", svc.Name, runtimeKind)
	}
	var running []int
	if copier != nil && (opts.Instances != nil || recordedCopies > 1) {
		if running, err = copier.Copies(ctx, slug); err != nil {
			return store.Deployment{}, err
		}
	}

	m.store.RecordEvent(slug, "pull_start", deploySource(ctx, provider, svc, runtimeKind))
	info, err := provider.Deploy(ctx, runtime.DeploySpec{Slug: slug, Service: svc, Env: credentials}, func(message string) {
		m.store.RecordEvent(slug, "runtime_progress", message)
	})
	if err != nil {
		m.store.RecordEvent(slug, "deploy_error", err.Error())
		return store.Deployment{}, err
	}

	deployment := store.Deployment{
		Slug:        slug,
		ContainerID: info.ContainerID,
		Name:        info.Name,
		Image:       info.Image,
		Status:      info.Status,
		Runtime:     runtimeKind,
		CPUPercent:  info.CPUPercent,
		MemoryMB:    info.MemoryMB,
	}
	if err := m.store.UpsertDeployment(deployment); err != nil {
		return store.Deployment{}, err
	}
	if err := m.store.SetCustomImage(slug, custom); err != nil {
		return store.Deployment{}, err
	}
	deployment.CustomImage = custom
	// The count is recorded before the extra copies start: if one fails, the next
	// redeploy still knows how many this machine should run and tries again.
	if err := m.store.SetInstances(slug, instances); err != nil {
		return store.Deployment{}, err
	}
	deployment.Instances = instances
	m.store.RecordEvent(slug, "deployed", info.ContainerID)

	var copies []runtime.ContainerInfo
	for n := 2; n <= instances; n++ {
		copyInfo, err := provider.Deploy(ctx, runtime.DeploySpec{Slug: slug, Service: svc, Env: credentials, Instance: n}, func(message string) {
			m.store.RecordEvent(slug, "runtime_progress", message)
		})
		if err != nil {
			m.store.RecordEvent(slug, "deploy_error", fmt.Sprintf("copy %d: %v", n, err))
			m.setCopies(slug, copies)
			return store.Deployment{}, fmt.Errorf("copy %d of %s: %w", n, svc.Name, err)
		}
		copies = append(copies, copyInfo)
	}
	for i := len(running) - 1; i >= 0; i-- {
		if n := running[i]; n > instances {
			if err := copier.RemoveCopy(ctx, slug, n, m.stopTimeout(slug)); err != nil {
				m.store.RecordEvent(slug, "remove_error", fmt.Sprintf("copy %d: %v", n, err))
				m.setCopies(slug, copies)
				return store.Deployment{}, fmt.Errorf("copy %d of %s: %w", n, svc.Name, err)
			}
			m.store.RecordEvent(slug, "removed", fmt.Sprintf("copy %d", n))
		}
	}
	m.setCopies(slug, copies)
	return deployment, nil
}

func (m *Manager) Stop(ctx context.Context, slug string) error {
	if err := m.providerForSlug(slug).Stop(ctx, slug, m.stopTimeout(slug)); err != nil {
		m.store.RecordEvent(slug, "stop_error", err.Error())
		return err
	}
	if dep, ok, err := m.store.GetDeployment(slug); err == nil && ok {
		dep.Status = "stopped"
		_ = m.store.UpsertDeployment(dep)
	}
	m.store.RecordEvent(slug, "stopped", "")
	return nil
}

func (m *Manager) Start(ctx context.Context, slug string) error {
	if err := m.providerForSlug(slug).Start(ctx, slug); err != nil {
		m.store.RecordEvent(slug, "start_error", err.Error())
		return err
	}
	if dep, ok, err := m.store.GetDeployment(slug); err == nil && ok {
		dep.Status = "running"
		_ = m.store.UpsertDeployment(dep)
	}
	m.store.RecordEvent(slug, "started", "")
	return nil
}

func (m *Manager) Restart(ctx context.Context, slug string) error {
	if err := m.providerForSlug(slug).Restart(ctx, slug, m.stopTimeout(slug)); err != nil {
		m.store.RecordEvent(slug, "restart_error", err.Error())
		return err
	}
	if dep, ok, err := m.store.GetDeployment(slug); err == nil && ok {
		dep.Status = "running"
		_ = m.store.UpsertDeployment(dep)
	}
	m.store.RecordEvent(slug, "restarted", "")
	return nil
}

// stopTimeout is the grace period this service's catalog entry asks for, in seconds,
// or 0 when the service has left the catalog and nothing can say.
//
// The catalog is read on EVERY stop, restart and remove, not just on deploy, for the
// same reason the web worker does it: the entry is the current answer. Storj asks for
// 300 seconds because a storage node has to flush before it goes, and a node deployed
// last month, before that number existed or when it was lower, has to get those 300
// seconds too. Reading only what the container was built with would have left every
// already-running node on the old value until somebody redeployed it.
func (m *Manager) stopTimeout(slug string) int {
	svc, ok := m.catalog.Get(slug)
	if !ok {
		return 0
	}
	return runtime.StopTimeoutSeconds(svc)
}

// criticalTargets is what the catalog says about this service's unrecoverable data.
// nil means the service is no longer in the catalog, so nothing can be said — which
// the runtime treats as "assume every volume matters" rather than as permission.
func (m *Manager) criticalTargets(slug string) map[string]string {
	svc, ok := m.catalog.Get(slug)
	if !ok {
		return nil
	}
	return runtime.CriticalTargets(svc)
}

// PlanRemoval reports what removing a service would delete, so the confirmation the
// user is shown can name the data instead of saying "and its volumes".
func (m *Manager) PlanRemoval(ctx context.Context, slug string) (runtime.RemovalPlan, error) {
	return m.providerForSlug(slug).PlanRemoval(ctx, slug, m.criticalTargets(slug))
}

// Remove removes the service. deleteData decides whether the data it stored goes with
// it; allowCritical is the separate yes needed for the volumes whose loss cannot be
// undone. Both default to false, so the ordinary "remove this service" leaves every
// node identity and keystore on disk for a later redeploy to pick up.
func (m *Manager) Remove(ctx context.Context, slug string, deleteData, allowCritical bool) error {
	opts := runtime.RemoveOptions{
		DeleteData:    deleteData,
		AllowCritical: allowCritical,
		Critical:      m.criticalTargets(slug),
		StopTimeout:   m.stopTimeout(slug),
	}
	provider := m.providerForSlug(slug)
	// Copies 2..N first, then the service itself. A copy that fails to go leaves the
	// first copy and the deployment row in place, so the service stays on the
	// dashboard and Remove can be retried; the other order would leave running
	// copies with no row to show them under. Copies have no volumes, so the
	// critical-data refusal below can never be what stops the first copy after them.
	if copier, ok := provider.(runtime.CopyProvider); ok {
		copies, err := copier.Copies(ctx, slug)
		if err != nil {
			m.store.RecordEvent(slug, "remove_error", err.Error())
			return err
		}
		for _, n := range copies {
			if err := copier.RemoveCopy(ctx, slug, n, opts.StopTimeout); err != nil {
				m.store.RecordEvent(slug, "remove_error", fmt.Sprintf("copy %d: %v", n, err))
				return err
			}
		}
	}
	if err := provider.Remove(ctx, slug, opts); err != nil {
		m.store.RecordEvent(slug, "remove_error", err.Error())
		return err
	}
	m.setCopies(slug, nil)
	if err := m.store.DeleteDeployment(slug); err != nil {
		return err
	}
	detail := "container removed, stored data kept"
	if deleteData {
		detail = "container and stored data removed"
	}
	m.store.RecordEvent(slug, "removed", detail)
	return nil
}

func (m *Manager) Logs(ctx context.Context, slug string, lines int) (string, error) {
	return m.providerForSlug(slug).Logs(ctx, slug, lines)
}

// providerListing pairs a runtime kind with the units its provider reported, so a
// caller (Refresh) can record each unit under the runtime that actually owns it.
type providerListing struct {
	kind  string
	infos []runtime.ContainerInfo
}

// collectListings lists every registered provider in sorted kind order. A provider
// that fails to list is non-fatal: its error is collected and returned, but the
// other providers' units are still gathered, so one backend being down (e.g. Docker
// offline) cannot blank a healthy one. listed reports whether at least one provider
// listed successfully, letting callers surface an error only when ALL providers
// failed — which, with a single provider registered, reproduces the previous
// single-runtime behavior exactly (Docker's list on success, Docker's error on
// failure). Sorted iteration keeps the union order deterministic.
func (m *Manager) collectListings(ctx context.Context) (listings []providerListing, listed bool, err error) {
	kinds := make([]string, 0, len(m.providers))
	for kind := range m.providers {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)

	var errs []error
	for _, kind := range kinds {
		infos, listErr := m.providers[kind].List(ctx)
		if listErr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", kind, listErr))
			continue
		}
		listed = true
		listings = append(listings, providerListing{kind: kind, infos: infos})
	}
	if len(errs) > 0 {
		err = errors.Join(errs...)
	}
	return listings, listed, err
}

// List returns every managed unit across all registered providers as a single flat
// slice — the monitoring/health view used by the app's health sampler. An error is
// returned only when no provider listed successfully; a partial failure yields the
// healthy providers' units (the failing provider's error is non-fatal). With just
// Docker registered this returns Docker's list unchanged, or Docker's error.
func (m *Manager) List(ctx context.Context) ([]runtime.ContainerInfo, error) {
	listings, listed, err := m.collectListings(ctx)
	if !listed && err != nil {
		return nil, err
	}
	var union []runtime.ContainerInfo
	for _, listing := range listings {
		union = append(union, listing.infos...)
	}
	return union, nil
}

func (m *Manager) Refresh(ctx context.Context) ([]store.Deployment, error) {
	listings, listed, err := m.collectListings(ctx)
	if !listed && err != nil {
		return nil, err
	}

	total := 0
	active := make(map[string]bool)
	copies := make(map[string][]runtime.ContainerInfo)
	for _, listing := range listings {
		for _, info := range listing.infos {
			total++
			// Copies 2..N share their service's slug; the row is the first copy's.
			if info.Instance > 1 {
				copies[info.Slug] = append(copies[info.Slug], info)
				continue
			}
			dep := store.Deployment{
				Slug:        info.Slug,
				ContainerID: info.ContainerID,
				Name:        info.Name,
				Image:       info.Image,
				Status:      info.Status,
				Runtime:     listing.kind,
				CPUPercent:  info.CPUPercent,
				MemoryMB:    info.MemoryMB,
			}
			if dep.Slug != "" {
				active[dep.Slug] = true
				_ = m.store.UpsertDeployment(dep)
			}
		}
	}
	// Only reconcile away stale records when at least one provider actually
	// returned containers. An empty (but error-free) result usually means a
	// different runtime/context is active, not that every managed container
	// vanished — deleting on that would wipe the dashboard and reset CreatedAt.
	for slug := range copies {
		sort.Slice(copies[slug], func(i, j int) bool { return copies[slug][i].Instance < copies[slug][j].Instance })
	}
	m.mu.Lock()
	m.copies = copies
	m.mu.Unlock()
	if total > 0 {
		for _, dep := range m.store.ListDeployments() {
			if !active[dep.Slug] {
				_ = m.store.DeleteDeployment(dep.Slug)
				m.store.RecordEvent(dep.Slug, "missing_from_runtime", "removed stale deployment record")
			}
		}
	}
	return m.store.ListDeployments(), nil
}

// deploySource returns the human-facing source recorded in the pull_start event: the
// pinned native binary URL when deploying natively, otherwise the Docker image that is
// really about to be pulled.
//
// "Really" is the point. A catalog entry can name a different image per architecture —
// traffmonetizer publishes its ARM builds as separate tags — so on an ARM machine the
// pull is for cli_v2:arm64v8 while docker.image still reads cli_v2@sha256:... Recording
// the entry's default made the history say a deploy pulled something it never pulled,
// which is exactly the line somebody reads when an image turns out to be the problem.
// Only the provider can resolve it, because the architecture that decides is the
// DAEMON's, not this process's.
func deploySource(ctx context.Context, provider runtime.Provider, svc catalog.Service, runtimeKind string) string {
	if runtimeKind == nativeRuntimeKind {
		if bin, ok := svc.NativeBinaryFor(goruntime.GOOS, goruntime.GOARCH); ok {
			return bin.URL
		}
		return svc.Docker.Image
	}
	if resolver, ok := provider.(runtime.ImageResolver); ok {
		if image := resolver.ResolveImage(ctx, svc); image != "" {
			return image
		}
	}
	return svc.Docker.Image
}

// validateRequired checks that every required credential is supplied, across both the
// Docker and native env declarations, so a native-only service's required fields are
// enforced too. A required key missing from either declaration (with no default) fails.
func validateRequired(svc catalog.Service, credentials map[string]string) error {
	for _, list := range [][]catalog.EnvVar{svc.Docker.Env, svc.Native.Env} {
		for _, item := range list {
			label := item.Label
			if label == "" {
				label = item.Key
			}
			value := credentials[item.Key]
			if item.Required && value == "" && item.Default == "" {
				return fmt.Errorf("missing required field: %s", label)
			}
			// A value the catalog constrains must match all of the pattern before it
			// is deployed or saved. Only what the user supplied is checked; an empty
			// value falls back to the default, which the catalog itself vouches for.
			if item.Pattern != "" && value != "" {
				re, err := regexp.Compile(`^(?:` + item.Pattern + `)$`)
				if err != nil || !re.MatchString(value) {
					return fmt.Errorf("invalid value for field: %s", label)
				}
			}
		}
	}
	return nil
}

// ValidateCredentials reports whether the given credentials would let the service
// deploy — mirroring Deploy's pre-checks (known, deployable, required fields present) —
// so a caller can validate BEFORE persisting anything.
func (m *Manager) ValidateCredentials(slug string, credentials map[string]string) error {
	svc, ok := m.catalog.Get(slug)
	if !ok {
		return fmt.Errorf("unknown service: %s", slug)
	}
	if svc.ManualOnly {
		return fmt.Errorf("%s is tracked manually and has no Docker image", svc.Name)
	}
	if err := refuseRetired(svc); err != nil {
		return err
	}
	return validateRequired(svc, credentials)
}

// RequiredCredentialsMet reports whether every required field for the service is present
// in the given credentials. Unlike ValidateCredentials it does not reject a manually
// tracked service, so the settings "Configured" badge can reflect whether the stored
// blob actually satisfies the current schema — not merely that it is non-empty, which an
// orphaned pre-migration blob (wrong keys) would also be.
func (m *Manager) RequiredCredentialsMet(slug string, credentials map[string]string) bool {
	svc, ok := m.catalog.Get(slug)
	if !ok {
		return false
	}
	return validateRequired(svc, credentials) == nil
}
