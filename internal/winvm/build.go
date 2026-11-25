package winvm

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Build is the elevated setup and Repair (--vm-setup). It runs as Go behind the
// Builder, Prober and GuestProbe seams, so the fake covers it end to end. Every
// step is idempotent and desired.json is the journal: setup writes it before it
// creates anything, and again after each object it creates, so a build that
// stopped at any step runs again from the start and picks up where it was.
//
// It never touches a host object this install did not make: a VM or switch
// with our name is ours only when its ID is in the journal or it carries the
// journal's owner token (see Builder). Anything else with our name stops the
// build before it changes a thing.

// SetupParamsVersion is the params file format --vm-setup reads.
const SetupParamsVersion = 1

// maxParamsBytes caps a params file.
const maxParamsBytes = 64 << 10

// SetupParams is the params file the app writes for --vm-setup. It holds the
// key and the passwords: --vm-setup deletes it when it is done.
type SetupParams struct {
	Version    int    `json:"version"`
	VHDXDir    string `json:"vhdxDir"`
	ISO        string `json:"iso"`        // the Windows ISO; may be "" for a Repair of a VM that exists
	ImageIndex int    `json:"imageIndex"` // the edition's index in install.wim
	ProductKey string `json:"productKey"` // "" for an evaluation image
	TimeZone   string `json:"timeZone"`   // a Windows time zone ID, "" for Setup's default
	// AdminPassword and EarnerPassword are the guest accounts' real passwords.
	// The app generated them and keeps them under its reserved store slug.
	AdminPassword  string `json:"adminPassword"`
	EarnerPassword string `json:"earnerPassword"`
	// UserSID is the account that runs the app, for Hyper-V Administrators.
	UserSID    string       `json:"userSid"`
	ExtraExits []netip.Addr `json:"extraExits"`
	// ApproveNetwork approves the network the PC is on now, when it is not
	// metered.
	ApproveNetwork bool `json:"approveNetwork"`
	// Lab is honoured only by a winvmlab build. A product build refuses it.
	Lab *LabOverrides `json:"lab"`
}

// userSIDRe is one user account: local or domain (S-1-5-21-...) or Microsoft
// Entra ID (S-1-12-1-...). A group or well-known SID such as Everyone or
// Users would put every account in Hyper-V Administrators.
var userSIDRe = regexp.MustCompile(`^S-1-(5-21|12-1)-[0-9]+-[0-9]+-[0-9]+-[0-9]+$`)

var winPathRe = regexp.MustCompile(`^[A-Za-z]:\\[^"'` + "`" + `$;&|<>*?\r\n]*$`)

// ParseSetupParams reads a params file strictly.
func ParseSetupParams(b []byte) (SetupParams, error) {
	var sp SetupParams
	if err := decodeStrict(b, maxParamsBytes, &sp); err != nil {
		return SetupParams{}, fmt.Errorf("setup params: %w", err)
	}
	if sp.Version != SetupParamsVersion {
		return SetupParams{}, fmt.Errorf("setup params version %d, this code reads %d", sp.Version, SetupParamsVersion)
	}
	if !userSIDRe.MatchString(sp.UserSID) {
		return SetupParams{}, fmt.Errorf("setup params: %q is not the SID of one user account", sp.UserSID)
	}
	if err := checkPassword("cpadmin", sp.AdminPassword); err != nil {
		return SetupParams{}, err
	}
	if err := checkPassword("earner", sp.EarnerPassword); err != nil {
		return SetupParams{}, err
	}
	if sp.AdminPassword == sp.EarnerPassword {
		return SetupParams{}, errors.New("setup params: the two guest accounts need different passwords")
	}
	if sp.ISO != "" && (!winPathRe.MatchString(sp.ISO) || strings.Contains(sp.ISO, "..") || !strings.HasSuffix(strings.ToLower(sp.ISO), ".iso")) {
		return SetupParams{}, fmt.Errorf("setup params: %q is not an absolute path to an .iso", sp.ISO)
	}
	if sp.ProductKey != "" && !productKeyRe.MatchString(sp.ProductKey) {
		return SetupParams{}, errors.New("setup params: the product key is not five groups of five letters and digits")
	}
	if sp.TimeZone != "" && !timeZoneRe.MatchString(sp.TimeZone) {
		return SetupParams{}, fmt.Errorf("setup params: %q is not a Windows time zone ID", sp.TimeZone)
	}
	return sp, nil
}

// decodeStrict decodes one JSON document of at most max bytes with no unknown
// field and nothing after it.
func decodeStrict(b []byte, max int, out any) error {
	if len(b) > max {
		return fmt.Errorf("%d bytes, over the %d byte cap", len(b), max)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing data after the document")
	}
	return nil
}

