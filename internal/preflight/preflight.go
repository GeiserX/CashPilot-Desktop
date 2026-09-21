// Package preflight answers one question before a deploy runs: what, on THIS
// machine, will stop this service earning?
//
// The catalog shows one generic earnings range per service. A user cannot tell,
// before deploying, whether a service will work at all in their situation — and
// several will not. They find out weeks later, when it has earned nothing.
//
// This is the Go port of the web repository's app/preflight.py, and it keeps that
// module's two rules:
//
//   - Informed consent, not a nanny. Where the answer is "this will earn nothing
//     for you", say so plainly and let the deploy proceed. Nothing here blocks.
//   - Never imply a check we did not run. Desktop cannot see what kind of internet
//     connection it is on, how fast it is, or how much disk is free. Requirements
//     that depend on those are reported as unverified preconditions in the user's
//     own words, never as a pass, and everything unknown is listed by name.
//
// Two things are deliberately NOT a straight copy of the web module, because on a
// desktop they would be wrong:
//
//   - The web module warns when the SAME service is already deployed on the same
//     worker, treating it as a second instance. Desktop deploys one container per
//     service (cashpilot-<slug>), so redeploying a service replaces it. Firing "a
//     second instance earns nothing" there would warn every user who simply
//     re-deploys, which is the false alarm this whole feature exists to avoid.
//   - The web module can confirm that two workers leave through the same public
//     address and states a documented per-IP conflict as fact. Desktop knows the
//     names and services of the other machines in the fleet but not their egress,
//     so the same conflict is reported as something to check, conditional on the
//     machines sharing one connection. A warning we cannot stand behind is stated
//     as a question, never as a verdict.
package preflight

