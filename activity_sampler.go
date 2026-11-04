package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/activity"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// Activity: what each earner does on the network (internal/activity). Off by
// default; the owner switches it on on the Activity page. Rounds run on their
// own 5-minute wall-clock timer beside the usage sampler, never inside the
// earnings collection, and a failed round is a missing window, nothing more.

// activityState is the sampler's handle and what the last round said.
type activityState struct {
	mu        sync.Mutex
	stop      func()
	deltas    *activity.Deltas
	geo       *activity.IPGeo
	geoBusy   sync.Mutex
	last      activityRound
	lastPurge int64
}

type activityRound struct {
	At             int64    `json:"at"`
	WorkerStatus   string   `json:"workerStatus"`
	LeftoverProbes []string `json:"leftoverProbes"`
}

// activityWorker is this app's label on its helpers, so another app on the same
// Docker host (a second Desktop, a CashPilot worker) never removes ours.
func (a *App) activityWorker() string {
	host, _ := os.Hostname()
	dir := ""
	if a.cfg != nil {
		dir = a.cfg.DataDir()
	}
	sum := sha256.Sum256([]byte(host + "\x00" + dir))
	return "desktop-" + hex.EncodeToString(sum[:6])
}

func (a *App) activityOn() bool {
	return a.cfg != nil && a.cfg.Config().ActivityEnabled
}

// startActivitySampler starts the loop. It wakes a few seconds after every
// 5-minute boundary, samples when the switch is on, and purges hourly either way.
func (a *App) startActivitySampler(ctx context.Context) {
	if ctx == nil || a.store == nil || a.cfg == nil {
		return
	}
	a.activity.deltas = activity.NewDeltas()
	a.activity.geo = &activity.IPGeo{Dir: a.cfg.DataDir(), URL: activity.IPGeoURL}
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	a.activity.stop = func() {
		cancel()
		<-done
	}
	go func() {
		defer close(done)
		if a.activityOn() {
			// Helpers left by a round that died with the app.
			a.withActivityRunner(loopCtx, func(r *activity.Runner) { r.Sweep(loopCtx) })
		}
		for {
			now := time.Now()
			next := time.Unix((now.Unix()/activity.WindowSeconds+1)*activity.WindowSeconds+5, 0)
			select {
			case <-loopCtx.Done():
				return
			case <-time.After(time.Until(next)):
			}
			a.activityTick(loopCtx, time.Now())
		}
	}()
}

func (a *App) stopActivitySampler() {
	if a.activity.stop != nil {
		a.activity.stop()
		a.activity.stop = nil
	}
}

func (a *App) activityTick(ctx context.Context, now time.Time) {
	if a.activityOn() {
		a.runActivityRound(ctx)
	}
	hour := now.Unix() / 3600
	a.activity.mu.Lock()
	due := hour != a.activity.lastPurge
	if due {
		a.activity.lastPurge = hour
	}
	a.activity.mu.Unlock()
	if !due {
		return
	}
	if busy, err := a.store.PurgeActivity(now); err != nil {
		a.emitError("activity", err)
	} else if busy {
		a.emitNotice("activity", "Activity: the database was busy, so deleted addresses stay in its log until the next hourly purge.")
	}
	if a.activityOn() {
		a.activitySpikes(now)
		if a.activity.geo.NeedsRefresh() {
			go a.refreshActivityNetworks(ctx)
		}
	}
}

func (a *App) withActivityRunner(ctx context.Context, fn func(*activity.Runner)) bool {
	cli, err := runtime.NewDockerClient()
	if err != nil {
		return false
	}
	defer cli.Close()
	fn(&activity.Runner{Engine: &activity.DockerEngine{Client: cli}, Worker: a.activityWorker(), Deltas: a.activity.deltas})
	return true
}

// runActivityRound runs one round and stores it.
func (a *App) runActivityRound(ctx context.Context) {
	var w activity.Window
	var rerr error
	if !a.withActivityRunner(ctx, func(r *activity.Runner) { w, rerr = r.Round(ctx) }) {
		a.setActivityRound(activityRound{At: time.Now().Unix(), WorkerStatus: "no_runtime"})
		return
	}
	if errors.Is(rerr, activity.ErrBusy) || ctx.Err() != nil {
		return
	}
	w.Samples = append(w.Samples, a.nativeActivitySamples()...)
	activity.Fit(&w)
	a.setActivityRound(activityRound{At: time.Now().Unix(), WorkerStatus: w.WorkerStatus, LeftoverProbes: w.LeftoverProbes})
	if _, err := a.recordActivityWindow(w, time.Now()); err != nil {
		a.emitError("activity", err)
		return
	}
	a.raiseActivityAlerts(activity.AlertFindings(w), time.Now())
}

// nativeActivitySamples marks every program this app runs natively: no helper
// can join it, so it is shown and never sampled.
func (a *App) nativeActivitySamples() []activity.Sample {
	var out []activity.Sample
	for _, d := range a.store.ListDeployments() {
		if d.Runtime == runtime.NativeRuntimeKind && d.Status == "running" {
			out = append(out, activity.Sample{Container: d.Name, Members: []string{d.Name}, NetworkMode: "native", Status: "native", ListenPorts: []int{}, Out: []activity.Dest{}, In: []activity.Dest{}})
		}
	}
	return out
}

func (a *App) setActivityRound(r activityRound) {
	if r.LeftoverProbes == nil {
		r.LeftoverProbes = []string{}
	}
	a.activity.mu.Lock()
	a.activity.last = r
	a.activity.mu.Unlock()
}