// SetupNames is the instance a params file builds: the product's, or a lab
// instance's in a winvmlab build. It also returns the installed-RAM floor the
// host check uses: 0 (MinHostRAM) except in a lab build. A product build
// refuses lab settings, so it can never lower the RAM floor, keep Enhanced
// Session on, or use another instance's names.
func SetupNames(sp SetupParams) (Names, Options, uint64, error) {
	o := Options{Names: ProductNames(), VHDXDir: sp.VHDXDir, ExtraExits: sp.ExtraExits}
	if sp.Lab == nil {
		return o.Names, o, 0, nil
	}
	if !labBuild {
		return Names{}, Options{}, 0, errors.New("lab settings need a lab build of CashPilot (winvmlab); this build refuses them")
	}
	lo, err := sp.Lab.Apply(o)
	if err != nil {
		return Names{}, Options{}, 0, err
	}
	return lo.Names, lo, sp.Lab.MinHostRAM, nil
}

// BuildDeps is what Build works with.
type BuildDeps struct {
	HV    Builder
	Probe Prober
	// Guest sends the proof's probes; nil is the product's PowerShell Direct
	// probe as cpadmin.
	Guest GuestProbe
	// Echo asks the two echo services for this line's public IPs.
	Echo  func(ctx context.Context) []netip.Addr
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
	// Dir is Names.DataDir.
	Dir string
	// Exe is the admin-only copy of the exe the guard task runs.
	Exe  string
	Rand io.Reader
	Logf func(format string, args ...any)
	// InstallTimeout bounds the wait for Windows Setup; 0 is two hours.
	InstallTimeout time.Duration
	// ProofWait is how long each proof step waits; 0 is ProofWait.
	ProofWait time.Duration
}

func (d BuildDeps) logf(format string, args ...any) {
	if d.Logf != nil {
		d.Logf(format, args...)
	}
}

func (d BuildDeps) now() time.Time {
	if d.Now == nil {
		return time.Now()
	}
	return d.Now()
}

func (d BuildDeps) sleep(ctx context.Context, t time.Duration) error {
	if d.Sleep == nil {
		return sleepCtx(ctx, t)
	}
	return d.Sleep(ctx, t)
}

