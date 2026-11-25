package main

import (
	"context"
	"encoding/hex"
	"log"
	"path/filepath"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/upstream"
)

// The heartbeat's traffic counters. A CashPilot server judges a copy for traffic
// stalls (web app/traffic_stall.py) from the per-container counters each worker
// sends, so this computer sends them as the web worker does: every copy, with its
// cumulative counters, and the proxied copies with their tunnel's counters. The
// reading is the one the usage graphs take (services.List), taken live for each
// heartbeat, as the web worker takes get_status for each of its own.

// heartbeatLockWait bounds the wait for the other process's turn (upstream.SendLock):
// its reading and up to two sends (a refused key enrols again), each bounded by the
// client's 30 seconds. A var so tests can shorten it.
var heartbeatLockWait = 45 * time.Second

// heartbeatReads is the live reading each heartbeat takes: every copy, and the
// proxied copies. A var so tests can keep the runtime out.
var heartbeatReads = func(a *App) (func(context.Context) ([]runtime.ContainerInfo, error), func(context.Context) ([]runtime.ProxiedCopy, error)) {
	return a.services.List, a.services.ReadProxiedCopies
}

// heartbeatLockPath is the lock file the window and the helper share.
func (a *App) heartbeatLockPath() string {
	return filepath.Join(a.cfg.DataDir(), "heartbeat.lock")
}

// withLiveUnits takes this process's turn to send and puts a live reading in the
// payload: every copy with its counters in Containers, and the proxied copies in
// SystemInfo.Proxied. It returns the release of the turn, which the caller calls
// once the server answered, so the server gets the readings of the two processes in
// the order they were taken.
//
// What could not be read keeps the payload as it was: a failed list keeps the
// stored copies without counters, and failed proxied copies go out as null, so the
// server does not read them as gone. Without the turn nothing is read at all.
func (a *App) withLiveUnits(ctx context.Context, p *upstream.Payload,
	list func(context.Context) ([]runtime.ContainerInfo, error),
	proxied func(context.Context) ([]runtime.ProxiedCopy, error)) (release func()) {
	release, ok := upstream.SendLock(ctx, a.heartbeatLockPath(), heartbeatLockWait)
	if !ok {
		log.Printf("upstream: the other CashPilot process did not give up its turn to send; this heartbeat carries no traffic counters")
		return release
	}
	if infos, err := list(ctx); err == nil {
		p.Containers = heartbeatContainers(infos)
	}
	if copies, err := proxied(ctx); err == nil {
		p.SystemInfo.Proxied = heartbeatProxied(copies)
	} else {
		p.SystemInfo.Proxied = nil
	}
	return release
}

// heartbeatContainers is every copy in the web worker's shape. A counter or a
// figure that was not read stays null.
func heartbeatContainers(infos []runtime.ContainerInfo) []upstream.Container {
	out := make([]upstream.Container, 0, len(infos))
	for _, c := range infos {
		if c.Slug == "" {
			continue
		}
		item := upstream.Container{
			Slug: c.Slug, Name: c.Name, Status: c.Status,
			ContainerID: shortContainerID(c.ContainerID), Instance: max(c.Instance, 1),
		}
		if c.CPUKnown {
			cpu := c.CPUPercent
			item.CPUPercent = &cpu
		}
		if c.MemoryKnown {
			mem := c.MemoryMB
			item.MemoryMB = &mem
		}
		if c.NetRxBytes != nil && c.NetTxBytes != nil {
			rx, tx := *c.NetRxBytes, *c.NetTxBytes
			item.NetRxBytes, item.NetTxBytes = &rx, &tx
		}
		out = append(out, item)
	}
	return out
}

// heartbeatProxied is every proxied copy in the web worker's shape, an empty list
// when there is none.
func heartbeatProxied(copies []runtime.ProxiedCopy) []upstream.Proxied {
	out := make([]upstream.Proxied, 0, len(copies))
	for _, c := range copies {
		if c.Slug == "" {
			continue
		}
		item := upstream.Proxied{
			Slug: c.Slug, ProxyID: c.ProxyID, Name: c.Name, Status: c.Status,
			ContainerID:       shortContainerID(c.ContainerID),
			TunnelStatus:      c.TunnelStatus,
			TunnelContainerID: shortContainerID(c.TunnelContainerID),
			TunnelStartedAt:   c.TunnelStartedAt,
			ExitIP:            c.ExitIP, ExitCountry: c.ExitCountry,
		}
		if c.NetRxBytes != nil && c.NetTxBytes != nil {
			rx, tx := *c.NetRxBytes, *c.NetTxBytes
			item.NetRxBytes, item.NetTxBytes = &rx, &tx
		}
		out = append(out, item)
	}
	return out
}

// shortContainerID is Docker's 12-character short id, which the web worker sends,
// for a full 64-character id. Any other id (a native program's) is sent as it is.
func shortContainerID(id string) string {
	if len(id) != 64 {
		return id
	}
	if _, err := hex.DecodeString(id); err != nil {
		return id
	}
	return id[:12]
}

// proxiedUnits is the proxied copies as units for the usage graphs and the stall
// check. Their counters are their tunnel's, the container every byte of the earner
// crosses, so the tunnel's id keys the counter baseline: a new tunnel starts a new
// baseline. CPU and memory are not read for them.
func proxiedUnits(copies []runtime.ProxiedCopy) []runtime.ContainerInfo {
	out := make([]runtime.ContainerInfo, 0, len(copies))
	for _, c := range copies {
		if c.Slug == "" || c.Name == "" {
			continue
		}
		out = append(out, runtime.ContainerInfo{
			Slug: c.Slug, Name: c.Name, Status: c.Status, ContainerID: c.TunnelContainerID,
			NetRxBytes: c.NetRxBytes, NetTxBytes: c.NetTxBytes, ProxyID: c.ProxyID,
		})
	}
	return out
}

// withProxiedUnits makes one reading of every unit for the usage sampler: the
// service's own copies (list) and the proxied copies. A proxied read that fails
// fails the reading, so no stall alert of a proxied copy is cleared as gone while
// it may still run.
func withProxiedUnits(list func(context.Context) ([]runtime.ContainerInfo, error),
	proxied func(context.Context) ([]runtime.ProxiedCopy, error)) func(context.Context) ([]runtime.ContainerInfo, error) {
	return func(ctx context.Context) ([]runtime.ContainerInfo, error) {
		infos, err := list(ctx)
		if err != nil {
			return nil, err
		}
		copies, err := proxied(ctx)
		if err != nil {
			return nil, err
		}
		return append(infos, proxiedUnits(copies)...), nil
	}
}