// recordActivityWindow stores a window, its public destinations looked up in
// the local network table. Nothing is stored when the switch went off meanwhile.
func (a *App) recordActivityWindow(w activity.Window, now time.Time) (int, error) {
	var ips []string
	for _, s := range w.Samples {
		for _, d := range append(append([]activity.Dest{}, s.Out...), s.In...) {
			if d.Class == "public" {
				ips = append(ips, d.IP)
			}
		}
	}
	geo := map[string]activity.Geo{}
	if a.activity.geo != nil {
		geo = a.activity.geo.Lookup(ips)
	}
	rows := make([]store.ActivitySample, 0, len(w.Samples))
	for _, s := range w.Samples {
		row := store.ActivitySample{
			Container: s.Container, Status: s.Status, Reason: s.Reason, Members: s.Members, NetworkMode: s.NetworkMode,
			Opens: s.Opens, PassiveOpens: s.PassiveOpens, AttemptFails: s.AttemptFails, Resets: s.Resets, UDPIn: s.UDPIn, UDPOut: s.UDPOut,
			EstMax: s.EstMax, SynSentMax: s.SynSentMax, UDPBound: s.UDPBound, ListenPorts: s.ListenPorts, RemotesTotal: s.RemotesTotal, Truncated: s.Truncated,
		}
		for _, d := range s.Out {
			g := geo[d.IP]
			row.Dests = append(row.Dests, store.ActivityDest{Dir: "out", Proto: d.Proto, IP: d.IP, Port: d.Port, Class: d.Class, ASN: g.ASN, CC: g.CC, Seen: d.Seen})
		}
		for _, d := range s.In {
			g := geo[d.IP]
			row.Dests = append(row.Dests, store.ActivityDest{Dir: "in", Proto: "tcp", IP: d.IP, Port: d.Port, Class: d.Class, ASN: g.ASN, CC: g.CC, Seen: d.Seen})
		}
		rows = append(rows, row)
	}
	return a.store.RecordActivity(w.WindowEnd, rows, now, a.activityOn)
}

// raiseActivityAlerts stores each finding outside its cooldown and shows it.
func (a *App) raiseActivityAlerts(found []activity.Finding, now time.Time) {
	for _, f := range found {
		cooldown := time.Duration(activity.LanCooldownHours) * time.Hour
		if f.Kind == "activity_mail" {
			cooldown = time.Duration(activity.MailCooldownHours) * time.Hour
		}
		raised, err := a.store.RaiseActivityAlert(store.ActivityAlert{Kind: f.Kind, Subject: f.Subject(), Message: f.Message, CreatedAt: now.Unix()}, cooldown)
		if err != nil {
			a.emitError("activity", err)
			continue
		}
		if raised {
			a.emitNotice("activity", f.Message)
		}
	}
}

// activitySpikes raises "unusual hour" when the last full hour moved more than
// 10 times the median hour of the past week and over 1 GiB, or opened more than
// 10 times the median and over 2,000 connections. Unknown hours are skipped.
func (a *App) activitySpikes(now time.Time) {
	last := now.Unix()/3600*3600 - 3600
	since := last - 7*24*3600
	type series map[int64]*float64
	bytesBy := map[string]series{}
	if rows, err := a.store.UsageHours(since); err == nil {
		for _, r := range rows {
			if r.RxBytes == nil || r.TxBytes == nil {
				continue
			}
			v := float64(*r.RxBytes + *r.TxBytes)
			if bytesBy[r.Container] == nil {
				bytesBy[r.Container] = series{}
			}
			bytesBy[r.Container][r.Hour] = &v
		}
	}
	opensBy := map[string]series{}
	if rows, err := a.store.ActivityOpensHourly(since); err == nil {
		for c, hours := range rows {
			opensBy[c] = series{}
			for h, v := range hours {
				if v != nil {
					f := float64(*v)
					opensBy[c][h] = &f
				}
			}
		}
	}
	check := func(kind string, data map[string]series, floor float64, what string) {
		names := make([]string, 0, len(data))
		for c := range data {
			names = append(names, c)
		}
		sort.Strings(names)
		for _, c := range names {
			var history []*float64
			for h := since; h < last; h += 3600 {
				history = append(history, data[c][h])
			}
			if activity.Spike(history, data[c][last], floor) {
				a.raiseSpike(c, kind, c+" "+what+" in the last hour than 10 times its usual hour.", now)
			}
		}
	}
	check("bytes", bytesBy, activity.SpikeBytesFloor, "moved more data")
	check("opens", opensBy, activity.SpikeOpensFloor, "opened more connections")
}

func (a *App) raiseSpike(container, kind, message string, now time.Time) {
	_, err := a.store.RaiseActivityAlert(store.ActivityAlert{Kind: "activity_spike", Subject: container + ":" + kind, Message: message, CreatedAt: now.Unix()},
		time.Duration(activity.SpikeCooldownHours)*time.Hour)
	if err != nil {
		a.emitError("activity", err)
	}
}

// refreshActivityNetworks downloads the network table. One at a time.
func (a *App) refreshActivityNetworks(ctx context.Context) error {
	if a.activity.geo == nil {
		return errors.New("activity is not ready")
	}
	if !a.activity.geoBusy.TryLock() {
		return activity.ErrDownloadBusy
	}
	defer a.activity.geoBusy.Unlock()
	_, err := a.activity.geo.Download(ctx)
	return err
}
