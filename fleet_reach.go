package main

import (
	"net"
	"strconv"
)

// FleetReach says whether another machine can report to this Desktop's fleet API
// right now, so the Fleet page offers a worker snippet only when one could work.
//
// Two things must hold. The hub switch (Accept workers) is on, or every heartbeat
// gets 403. And the API listens on an address other machines reach, not loopback.
// Both are read from what runs now: the bind address and port take effect only
// after a restart, so a saved change the listener has not picked up yet is reported
// apart and never counted as open.
type FleetReach struct {
	// Open is true when a worker or phone on the LAN or a tailnet can report here.
	Open bool `json:"open"`
	// HubOff: Accept workers (hub mode) is off, so heartbeats are refused.
	HubOff bool `json:"hubOff"`
	// ThisPCOnly: the API listens on a loopback address, so only this PC reaches it.
	ThisPCOnly bool `json:"thisPcOnly"`
	// NotListening: the API did not start, for instance because its port was taken.
	NotListening bool `json:"notListening"`
	// Listen is the address the API listens on now, "" when it does not.
	Listen string `json:"listen"`
	// RestartPending: the saved bind address or port differs from the running
	// listener, and takes effect after CashPilot restarts.
	RestartPending bool `json:"restartPending"`
}

// fleetReach decides FleetReach. listen is the running listener's address
// ("" when the API is not running); bind and port are what Settings hold.
func fleetReach(hubOn bool, listen, bind string, port int) FleetReach {
	r := FleetReach{Listen: listen, HubOff: !hubOn, NotListening: listen == ""}
	if listen != "" {
		host, livePort, err := net.SplitHostPort(listen)
		if err == nil {
			r.ThisPCOnly = isLoopbackHost(host)
			r.RestartPending = livePort != strconv.Itoa(port) || !sameBind(host, bind)
		}
	}
	r.Open = !r.HubOff && !r.NotListening && !r.ThisPCOnly
	return r
}

// isLoopbackHost reports an address only this machine reaches.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// sameBind reports whether the live listener's host is the one Settings ask for.
// An empty or wildcard bind listens on every address, which Go reports as "::"
// or "0.0.0.0" depending on the system.
func sameBind(live, bind string) bool {
	if isWildcardHost(live) && isWildcardHost(bind) {
		return true
	}
	a, b := net.ParseIP(live), net.ParseIP(bind)
	if a != nil && b != nil {
		return a.Equal(b)
	}
	return live == bind
}

func isWildcardHost(host string) bool {
	if host == "" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsUnspecified()
}

// fleetURLFor is the URL a worker should use for an API listening on listen
// ("host:port"). A wildcard listener answers on every address, so the URL names
// this PC's LAN address (lanAddress, "" when none is known); any other listener
// answers only on its own address, so the URL names that one. A loopback
// listener gets a loopback URL: that is the truth, and no other machine can use it.
func fleetURLFor(listen string, lanAddress func() string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "http://" + listen
	}
	if isWildcardHost(host) {
		host = lanAddress()
		if host == "" {
			host = "127.0.0.1"
		}
	}
	return "http://" + net.JoinHostPort(host, port)
}

// fleetListenAddr is the address the fleet API listens on now, "" when it does not run.
func (a *App) fleetListenAddr() string {
	if a.fleetAPI == nil {
		return ""
	}
	return a.fleetAPI.addr
}

// fleetReachNow is fleetReach for this app as it runs.
func (a *App) fleetReachNow() FleetReach {
	cfg := a.cfg.Config()
	return fleetReach(cfg.FleetServerEnabled, a.fleetListenAddr(), cfg.FleetBindAddress, cfg.FleetPort)
}
