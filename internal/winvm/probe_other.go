//go:build !windows

package winvm

import (
	"context"
	"errors"
)

// otherProbes is every OS but Windows: there is nothing to read, so a Snapshot
// reports Supported false with every fact Unknown.
type otherProbes struct{}

var system probes = otherProbes{}

var errUnsupported = errors.New("host checks run only on Windows")

func (otherProbes) supported() bool { return false }

func (otherProbes) osVersion(context.Context) (func(*HostFacts), error) { return nil, errUnsupported }
func (otherProbes) processor(context.Context) (func(*HostFacts), error) { return nil, errUnsupported }
func (otherProbes) cpu(context.Context) (func(*HostFacts), error)       { return nil, errUnsupported }
func (otherProbes) features(context.Context) (func(*HostFacts), error)  { return nil, errUnsupported }
func (otherProbes) wsl(context.Context) (func(*HostFacts), error)       { return nil, errUnsupported }
func (otherProbes) docker(context.Context) (func(*HostFacts), error)    { return nil, errUnsupported }
func (otherProbes) account(context.Context) (func(*HostFacts), error)   { return nil, errUnsupported }
func (otherProbes) restart(context.Context) (func(*HostFacts), error)   { return nil, errUnsupported }
