package main

import (
	"fmt"
	"sort"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/stall"
)

// StallAlert is one running copy that carries almost no traffic while the other
// copies of its service on this computer are busy (internal/stall). It mirrors the
// web app's "stall" alert (GeiserX/CashPilot#808).
type StallAlert struct {
	Slug string `json:"slug"`
	// Service is the catalog name, Container the container's name, Copy which copy
	// of the service it is (1 for the first).
	Service   string `json:"service"`
	Container string `json:"container"`
	Copy      int    `json:"copy"`
	// Since is the start (Unix seconds) of its first low hour, Hours how many whole
	// low hours in a row it has had.
	Since   int64  `json:"since"`
	Hours   int    `json:"hours"`
	Message string `json:"message"`
}

// stallStopped is what "no longer runs" means for a copy the list still shows. A
// restarting copy is not stopped: a crash loop shows it as restarting between its
// runs, and clearing there would raise the alert again on every run.
var stallStopped = map[string]bool{"exited": true, "dead": true}

// checkStalls updates the stall alerts from one whole reading of every unit and
// the stored usage hours. It runs after each usage reading (every five minutes),
// so it sees what that reading saw.
//
// An alert is raised once per copy and stays until the copy carries traffic
// again, is stopped (exited or dead) or gone from the list, or its service no
// longer declares docker.traffic_stall. A copy that cannot be judged neither
// raises nor clears: it is restarting, an hour has no reading, or the other copies
// are quiet too. A reading whose list failed never reaches here (sampleUsage
// returns first), so a runtime that cannot be read changes nothing.
//
// It alerts only. It never restarts anything, as on the web.
//
// It runs while this computer is joined to a CashPilot server (joined) too. The web app
// runs the check on the server, from the per-container traffic each worker
// reports, but this computer's heartbeat carries no traffic counters
// (upstreamPayload), so the server cannot judge these copies.
func (a *App) checkStalls(infos []runtime.ContainerInfo, now time.Time) {
	if a.store == nil || a.catalog == nil {
		return
	}
	specs := map[string]*stall.Spec{}
	specFor := func(slug string) *stall.Spec {
		spec, seen := specs[slug]
		if !seen {
			if svc, ok := a.catalog.Get(slug); ok {
				spec = stall.SpecOf(svc.Docker.TrafficStall)
			}
			specs[slug] = spec
		}
		return spec
	}
	listed := map[string]runtime.ContainerInfo{}
	for _, info := range infos {
		if info.Slug != "" && info.Name != "" {
			listed[info.Name] = info
			specFor(info.Slug)
		}
	}

	type hit struct {
		slug string
		stall.Stall
	}
	stalled := map[string]hit{}
	carrying := map[string]bool{}
	declared := false
	for _, spec := range specs {
		declared = declared || spec != nil
	}
	if declared {
		rows, err := a.store.UsageHours(stall.LastWholeHour(now.Unix()) - 3600*(stall.MaxHours-1))
		if err != nil {
			return // nothing read, nothing decided
		}
		bySlug := map[string][]int{}
		for i, r := range rows {
			bySlug[r.Slug] = append(bySlug[r.Slug], i)
		}
		for slug, spec := range specs {
			if spec == nil {
				continue
			}
			mine := bySlug[slug]
			rs := rows[:0:0]
			for _, i := range mine {
				rs = append(rs, rows[i])
			}
			found, busy := stall.Judge(rs, *spec, now.Unix())
			for name, s := range found {
				stalled[name] = hit{slug, s}
			}
			for name := range busy {
				carrying[name] = true
			}
		}
	}

	a.stallMu.Lock()
	defer a.stallMu.Unlock()
	if a.stalls == nil {
		a.stalls = map[string]StallAlert{}
	}
	raised := map[string]bool{}
	for name, h := range stalled {
		info, ok := listed[name]
		if !ok || info.Status != "running" || info.Slug != h.slug {
			continue
		}
		a.stalls[name] = a.stallAlert(info, h.Stall)
		raised[name] = true
	}
	for name, al := range a.stalls {
		if raised[name] {
			continue
		}
		if info, ok := listed[name]; ok && info.Slug == al.Slug && specFor(al.Slug) != nil {
			if info.Status == "running" && !carrying[name] {
				continue // still quiet, or undecided
			}
			if info.Status != "running" && !stallStopped[info.Status] {
				continue // restarting or starting: not stopped
			}
		}
		delete(a.stalls, name)
	}
}

func (a *App) stallAlert(info runtime.ContainerInfo, s stall.Stall) StallAlert {
	name := info.Slug
	if svc, ok := a.catalog.Get(info.Slug); ok && svc.Name != "" {
		name = svc.Name
	}
	copyN := max(info.Instance, 1)
	return StallAlert{
		Slug: info.Slug, Service: name, Container: info.Name, Copy: copyN,
		Since: s.Since, Hours: s.Hours,
		Message: stallMessage(name, copyN, info.Name, s),
	}
}

// stallMessage says what is wrong and what to check, in the web alert's words.
func stallMessage(name string, copyN int, container string, s stall.Stall) string {
	mb := func(rate float64) string {
		if rate < 0.1*stall.MB {
			return "under 0.1 MB"
		}
		return fmt.Sprintf("%.1f MB", rate/stall.MB)
	}
	hours := fmt.Sprintf("%d hours", s.Hours)
	if s.Hours == 1 {
		hours = "1 hour"
	}
	return fmt.Sprintf("%s copy %d (%s) has sent %s an hour for %s, while its other copies on this computer sent %s an hour. "+
		"It runs but gets almost no work. Look at its logs, and restart it if this lasts. CashPilot does not restart it by itself.",
		name, copyN, container, mb(s.Sent), hours, mb(s.Peers))
}

// currentStalls is the current stall alerts, by service and copy.
func (a *App) currentStalls() []StallAlert {
	a.stallMu.Lock()
	defer a.stallMu.Unlock()
	out := make([]StallAlert, 0, len(a.stalls))
	for _, al := range a.stalls {
		out = append(out, al)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Service != out[j].Service {
			return out[i].Service < out[j].Service
		}
		return out[i].Copy < out[j].Copy
	})
	return out
}

// stallNotifications puts each stall alert in the Alerts menu.
func (a *App) stallNotifications() []Notification {
	var items []Notification
	for _, al := range a.currentStalls() {
		items = append(items, Notification{
			Level:   "warning",
			Title:   fmt.Sprintf("%s copy %d: almost no traffic", al.Service, al.Copy),
			Message: al.Message,
		})
	}
	return items
}
