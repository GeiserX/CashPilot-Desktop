package main

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/activity"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// ActivityTop is one value of a dimension and how often it was seen.
type ActivityTop struct {
	Value     string `json:"value"`
	Sightings int64  `json:"sightings"`
}

// ActivityRow is one container on the Activity page: its newest sample, its
// series and its most seen ports, networks and countries.
type ActivityRow struct {
	Container        string                `json:"container"`
	Status           string                `json:"status"`
	Reason           string                `json:"reason"`
	Members          []string              `json:"members"`
	NetworkMode      string                `json:"networkMode"`
	ListenPorts      []int                 `json:"listenPorts"`
	LastWindow       int64                 `json:"lastWindow"`
	EstMax           *int64                `json:"estMax"`
	RemotesTotal     *int64                `json:"remotesTotal"`
	Truncated        bool                  `json:"truncated"`
	OpensLast        *int64                `json:"opensLast"`
	AttemptFailsLast *int64                `json:"attemptFailsLast"`
	Series           []store.ActivityPoint `json:"series"`
	TopPorts         []ActivityTop         `json:"topPorts"`
	TopASN           []ActivityTop         `json:"topAsn"`
	TopCC            []ActivityTop         `json:"topCc"`
	// Classes is outbound only: an inbound row from the LAN is the owner
	// reaching the earner, not the earner reaching the LAN.
	Classes map[string]int64 `json:"classes"`
	Dirs    map[string]int64 `json:"dirs"`
	Alerts  int              `json:"alerts"`
}

// ActivityView is the whole Activity page.
type ActivityView struct {
	Enabled         bool                  `json:"enabled"`
	Days            int                   `json:"days"`
	RawHours        int                   `json:"rawHours"`
	LastWindow      int64                 `json:"lastWindow"`
	Round           activityRound         `json:"round"`
	Networks        activity.IPGeoStatus  `json:"networks"`
	NetworksEnabled bool                  `json:"networksEnabled"`
	Containers      []ActivityRow         `json:"containers"`
	Alerts          []store.ActivityAlert `json:"alerts"`
}

// ActivityDetail is one container's expanded view: every port, network and
// country of the period, and the raw destinations of the last 24 hours.
type ActivityDetail struct {
	Container string                 `json:"container"`
	Ports     []ActivityTop          `json:"ports"`
	ASN       []ActivityTop          `json:"asn"`
	CC        []ActivityTop          `json:"cc"`
	Classes   []ActivityTop          `json:"classes"`
	Raw       []store.ActivityRawRow `json:"raw"`
	RawHours  int                    `json:"rawHours"`
}

func clampActivityDays(days int) int {
	switch {
	case days <= 1:
		return 1
	case days >= 30:
		return 30
	default:
		return 7
	}
}

func tops(dims []store.ActivityDim, container, dim string, n int) []ActivityTop {
	var out []ActivityTop
	for _, d := range dims {
		if d.Container == container && d.Dim == dim {
			out = append(out, ActivityTop{Value: d.Value, Sightings: d.Sightings})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Sightings != out[j].Sightings {
			return out[i].Sightings > out[j].Sightings
		}
		return out[i].Value < out[j].Value
	})
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	if out == nil {
		out = []ActivityTop{}
	}
	return out
}

// buildActivityRows is the page's rows from what the store returns. Pure, so
// it is tested without a database.
func buildActivityRows(latest []store.ActivitySample, series []store.ActivityPoint, dims []store.ActivityDim, alerts []store.ActivityAlert) []ActivityRow {
	out := make([]ActivityRow, 0, len(latest))
	for _, l := range latest {
		row := ActivityRow{
			Container: l.Container, Status: l.Status, Reason: l.Reason, Members: l.Members, NetworkMode: l.NetworkMode,
			ListenPorts: l.ListenPorts, LastWindow: l.WindowEnd, EstMax: l.EstMax, RemotesTotal: l.RemotesTotal,
			Truncated: l.Truncated, OpensLast: l.Opens, AttemptFailsLast: l.AttemptFails,
			Series: []store.ActivityPoint{}, Classes: map[string]int64{}, Dirs: map[string]int64{},
		}
		for _, p := range series {
			if p.Container == l.Container {
				row.Series = append(row.Series, p)
			}
		}
		row.TopPorts = tops(dims, l.Container, "port", 5)
		row.TopASN = tops(dims, l.Container, "asn", 5)
		row.TopCC = tops(dims, l.Container, "cc", 5)
		for _, t := range tops(dims, l.Container, "oclass", 0) {
			row.Classes[t.Value] = t.Sightings
		}
		for _, t := range tops(dims, l.Container, "dir", 0) {
			row.Dirs[t.Value] = t.Sightings
		}
		for _, a := range alerts {
			if strings.SplitN(a.Subject, ":", 2)[0] == l.Container {
				row.Alerts++
			}
		}
		out = append(out, row)
	}
	return out
}

