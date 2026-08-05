package main

import "strings"

// appVersion is this build's release version, injected at link time:
//
//	go build -ldflags "-X main.appVersion=1.2.3"
//
// It is DELIBERATELY not a constant kept in sync by hand. wails.json already
// carries productVersion and the release workflow already derives it from the
// git tag, so a second hand-maintained copy in Go would be a second source of
// truth — and the failure mode of a stale one is worse than having none at all:
// a wrong version reads as a MATCH on the fleet page and hides the very
// staleness it was added to reveal.
//
// The default is empty, which is the honest answer for a local or unstamped
// build: it does not know. Everything downstream must treat empty as UNKNOWN
// rather than as "current" — see [AppVersion].
var appVersion = ""

// AppVersion returns the release version, or "" when this build does not know.
//
// "" is UNKNOWN, and the server's own rule is that an absent version reads as
// unknown rather than as a match — both sides must be known releases before it
// will call anything a mismatch. So a `wails dev` build reports nothing and is
// correctly shown as unknown, while a released build reports its tag.
//
// The leading "v" is stripped because the tag is "v1.2.3" while every other
// version surface in this project (wails.json productVersion, package.json)
// stores the bare number. Sending both spellings for one release would make the
// fleet page show what looks like two different versions.
func AppVersion() string {
	return strings.TrimPrefix(strings.TrimSpace(appVersion), "v")
}