import (
	"fmt"
	"sort"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// The four verdicts, worst first. Same vocabulary as the web repository, so a
// finding means the same thing in both products.
const (
	EarnsNothing  = "will_earn_nothing"
	Reduced       = "reduced_earnings"
	CheckYourself = "check_these"
	LooksFine     = "looks_fine"
)

var severity = map[string]int{EarnsNothing: 3, Reduced: 2, CheckYourself: 1, LooksFine: 0}

// Finding is one thing worth knowing before the deploy, in the words the user
// reads.
type Finding struct {
	Verdict string `json:"verdict"`
	Message string `json:"message"`
}

// Device is one other machine in the fleet, reduced to what a preflight needs:
// what to call it, and what it already runs.
type Device struct {
	Name     string   `json:"name"`
	Services []string `json:"services"`
}

// Input is everything the assessment reads. Every field is optional: with an empty
// Input the assessment degrades to what the catalog entry alone says and keeps
// listing what it could not check.
type Input struct {
	// Service is the catalog entry about to be deployed.
	Service catalog.Service
	// Fleet is the OTHER machines registered with this Desktop. The local machine is
	// not in here: Desktop runs one container per service, so its own deployments are
	// a redeploy rather than a second device.
	Fleet []Device
	// DaemonArch is what the container runtime reports about the CPU it runs on
	// ("arm64", "x86_64"). Empty means it could not be read, which is reported as
	// unchecked rather than guessed — the Desktop binary's own architecture is not
	// the same fact when the runtime is a VM or a remote context.
	DaemonArch string
}

// Report is the whole answer, ready to render. Blocking is always false: this
// informs a decision, it never takes it.
type Report struct {
	Slug       string    `json:"slug"`
	Name       string    `json:"name"`
	Verdict    string    `json:"verdict"`
	Summary    string    `json:"summary"`
	Findings   []Finding `json:"findings"`
	NotChecked []string  `json:"notChecked"`
	// MachineArch is the architecture the assessment used, empty when unknown.
	MachineArch string `json:"machineArch"`
	Blocking    bool   `json:"blocking"`
}

// Assess answers "what will this realistically do for me?" before the deploy runs.
func Assess(in Input) Report {
	svc := in.Service
	reqs := svc.Requirements
	name := serviceName(svc)
	findings := make([]Finding, 0, 6)

	// The provider forbids the very thing CashPilot does. This outranks every other
	// finding: the outcome is not "earns less", it is a terminated account with the
	// pending balance cancelled, and CashPilot deploying it as a container IS the
	// violation. Saying nothing here would be the worst possible silence, because
	// the tool causes the breach on the user's behalf.
	if reqs.ContainerProhibited {
		findings = append(findings, Finding{
			Verdict: EarnsNothing,
			Message: "This provider forbids running its software in containers, on virtual machines, " +
				"on home servers, or for money — which is exactly what CashPilot does. Their stated " +
				"penalty is closing the account without notice and cancelling any pending payment. " +
				"Deploying it here means accepting that risk knowingly.",
		})
	}

	// Hardware the container cannot conjure.
	if reqs.GPU {
		findings = append(findings, Finding{
			Verdict: CheckYourself,
			Message: "This needs a supported graphics card passed through to the container. " +
				"Without one it will start and then sit idle, earning nothing.",
		})
	}

	if storage := strings.TrimSpace(reqs.MinStorage); storage != "" {
		findings = append(findings, Finding{
			Verdict: CheckYourself,
			Message: fmt.Sprintf("Needs at least %s of disk you can commit for months. Storage payouts build "+
				"up slowly and part of the balance is held back and lost if the node is abandoned "+
				"early — running one for a month is worse than not running it at all.", storage),
		})
	}

	// Connection type. Desktop cannot see whether this machine sits on a home line or
	// in a datacentre, so this stays an explicitly unverified precondition rather
	// than being dressed up as a check.
	if reqs.ResidentialIP && !reqs.VPSIP {
		findings = append(findings, Finding{
			Verdict: CheckYourself,
			Message: "This needs a home internet connection. On a rented server or datacentre " +
				"connection it usually earns far less, or the account is banned outright. " +
				"CashPilot cannot check what kind of connection you have, so this one is on you.",
		})
	}

	if bandwidth := strings.TrimSpace(reqs.MinBandwidth); bandwidth != "" {
		findings = append(findings, Finding{
			Verdict: CheckYourself,
			Message: fmt.Sprintf("Wants at least %s. Below that it still runs, it just earns "+
				"proportionally less. CashPilot does not measure your connection.", bandwidth),
		})
	}

	// A note the catalog author left specifically for this situation.
	if note := strings.TrimSpace(reqs.Note); note != "" {
		findings = append(findings, Finding{Verdict: CheckYourself, Message: note})
	}

	// Does the provider publish a build for this CPU at all? A wrong answer here is
	// not "earns less" but "never starts", so it gets the top verdict. Emulation
	// (Docker Desktop with Rosetta, binfmt/qemu) makes a foreign image run anyway and
	// cannot be detected from here, so that exception is stated rather than guessed.
	archChecked := false
	if strings.TrimSpace(svc.Docker.Image) != "" {
		supported, known := supports(svc.Docker, in.DaemonArch)
		archChecked = known
		if known && !supported {
			findings = append(findings, Finding{
				Verdict: EarnsNothing,
				Message: fmt.Sprintf("This machine is %s (%s) and %s publishes no build for it, only %s. "+
					"The container will not start: your container runtime pulls the wrong build and it "+
					"dies with \"exec format error\". The one exception is a runtime that runs foreign "+
					"images under emulation (Docker Desktop with Rosetta, or binfmt/qemu), which "+
					"CashPilot cannot check.",
					Label(Family(in.DaemonArch)), strings.TrimSpace(in.DaemonArch), name,
					describeBuilds(svc.Docker)),
			})
		}
	}

	peers := peersRunning(in.Fleet, svc.Slug)
	findings = append(findings, fleetFindings(svc, peers)...)

	// Worst verdict first, keeping the order above within one verdict. The decisive
	// sentence has to be the first one read.
	sort.SliceStable(findings, func(i, j int) bool {
		return severity[findings[i].Verdict] > severity[findings[j].Verdict]
	})

	verdict := LooksFine
	for _, finding := range findings {
		if severity[finding.Verdict] > severity[verdict] {
			verdict = finding.Verdict
		}
	}

	// Say what was NOT checked, so a clean result is not mistaken for a guarantee
	// about things nobody looked at.
	notChecked := []string{
		"what kind of internet connection you have (home or datacentre)",
		"your connection speed",
		"how much disk you have free",
	}
	if strings.TrimSpace(svc.Docker.Image) != "" && !archChecked {
		// No architecture reported, an unusual CPU, or an entry that declares no
		// platforms. Say so instead of implying a pass.
		notChecked = append(notChecked, "whether this image has a build for your CPU")
	}
	if len(peers) > 0 {
		notChecked = append(notChecked, "whether your other machines use the same internet connection as this one")
	}

	return Report{
		Slug:        svc.Slug,
		Name:        name,
		Verdict:     verdict,
		Summary:     summary(verdict, svc),
		Findings:    findings,
		NotChecked:  notChecked,
		MachineArch: strings.TrimSpace(in.DaemonArch),
		Blocking:    false, // informed consent, never a block
	}
}

// fleetFindings is what the REST of the fleet implies about deploying this here.
//
// Providers cap per internet connection, not per device: a second machine behind
// the same router is two machines to CashPilot and one customer to the provider.
// Desktop cannot see whether those machines share a connection, so every finding
// here is conditional on that and carries the "check this" verdict. A warning
// nobody can act on, fired at users who are fine, is how a safety feature gets
// ignored.
func fleetFindings(svc catalog.Service, peers []string) []Finding {
	if len(peers) == 0 {
		return nil
	}
	name := serviceName(svc)
	where := strings.Join(peers, ", ")
	limit := svc.Requirements.DevicesPerIP

	switch {
	// Absent means nobody documented a limit, which is not the same as no limit. The
	// pointer keeps them apart, and collapsing them is how a warning becomes wrong.
	case limit == nil:
		return []Finding{{
			Verdict: CheckYourself,
			Message: fmt.Sprintf("%s already runs on %s. Nobody has documented how many devices this "+
				"service allows on one internet connection, so if those machines share yours, "+
				"CashPilot cannot tell you whether a second one earns or is wasted. Check the "+
				"provider's terms first.", name, where),
		}}
	case *limit == 1:
		return []Finding{{
			Verdict: CheckYourself,
			Message: fmt.Sprintf("%s already runs on %s, and it allows only one device per internet "+
				"connection. If those machines share this connection, the provider sees one device "+
				"either way: the second normally earns nothing, and some providers cancel the "+
				"balance of accounts that do this.", name, where),
		}}
	case *limit == 0:
		return []Finding{{
			Verdict: CheckYourself,
			Message: fmt.Sprintf("%s already runs on %s. The provider sets no limit per internet "+
				"connection, but if those machines share yours the instances still share one line, "+
				"so expect the pair to earn roughly what one already does rather than double.",
				name, where),
		}}
	default:
		instances := len(peers) + 1
		if instances <= *limit {
			return nil
		}
		return []Finding{{
			Verdict: CheckYourself,
			Message: fmt.Sprintf("%s already runs on %s. It allows %d devices per internet connection "+
				"and this would be number %d, so if those machines share this connection the extra "+
				"one earns nothing.", name, where, *limit, instances),
		}}
	}
}

// peersRunning names the other machines already running this service, sorted and
// without duplicates so the message reads the same on every render.
func peersRunning(fleet []Device, slug string) []string {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return nil
	}
	seen := map[string]bool{}
	names := make([]string, 0, len(fleet))
	for _, device := range fleet {
		for _, running := range device.Services {
			if strings.TrimSpace(running) != slug {
				continue
			}
			label := strings.TrimSpace(device.Name)
			if label == "" {
				label = "an unnamed machine"
			}
			if !seen[label] {
				seen[label] = true
				names = append(names, label)
			}
			break
		}
	}
	sort.Strings(names)
	return names
}

func summary(verdict string, svc catalog.Service) string {
	name := serviceName(svc)
	if svc.Requirements.ContainerProhibited {
		// "will earn nothing" is the right severity but the wrong words: the outcome
		// here is a closed account and a cancelled balance, not a disappointing month.
		return fmt.Sprintf("%s forbids being run this way. The risk is not low earnings — it is a "+
			"closed account with any pending payment cancelled. You can still deploy it.", name)
	}
	switch verdict {
	case EarnsNothing:
		return fmt.Sprintf("%s will most likely earn nothing here. You can deploy it anyway.", name)
	case Reduced:
		return fmt.Sprintf("%s will probably earn less here than the catalog range suggests.", name)
	case CheckYourself:
		return fmt.Sprintf("%s should work, as long as the points below are true of your setup.", name)
	}
	return fmt.Sprintf("Nothing stands out — %s should work normally here.", name)
}

func serviceName(svc catalog.Service) string {
	if name := strings.TrimSpace(svc.Name); name != "" {
		return name
	}
	if slug := strings.TrimSpace(svc.Slug); slug != "" {
		return slug
	}
	return "This service"
}