func (d BuildDeps) random(n int) []byte {
	src := d.Rand
	if src == nil {
		src = rand.Reader
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(src, b); err != nil {
		panic("winvm: no randomness: " + err.Error())
	}
	return b
}

// newMAC is a Hyper-V MAC (00-15-5D) with three random bytes.
func (d BuildDeps) newMAC() string {
	return "00155D" + strings.ToUpper(hex.EncodeToString(d.random(3)))
}

// newPassword is a temporary password for the answer file.
func (d BuildDeps) newPassword() string {
	const set = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	b := d.random(24)
	out := []byte("Cp9!")
	for _, c := range b {
		out = append(out, set[int(c)%len(set)])
	}
	return string(out)
}

// ErrNotOwned is a host object with the Secure VM's name that this install did
// not make. Build stops; Remove leaves it.
var ErrNotOwned = errors.New("a host object with the Secure VM's name was not made by this install")

// installFile holds the temporary answer-file passwords until the first
// PowerShell Direct session has replaced them. While it exists, the first
// session has not run.
const installFile = "install.json"

// answerISOFile is the answer ISO, deleted after the first session.
const answerISOFile = "answer.iso"

// guestUser names the guest's admin account, the one PowerShell Direct uses.
const guestUser = "cpadmin"

type installState struct {
	Version int    `json:"version"`
	Admin   string `json:"admin"`
	Earner  string `json:"earner"`
}

// BuildResult is what Build did.
type BuildResult struct {
	Plan   Plan         `json:"plan"`
	Proof  []StepResult `json:"proof"`
	Status Status       `json:"status"`
}

// ErrProofFailed is a build whose proof did not go green. The VM stays off the
// network: the plan is written unproven, and the guard keeps it off.
var ErrProofFailed = errors.New("the fence proof did not pass")

// builder is one Build run.
type builder struct {
	BuildDeps
	sp     SetupParams
	n      Names
	opts   Options
	minRAM uint64
	p      Plan
	prev   *Plan
	inv    Inventory
	vnicIf int
	h      HostNet
	own    []netip.Addr
	// guardOwner is the owner tag of the task setup can safely restart.
	// setup.ps1 stopped it before the exe ran, so a build that fails starts
	// it again (see Build).
	guardOwner string
	// booted is whether proveAndBoot reached the guard's boot routine.
	booted bool
}

// Build builds the Secure VM, or repairs it, and proves its fence. It returns
// the plan as written. Run it again after any failure: it resumes.
func Build(ctx context.Context, d BuildDeps, sp SetupParams) (res BuildResult, err error) {
	n, opts, minRAM, err := SetupNames(sp)
	if err != nil {
		return BuildResult{}, err
	}
	b := &builder{BuildDeps: d, sp: sp, n: n, opts: opts, minRAM: minRAM}
	// setup.ps1 stopped the guard task before this ran. A build that fails
	// before proveAndBoot runs the guard's boot routine and starts the task
	// again, so the guard never stays off: on the unproven plan the journal
	// wrote, the boot routine keeps the VM off the network.
	defer func() {
		if err == nil || b.booted || b.guardOwner == "" {
			return
		}
		bg := context.WithoutCancel(ctx)
		gd := GuardDeps{HV: b.HV, Echo: b.Echo, Now: b.Now, Dir: b.Dir, Logf: b.Logf}
		res.Status = gd.Boot(bg)
		if serr := b.HV.StartGuardTask(bg, b.n, b.guardOwner); serr != nil {
			err = errors.Join(err, fmt.Errorf("the guard task did not start: %w", serr))
		}
	}()
	steps := []struct {
		name string
		run  func(context.Context) error
	}{
		{"journal", b.journal},
		{"host checks", b.hostChecks},
		{"switch", b.ensureSwitch},
		{"NAT", b.ensureNAT},
		{"bindings", b.bindings},
		{"firewall", b.firewall},
		{"host neighbour", b.neighbor},
		{"guardian", b.guardian},
		{"VM", b.vm},
		{"integration services", b.integration},
		{"port ACLs", b.acls},
		{"Enhanced Session", b.enhancedSession},
		{"Hyper-V Administrators", b.hyperVAdmin},
		{"Windows install", b.install},
		{"first session", b.firstSession},
		{"prefs", b.prefs},
		{"guard task", b.guardTask},
	}
	for _, s := range steps {
		if err := ctx.Err(); err != nil {
			return BuildResult{Plan: b.p}, err
		}
		d.logf("setup: %s", s.name)
		if err := s.run(ctx); err != nil {
			return BuildResult{Plan: b.p}, fmt.Errorf("%s: %w", s.name, err)
		}
	}
	return b.proveAndBoot(ctx)
}

// save writes the journal.
func (b *builder) save() error {
	raw, err := b.p.Marshal()
	if err != nil {
		return err
	}
	if _, err := LoadPlan(raw); err != nil {
		return fmt.Errorf("the plan would not load again: %w", err)
	}
	return writeAtomic(filepath.Join(b.Dir, PlanFile), raw, adminOnly)
}

// loadPrev reads the journal of an earlier run, if any.
func (b *builder) loadPrev() (*Plan, error) {
	raw, err := readCapped(filepath.Join(b.Dir, PlanFile), maxPlanBytes, true)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p, err := LoadPlan(raw)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// journal reads the earlier plan, checks every object with our names against
// it, and writes the new plan before anything is created. A fresh build gets
// a new owner token, a /29 and two MACs; a resumed build or a Repair keeps
// them.
func (b *builder) journal(ctx context.Context) error {
	prev, err := b.loadPrev()
	if err != nil {
		return fmt.Errorf("the earlier desired.json cannot be read, so this install cannot tell its objects apart; remove it by hand or run Remove VM: %w", err)
	}
	if prev != nil && prev.Names != b.n {
		return fmt.Errorf("desired.json belongs to %q, not %q", prev.Names.VM, b.n.VM)
	}
	b.prev = prev
	var known KnownIDs
	if prev != nil {
		known = KnownIDs{VM: prev.VMID, Switch: prev.SwitchID}
	}
	if b.inv, err = b.HV.Inventory(ctx, b.n, known, b.sp.UserSID); err != nil {
		return err
	}
	if err := b.checkOwnership(); err != nil {
		return err
	}
	if prev != nil && b.inv.Task != nil && b.inv.Task.Owner == prev.Owner {
		b.guardOwner = prev.Owner
	}
	// A Repair changes the fence, so the VM goes off the network before the
	// first change and stays off until the proof passes and the guard's boot
	// routine finds an exact match.
	if prev != nil {
		if id := adopt(b.inv.VMs, prev.VMID, prev.Owner); id != "" {
			if err := b.HV.Disconnect(ctx, id); err != nil {
				return fmt.Errorf("could not take the VM off the network before the Repair: %w", err)
			}
		}
	}
	// A proof killed in the middle can leave a canary rule, its hv_sock test
	// key and a pktmon session. Clear them, and list the host again when
	// something went, so the key never enters the GCS baseline and the next
	// capture can start.
	owner := ""
	if prev != nil {
		owner = prev.Owner
	}
	proofCleanup, err := b.HV.ClearProof(ctx, b.n, owner)
	if err != nil {
		return fmt.Errorf("could not clear what an earlier proof left: %w", err)
	}
	for _, c := range proofCleanup.Cleared {
		b.logf("setup: removed %s, left by an earlier proof", c)
	}
	for _, c := range proofCleanup.Left {
		b.logf("setup left %s", c)
	}
	if len(proofCleanup.Cleared) > 0 {
		if b.inv, err = b.HV.Inventory(ctx, b.n, known, b.sp.UserSID); err != nil {
			return err
		}
	}
	if prev != nil && prev.Owner != "" {
		var cleanupErr error
		cleanupData(b.Dir, proofDir, func(_ string, err error) { cleanupErr = errors.Join(cleanupErr, err) })
		if cleanupErr != nil {
			return cleanupErr
		}
	}
	o := b.opts
	if prev != nil {
		o.Subnet, o.HostVNICMAC, o.GuestMAC = prev.Subnet, prev.HostVNICMAC, prev.GuestMAC
	} else {
		var inUse []netip.Prefix
		for _, x := range b.inv.NATs {
			inUse = append(inUse, x.Prefix)
		}
		h, err := b.HV.ReadHostNet(ctx, EgressSamples)
		if err != nil {
			return err
		}
		inUse = append(inUse, h.Addrs...)
		for _, r := range h.Routes {
			if onLink(r) && r.Prefix.Bits() > 0 {
				inUse = append(inUse, r.Prefix)
			}
		}
		if o.Subnet, err = PickSubnet(inUse); err != nil {
			return err
		}
		o.HostVNICMAC, o.GuestMAC = b.newMAC(), b.newMAC()
		for o.GuestMAC == o.HostVNICMAC {
			o.GuestMAC = b.newMAC()
		}
	}
	p, err := NewPlan(o, prev)
	if err != nil {
		return err
	}
	if p.Owner == "" {
		p.Owner = hex.EncodeToString(b.random(16))
	}
	// Adopt what an earlier run made but did not get to record: it carries our
	// token. An ID recorded for an object that is gone is dropped, so the step
	// makes it again.
	p.VMID = adopt(b.inv.VMs, p.VMID, p.Owner)
	if p.VMID == "" {
		p.AdapterID = ""
	}
	p.SwitchID = adopt(b.inv.Switches, p.SwitchID, p.Owner)
	p.SetGCSBaseline(b.inv.GCS)
	p.HostBuild = b.inv.HostBuild
	b.p = p
	return b.save()
}

// adopt returns the ID only when exactly one live object matches.
func adopt(objs []OwnedObject, id, owner string) string {
	var found string
	for _, o := range objs {
		if !ownedBy(o, id, owner) {
			continue
		}
		if found != "" {
			return ""
		}
		found = o.ID
	}
	return found
}

func ownedBy(o OwnedObject, id, owner string) bool {
	return owner != "" && o.Owner == owner && (id == "" || strings.EqualFold(o.ID, id))
}

// checkOwnership refuses any object with our names that the earlier plan does
// not own. With no earlier plan nothing with our names may exist.
func (b *builder) checkOwnership() error {
	var id func(kind string) string
	owner := ""
	var sub netip.Prefix
	if b.prev != nil {
		owner, sub = b.prev.Owner, b.prev.Subnet
		id = func(kind string) string {
			if kind == "vm" {
				return b.prev.VMID
			}
			return b.prev.SwitchID
		}
	} else {
		id = func(string) string { return "" }
	}
	var bad []string
	count := func(kind string, objs []OwnedObject) {
		mine := 0
		for _, o := range objs {
			if ownedBy(o, id(kind), owner) {
				mine++
			} else {
				bad = append(bad, fmt.Sprintf("the %s %s", kind, o.ID))
			}
		}
		if mine > 1 {
			bad = append(bad, fmt.Sprintf("%d %ss that claim to be this install's", mine, kind))
		}
	}
	count("vm", b.inv.VMs)
	count("switch", b.inv.Switches)
	for _, x := range b.inv.NATs {
		if strings.EqualFold(x.Name, b.n.NAT) && (b.prev == nil || b.prev.NATID == "" || x.ID != b.prev.NATID || x.Prefix.Masked() != sub) {
			bad = append(bad, fmt.Sprintf("the NAT %s on %s", x.Name, x.Prefix))
		}
	}
	for _, r := range b.inv.Firewall {
		if owner == "" || r.Owner != owner {
			bad = append(bad, "the firewall rule "+r.ID)
		}
	}
	if t := b.inv.Task; t != nil && (owner == "" || t.Owner != owner) {
		bad = append(bad, "the scheduled task "+t.ID)
	}
	if len(bad) > 0 {
		return fmt.Errorf("%w: %s. Rename or remove it, then try again", ErrNotOwned, strings.Join(bad, ", "))
	}
	return nil
}

// hostChecks runs CheckVMHost. On a Repair the VM's own memory and NAT are
// not counted against it.
func (b *builder) hostChecks(ctx context.Context) error {
	h, err := b.HV.ReadHostNet(ctx, EgressSamples)
	if err != nil {
		return err
	}
	cur, err := b.HV.ReadNetwork(ctx)
	if err != nil {
		cur = CurrentNetwork{}
	}
	b.h = h
	var nats []netip.Prefix
	for _, x := range b.inv.NATs {
		if !strings.EqualFold(x.Name, b.n.NAT) {
			nats = append(nats, x.Prefix)
		}
	}
	in := VMHostInput{
		NATs: nats, Subnet: b.p.Subnet, Net: h, OurSwitchID: b.p.SwitchID, Network: cur,
		InstalledRAM: b.inv.InstalledRAM, FreeRAM: b.inv.FreeRAM,
		GuestMemory: b.p.MemoryBytes, MinHostRAM: b.minRAM, GCS: b.inv.GCS,
	}
	r := CheckVMHost(in)
	var bad []string
	for _, c := range r.Checks {
		if c.OK == Yes || (c.ID == CheckFreeRAM && b.p.VMID != "") {
			continue
		}
		bad = append(bad, fmt.Sprintf("%s (%s)", c.ID, c.Detail))
	}
	if len(bad) > 0 {
		return fmt.Errorf("this PC is not ready for the Secure VM: %s", strings.Join(bad, "; "))
	}
	if col, why := SubnetCollision(h, b.p.SwitchID, b.p.Subnet); col {
		return fmt.Errorf("%s. Remove the VM and build it again to move it to a free range", why)
	}
	b.own = nil
	if b.Echo != nil {
		for _, a := range b.Echo(ctx) {
			if PublicIPv4(a) && !slices.Contains(b.own, a) {
				b.own = append(b.own, a)
			}
		}
	}
	if len(b.own) == 0 {
		return errors.New("could not learn this line's public address from the echo services, so its Deny cannot be placed")
	}
	return nil
}

func (b *builder) ensureSwitch(ctx context.Context) error {
	id, ifx, err := b.HV.EnsureSwitch(ctx, b.n, b.p.Subnet, b.p.Owner, b.p.HostVNICMAC, b.p.SwitchID)
	if err != nil {
		return err
	}
	if !guidRe.MatchString(id) || ifx <= 0 {
		return fmt.Errorf("the switch came back without an ID or a host vNIC (%q, %d)", id, ifx)
	}
	b.vnicIf = ifx
	if !strings.EqualFold(b.p.SwitchID, id) {
		b.p.SwitchID = id
		return b.save()
	}
	return nil
}

func (b *builder) ensureNAT(ctx context.Context) error {
	id, err := b.HV.EnsureNAT(ctx, b.n, b.p.Subnet, b.p.NATID)
	if err != nil || id == b.p.NATID {
		return err
	}
	b.p.NATID = id
	return b.save()
}

func (b *builder) bindings(ctx context.Context) error {
	return b.HV.SetBindings(ctx, b.vnicIf, b.p.BindingsOff)
}

func (b *builder) firewall(ctx context.Context) error {
	return b.HV.EnsureFirewall(ctx, b.n, b.vnicIf, b.p.Owner)
}

func (b *builder) neighbor(ctx context.Context) error {
	return b.HV.SetHostNeighbor(ctx, b.vnicIf, b.p.GuestIP, b.p.GuestMAC)
}

// guardian journals the certificate fingerprint from a confirmed creation.
func (b *builder) guardian(ctx context.Context) error {
	id, err := b.HV.EnsureGuardian(ctx)
	if err != nil || id == "" {
		return err
	}
	b.p.CreatedGuardian, b.p.GuardianID = true, id
	return b.save()
}

func (b *builder) installPath() string { return filepath.Join(b.Dir, installFile) }
func (b *builder) answerPath() string  { return filepath.Join(b.Dir, answerISOFile) }

func (b *builder) readInstall() (*installState, error) {
	raw, err := readCapped(b.installPath(), 4<<10, true)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st installState
	if err := decodeStrict(raw, 4<<10, &st); err != nil {
		return nil, fmt.Errorf("install.json: %w", err)
	}
	return &st, nil
}

// vm creates the VM when the plan has none. The temporary passwords are saved
// before the answer ISO is written, and the ISO before the VM, so a stop at
// any point resumes with the same answer file.
func (b *builder) vm(ctx context.Context) error {
	if b.p.VMID != "" {
		live, err := b.HV.ReadLive(ctx, b.n, b.p.VMID, b.p.SwitchID)
		if err != nil {
			return err
		}
		if !live.VMExists {
			return errors.New("the VM vanished while setup ran")
		}
		if b.p.AdapterID == "" && len(live.Adapters) == 1 {
			b.p.AdapterID = strings.ToUpper(live.Adapters[0].ID)
			return b.save()
		}
		if b.p.AdapterID == "" {
			return fmt.Errorf("the VM has %d network adapters, so setup cannot tell which is ours", len(live.Adapters))
		}
		return nil
	}
	if b.sp.ISO == "" {
		return errors.New("the VM is not there and no Windows ISO was given; pick the ISO and run setup again")
	}
	st, err := b.readInstall()
	if err != nil {
		return err
	}
	if st == nil {
		st = &installState{Version: 1, Admin: b.newPassword(), Earner: b.newPassword()}
		raw, _ := json.Marshal(st)
		if err := writeAtomic(b.installPath(), raw, adminOnly); err != nil {
			return err
		}
	}
	name := strings.ToUpper(b.n.VM)
	if len(name) > 15 {
		name = name[:15]
	}
	xml, err := AnswerFile{
		ImageIndex: b.sp.ImageIndex, ProductKey: b.sp.ProductKey, ComputerName: name, TimeZone: b.sp.TimeZone,
		GuestMACDash: macDash(b.p.GuestMAC), HostMACDash: macDash(b.p.HostVNICMAC),
		GuestIP: b.p.GuestIP, Gateway: b.p.Gateway, PrefixLen: b.p.Subnet.Bits(),
		AdminPassword: st.Admin, EarnerPassword: st.Earner,
	}.Render()
	if err != nil {
		return err
	}
	if err := b.HV.WriteAnswerISO(ctx, b.answerPath(), xml); err != nil {
		return err
	}
	vmID, adapterID, disk, err := b.HV.CreateVM(ctx, b.p, b.sp.ISO, b.answerPath())
	if err != nil {
		return err
	}
	if !guidRe.MatchString(vmID) || !guidRe.MatchString(adapterID) {
		return fmt.Errorf("the VM came back without a VM or adapter ID (%q, %q)", vmID, adapterID)
	}
	b.p.VMID, b.p.AdapterID, b.p.Disk = vmID, strings.ToUpper(adapterID), &disk
	return b.save()
}

func (b *builder) integration(ctx context.Context) error {
	return b.HV.SetIntegration(ctx, b.p.VMID, b.p.Integration)
}

// dynamic is the Denies of this host now: its addresses, its physical
// networks, the line's public IPs and every tunnel prefix.
func (b *builder) dynamic() Dynamic {
	var own []netip.Prefix
	for _, a := range b.own {
		own = append(own, netip.PrefixFrom(a, 32))
	}
	return Dynamic{HostAddrs: HostDenies(b.h, b.p.SwitchID), OwnIPs: own, Tunnels: TunnelPrefixes(b.h, b.p.SwitchID, StaticDeny())}
}

// acls makes the adapter's rows exactly the planned set: every Allow goes (one
// left by a proof that stopped), every Deny not wanted goes, and every wanted
// Deny missing or one-way is added.
func (b *builder) acls(ctx context.Context) error {
	live, err := b.HV.ReadLive(ctx, b.n, b.p.VMID, b.p.SwitchID)
	if err != nil {
		return err
	}
	want := map[netip.Prefix]bool{}
	for _, a := range b.p.WantACLs(b.dynamic()) {
		want[a.Remote] = true
	}
	var rows []ACL
	for _, a := range live.ACLs {
		if strings.EqualFold(a.Action, "Meter") {
			return fmt.Errorf("the adapter has a Meter row for %v; turn resource metering off for this VM and run setup again", a.Remote)
		}
		rows = append(rows, ACL{Action: a.Action, Direction: a.Direction, Remote: a.Remote.Masked()})
	}
	var allow, extra, add []netip.Prefix
	have := map[netip.Prefix]bool{}
	for _, a := range foldBoth(rows) {
		switch {
		case strings.EqualFold(a.Action, "Allow"):
			allow = append(allow, a.Remote)
		case !want[a.Remote] || a.Direction != "Both":
			extra = append(extra, a.Remote)
		default:
			have[a.Remote] = true
		}
	}
	for _, q := range allow {
		if err := b.Probe.RemoveAllow(ctx, b.p.VMID, q); err != nil {
			return err
		}
	}
	if len(extra) > 0 {
		slices.SortFunc(extra, func(x, y netip.Prefix) int { return x.Addr().Compare(y.Addr()) })
		extra = slices.Compact(extra)
		if err := b.HV.RemoveDeny(ctx, b.p.VMID, extra); err != nil {
			return err
		}
	}
	for q := range want {
		if !have[q] {
			add = append(add, q)
		}
	}
	sortPrefixes(add)
	if len(add) > 0 {
		return b.HV.AddDeny(ctx, b.p.VMID, add)
	}
	return nil
}

// enhancedSession turns Enhanced Session off under ESOff, after recording the
// host's value once (read before any change, so a stop in between never
// records the value setup itself wrote). Under ESKeep it is never touched.
func (b *builder) enhancedSession(ctx context.Context) error {
	if b.p.EnhancedSession != ESOff {
		return nil
	}
	if b.p.EnhancedSessionOriginal == nil {
		b.p.RecordEnhancedSession(b.inv.Enhanced)
		if err := b.save(); err != nil {
			return err
		}
	}
	_, err := b.HV.SetEnhancedSession(ctx, false)
	return err
}

// hyperVAdmin journals intent before the host call and confirms a grant
// only when that call reports adding the account.
func (b *builder) hyperVAdmin(ctx context.Context) error {
	if !b.p.AdminGrantsTracked {
		b.p.PendingAdminSIDs = append(b.p.PendingAdminSIDs, b.p.AddedAdminSIDs...)
		b.p.AddedAdminSIDs = []string{}
		b.p.AdminGrantsTracked = true
		if err := b.save(); err != nil {
			return err
		}
	}
	if b.inv.HyperVAdmin {
		return nil
	}
	sid := b.sp.UserSID
	isSID := func(s string) bool { return strings.EqualFold(s, sid) }
	b.p.AddedAdminSIDs = slices.DeleteFunc(b.p.AddedAdminSIDs, isSID)
	if !slices.ContainsFunc(b.p.PendingAdminSIDs, isSID) {
		b.p.PendingAdminSIDs = append(b.p.PendingAdminSIDs, sid)
	}
	if err := b.save(); err != nil {
		return err
	}
	added, err := b.HV.AddHyperVAdmin(ctx, sid)
	if err != nil || !added {
		return err
	}
	b.p.PendingAdminSIDs = slices.DeleteFunc(b.p.PendingAdminSIDs, isSID)
	if len(b.p.PendingAdminSIDs) == 0 {
		b.p.PendingAdminSIDs = nil
	}
	b.p.AddedAdminSIDs = append(b.p.AddedAdminSIDs, sid)
	return b.save()
}

// install starts the VM for Windows Setup, presses the boot key, and waits
// until PowerShell Direct answers. A VM whose first session already ran is
// only started.
func (b *builder) install(ctx context.Context) error {
	st, err := b.readInstall()
	if err != nil {
		return err
	}
	live, err := b.HV.ReadLive(ctx, b.n, b.p.VMID, b.p.SwitchID)
	if err != nil {
		return err
	}
	if live.VMState != "Running" {
		if err := b.HV.Start(ctx, b.p.VMID); err != nil {
			return err
		}
		if st != nil && live.DVDs > 0 {
			if err := b.HV.BootKeys(ctx, b.p.VMID); err != nil {
				return err
			}
		}
	}
	creds := []Cred{{User: guestUser, Password: b.sp.AdminPassword}}
	if st != nil {
		creds = append([]Cred{{User: guestUser, Password: st.Admin}}, creds...)
	}
	limit := b.InstallTimeout
	if limit == 0 {
		limit = 2 * time.Hour
	}
	deadline := b.now().Add(limit)
	for {
		var last error
		for _, c := range creds {
			var out struct {
				OK bool `json:"ok"`
			}
			if last = b.HV.Guest(ctx, b.p.VMID, c, scriptGuestPing, map[string]any{}, &out); last == nil && out.OK {
				return nil
			}
		}
		if !b.now().Before(deadline) {
			return fmt.Errorf("Windows did not finish installing within %s: %v", limit, last)
		}
		if err := b.sleep(ctx, 30*time.Second); err != nil {
			return err
		}
	}
}

// firstSession sets the real passwords, deletes Windows Setup's copies of the
// answer file, removes both DVD drives and deletes the answer ISO, then the
// temporary passwords. install.json goes last: while it is there, the next run
// does this again.
func (b *builder) firstSession(ctx context.Context) error {
	st, err := b.readInstall()
	if err != nil {
		return err
	}
	if st != nil {
		arg := map[string]any{
			"admin": b.sp.AdminPassword, "earner": b.sp.EarnerPassword,
			"guestMac": macDash(b.p.GuestMAC), "hostMac": macDash(b.p.HostVNICMAC), "gateway": b.p.Gateway.String(),
		}
		var out struct {
			OK          bool     `json:"ok"`
			PantherLeft []string `json:"pantherLeft"`
		}
		// The temporary password works until the session ran; after a stop
		// in the middle, the real one does.
		err := b.HV.Guest(ctx, b.p.VMID, Cred{User: guestUser, Password: st.Admin}, scriptFirstSession, arg, &out)
		if err != nil {
			err = b.HV.Guest(ctx, b.p.VMID, Cred{User: guestUser, Password: b.sp.AdminPassword}, scriptFirstSession, arg, &out)
		}
		if err != nil {
			return err
		}
		if !out.OK || len(out.PantherLeft) > 0 {
			return fmt.Errorf("the guest still holds copies of the answer file: %s", strings.Join(out.PantherLeft, ", "))
		}
	}
	live, err := b.HV.ReadLive(ctx, b.n, b.p.VMID, b.p.SwitchID)
	if err != nil {
		return err
	}
	if live.DVDs > 0 {
		if err := b.HV.EjectDVDs(ctx, b.p.VMID); err != nil {
			return err
		}
	}
	for _, f := range []string{b.answerPath(), b.installPath()} {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// prefs makes sure prefs.json exists, the one file the user may write, and
// approves the network the PC is on now when asked and it is not metered.
func (b *builder) prefs(ctx context.Context) error {
	path := filepath.Join(b.Dir, PrefsFile)
	if _, err := os.Lstat(path); err == nil {
		return nil
	}
	pr := Prefs{ApprovedNetworks: []NetworkID{}}
	if b.sp.ApproveNetwork {
		cur, err := b.HV.ReadNetwork(ctx)
		if err != nil {
			return fmt.Errorf("could not read which network this PC is on: %w", err)
		}
		if cur.Metered {
			return errors.New("this PC is on a metered network, which is never approved")
		}
		if id, ok := normalizeNetwork(cur.ID); ok {
			pr.ApprovedNetworks = append(pr.ApprovedNetworks, id)
		}
	}
	raw, err := json.MarshalIndent(pr, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(raw, '\n'), usersWrite)
}

// guardTask registers the guard task, stopping a running guard first.
func (b *builder) guardTask(ctx context.Context) error {
	if err := b.HV.RegisterGuardTask(ctx, b.n, b.Exe, b.p.Owner); err != nil {
		return err
	}
	b.guardOwner = b.p.Owner
	return nil
}

// proveAndBoot checks the build reached the plan, seeds the guard's state,
// runs the proof under its lock, writes the verdict, then runs the guard's
// boot routine and starts the guard task.
func (b *builder) proveAndBoot(ctx context.Context) (BuildResult, error) {
	res := BuildResult{Plan: b.p}
	h, err := b.HV.ReadHostNet(ctx, EgressSamples)
	if err != nil {
		return res, err
	}
	b.h = h
	dyn := b.dynamic()
	live, err := b.HV.ReadLive(ctx, b.n, b.p.VMID, b.p.SwitchID)
	if err != nil {
		return res, err
	}
	if cs := Diff(b.p, live, dyn); len(cs) > 0 {
		var fs []string
		for _, c := range cs {
			fs = append(fs, fmt.Sprintf("%s: want %s, got %s", c.Field, c.Want, c.Got))
		}
		return res, fmt.Errorf("the VM did not reach the planned state: %s", strings.Join(fs, "; "))
	}
	if err := b.seedGuardState(dyn); err != nil {
		return res, err
	}
	g := b.Guest
	if g == nil {
		g = NewGuestProbe(b.HV, b.p.VMID, Cred{User: guestUser, Password: b.sp.AdminPassword})
	}
	// Every canary rule and hv_sock test key the proof makes carries the
	// owner token, so ClearProof can find this install's and only those.
	pr := b.Probe
	if o, ok := pr.(interface{ forPlan(Plan) Prober }); ok {
		pr = o.forPlan(b.p)
	}
	run := Runner{B: b.HV, P: pr, G: g, Clock: b.Now, Sleep: b.Sleep, Dir: b.Dir, Rand: b.Rand, Logf: b.Logf, Wait: b.ProofWait}
	t := Targets(h, b.p.SwitchID, b.own, live.VNICIfIndex)
	rs, runErr := run.Run(ctx, b.p, h, t)
	res.Proof = rs
	at := b.now()
	// Only a green verdict marks the plan proven. Whatever the proof found,
	// the guard's boot routine runs next and the guard task starts: on an
	// unproven plan they keep the VM off the network.
	var proofErr, saveErr error
	switch {
	case runErr != nil:
		proofErr = fmt.Errorf("proof: %w", runErr)
	default:
		if err := b.p.markProven(rs, at); err != nil {
			proofErr = err
		} else {
			saveErr = b.save()
		}
	}
	if err := WriteProof(b.Dir, at, rs, errors.Join(proofErr, saveErr)); err != nil {
		b.logf("could not write proof.json: %v", err)
	}
	if saveErr != nil {
		return res, saveErr
	}
	res.Plan = b.p
	b.booted = true
	gd := GuardDeps{HV: b.HV, Echo: b.Echo, Now: b.Now, Dir: b.Dir, Logf: b.Logf}
	res.Status = gd.Boot(context.WithoutCancel(ctx))
	if err := b.HV.StartGuardTask(context.WithoutCancel(ctx), b.n, b.p.Owner); err != nil {
		return res, errors.Join(proofErr, fmt.Errorf("the guard task did not start: %w", err))
	}
	return res, proofErr
}

// seedGuardState hands the guard the own IPs setup saw and the Denies it
// placed for this host, so the guard's first pass owns those rows: one it did
// not know would read as extra and cut the VM off.
func (b *builder) seedGuardState(dyn Dynamic) error {
	gd := GuardDeps{Dir: b.Dir, Logf: b.Logf}
	gs := gd.loadState()
	gs = gs.forPlan(b.p)
	gs.OwnIPs.Observe(b.now(), b.own)
	base := map[netip.Prefix]bool{}
	for _, a := range b.p.WantACLs(Dynamic{}) {
		base[a.Remote] = true
	}
	gs.Denied = nil
	for _, a := range b.p.WantACLs(dyn) {
		if !base[a.Remote] {
			gs.Denied = append(gs.Denied, a.Remote)
		}
	}
	gs.Cut, gs.CutChanges = false, nil
	return gd.saveState(gs)
}
