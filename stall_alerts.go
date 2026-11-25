package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/stall"
	"github.com/GeiserX/CashPilot-Desktop/internal/usage"
)

// StallAlert is one running copy that carries almost no traffic while the other
// copies of its service on this computer are busy (internal/stall). It mirrors the
// web app's "stall" alert (GeiserX/CashPilot#808).
type StallAlert struct {
	Slug string `json:"slug"`
	// Service is the catalog name, Container the container's name, Copy which copy
	// of the service it is (1 for the first). A proxied copy has ProxyID, the proxy
	// it leaves through, and Copy 0.
	Service   string `json:"service"`
	Container string `json:"container"`
	Copy      int    `json:"copy"`
	ProxyID   int64  `json:"proxyId"`
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
// The proxied copies (ProxyID) take part as copies of their service: their traffic
// is their tunnel's, which carries every byte the earner moves (withProxiedUnits).
//
// While a CashPilot server judges this computer's copies (stallServer), it raises
// their alerts, comparing them with the whole fleet, and this check raises none for
// them and drops the ones it held. It still judges the proxied copies: the server's
// check reads only the counters of a worker's own copies (web usage_hourly), so it
// judges no proxied copy.
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

	server := a.stallServer(now) != ""
	a.stallMu.Lock()
	defer a.stallMu.Unlock()
	if a.stalls == nil {
		a.stalls = map[string]StallAlert{}
	}
	raised := map[string]bool{}
	for name, h := range stalled {
		info, ok := listed[name]
		if !ok || info.Status != "running" || info.Slug != h.slug || (server && info.ProxyID == 0) {
			continue
		}
		a.stalls[name] = a.stallAlert(info, h.Stall)
		raised[name] = true
	}
	for name, al := range a.stalls {
		if raised[name] {
			continue
		}
		if server && al.ProxyID == 0 {
			delete(a.stalls, name) // the server raises this copy's alert now
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
	if info.ProxyID != 0 {
		copyN = 0
	}
	return StallAlert{
		Slug: info.Slug, Service: name, Container: info.Name, Copy: copyN, ProxyID: info.ProxyID,
		Since: s.Since, Hours: s.Hours,
		Message: stallMessage(name, stallCopyLabel(copyN), info.Name, s),
	}
}

// stallCopyLabel names a copy in an alert: "copy 2", or "proxied copy" (Copy 0).
func stallCopyLabel(copyN int) string {
	if copyN == 0 {
		return "proxied copy"
	}
	return fmt.Sprintf("copy %d", copyN)
}

// stallServer is the CashPilot server that judges this computer's own copies for
// traffic stalls, or "" when this computer judges them itself. A server judges them
// when this computer is joined to it, its last answer came within three heartbeats
// and from a release whose stall check reads the counters the heartbeat carries
// (stallsJudged), and the heartbeats come at least hourly: the server counts no
// traffic across a longer gap (usage.MaxBaselineAge, the web's too).
func (a *App) stallServer(now time.Time) string {
	if a.cfg == nil || !a.joined() {
		return ""
	}
	cfg := a.cfg.Config()
	serverURL := strings.TrimSpace(cfg.UpstreamURL)
	interval := heartbeatInterval(cfg.UpstreamIntervalMinutes)
	if serverURL == "" || interval.Seconds() > usage.MaxBaselineAge {
		return ""
	}
	a.upstream.mu.Lock()
	defer a.upstream.mu.Unlock()
	if a.upstream.statusURL != serverURL || !a.upstream.stallsJudged ||
		a.upstream.lastOK.IsZero() || now.Sub(a.upstream.lastOK) > 3*interval {
		return ""
	}
	return strings.TrimRight(serverURL, "/")
}

// stallMessage says what is wrong and what to check, in the web alert's words.
func stallMessage(name, copyLabel, container string, s stall.Stall) string {
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
	return fmt.Sprintf("%s %s (%s) has sent %s an hour for %s, while its other copies on this computer sent %s an hour. "+
		"It runs but gets almost no work. Look at its logs, and restart it if this lasts. CashPilot does not restart it by itself.",
		name, copyLabel, container, mb(s.Sent), hours, mb(s.Peers))
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
			Title:   fmt.Sprintf("%s %s: almost no traffic", al.Service, stallCopyLabel(al.Copy)),
			Message: al.Message,
		})
	}
	return items
}