// GetActivity is the Activity page over the last days (1, 7 or 30).
func (a *App) GetActivity(days int) (ActivityView, error) {
	if err := a.ready(); err != nil {
		return ActivityView{}, err
	}
	days = clampActivityDays(days)
	now := time.Now()
	since := now.Unix() - int64(days)*86400
	latest, err := a.store.ActivityLatest(since)
	if err != nil {
		return ActivityView{}, err
	}
	series, err := a.store.ActivitySeries(since, days > 1, "")
	if err != nil {
		return ActivityView{}, err
	}
	dims, err := a.store.ActivityDims(since/3600*3600, "")
	if err != nil {
		return ActivityView{}, err
	}
	alerts, err := a.store.ActivityAlerts(since)
	if err != nil {
		return ActivityView{}, err
	}
	view := ActivityView{
		Enabled: a.activityOn(), Days: days, RawHours: store.ActivityRawSeconds / 3600,
		Containers: buildActivityRows(latest, series, dims, alerts), Alerts: alerts, NetworksEnabled: true,
	}
	if view.Alerts == nil {
		view.Alerts = []store.ActivityAlert{}
	}
	for _, l := range latest {
		view.LastWindow = max(view.LastWindow, l.WindowEnd)
	}
	a.activity.mu.Lock()
	view.Round = a.activity.last
	a.activity.mu.Unlock()
	if view.Round.LeftoverProbes == nil {
		view.Round.LeftoverProbes = []string{}
	}
	if a.activity.geo != nil {
		view.Networks = a.activity.geo.Status()
	}
	return view, nil
}

// GetActivityDetail is one container's expanded row.
func (a *App) GetActivityDetail(container string, days int) (ActivityDetail, error) {
	if err := a.ready(); err != nil {
		return ActivityDetail{}, err
	}
	days = clampActivityDays(days)
	now := time.Now()
	dims, err := a.store.ActivityDims((now.Unix()-int64(days)*86400)/3600*3600, container)
	if err != nil {
		return ActivityDetail{}, err
	}
	raw, err := a.store.ActivityRaw(container, now)
	if err != nil {
		return ActivityDetail{}, err
	}
	if !a.activityOn() {
		// Switching off deletes the addresses; a read racing that delete shows none.
		raw = nil
	}
	if raw == nil {
		raw = []store.ActivityRawRow{}
	}
	return ActivityDetail{
		Container: container, Ports: tops(dims, container, "port", 15), ASN: tops(dims, container, "asn", 15),
		CC: tops(dims, container, "cc", 15), Classes: tops(dims, container, "class", 15), Raw: raw, RawHours: store.ActivityRawSeconds / 3600,
	}, nil
}

// SetActivityEnabled switches sampling on or off. Off deletes every remote
// address at once; if that delete fails the switch stays off and the error
// says so.
func (a *App) SetActivityEnabled(on bool) (ActivityView, error) {
	if err := a.ready(); err != nil {
		return ActivityView{}, err
	}
	cfg := a.cfg.Config()
	was := cfg.ActivityEnabled
	cfg.ActivityEnabled = on
	if on {
		// Under the write lock, so a pending switch-off retry cannot run between
		// its "still off?" check and its delete.
		a.activity.writeMu.Lock()
		saveErr := a.cfg.Save(cfg)
		if saveErr == nil {
			a.activity.deletePending = false
		}
		a.activity.writeMu.Unlock()
		if saveErr != nil {
			return ActivityView{}, saveErr
		}
	} else {
		// Saved and deleted under the write lock, so a round already running
		// stores nothing once the switch is off.
		var saveErr error
		delErr := a.activityDelete(func() error {
			if saveErr = a.cfg.Save(cfg); saveErr != nil {
				return nil
			}
			err := a.deleteActivityRows(false)
			// A failed delete is retried on every tick while the switch stays off.
			a.activity.deletePending = err != nil
			return err
		})
		if saveErr != nil {
			return ActivityView{}, saveErr
		}
		if delErr != nil {
			return ActivityView{}, errors.New("Activity is off, but its remote addresses could not be deleted yet: " + delErr.Error())
		}
	}
	if a.activity.deltas != nil && on != was {
		a.activity.deltas.Reset()
	}
	if on && !was && a.ctx != nil {
		go func() {
			if a.activity.geo != nil && a.activity.geo.NeedsRefresh() {
				_ = a.refreshActivityNetworks(a.ctx)
			}
		}()
	}
	return a.GetActivity(1)
}

// deleteActivityRows deletes and says so when the database log kept a copy.
func (a *App) deleteActivityRows(all bool) error {
	busy, err := a.store.DeleteActivity(all)
	if err == nil && busy {
		a.emitNotice("activity", activityBusyNotice)
	}
	return err
}

// DeleteActivityData deletes every Activity row: samples, totals, addresses and alerts.
func (a *App) DeleteActivityData() (ActivityView, error) {
	if err := a.ready(); err != nil {
		return ActivityView{}, err
	}
	if err := a.activityDelete(func() error { return a.deleteActivityRows(true) }); err != nil {
		return ActivityView{}, err
	}
	return a.GetActivity(1)
}

// RefreshActivityNetworks downloads the network table now.
func (a *App) RefreshActivityNetworks() (ActivityView, error) {
	if err := a.ready(); err != nil {
		return ActivityView{}, err
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if err := a.refreshActivityNetworks(ctx); err != nil {
		return ActivityView{}, err
	}
	return a.GetActivity(1)
}

// activityNotifications are the alerts of the last day, for the bell.
func (a *App) activityNotifications() []Notification {
	if a.store == nil {
		return nil
	}
	alerts, err := a.store.ActivityAlerts(time.Now().Unix() - 86400)
	if err != nil {
		return nil
	}
	var out []Notification
	for _, al := range alerts {
		out = append(out, Notification{Level: "warning", Title: "Activity", Message: al.Message})
	}
	return out
}
