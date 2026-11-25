package winvm

import (
	"context"
	"time"
)

// SetGuardInterval shortens Run's pass interval for a test and returns a func
// that puts it back.
func SetGuardInterval(d time.Duration) (restore func()) {
	old := guardInterval
	guardInterval = d
	return func() { guardInterval = old }
}

// GuardLoop is Run's loop, driven one routine at a time by a test.
type GuardLoop = guardLoop

// NewGuardLoop is Run's loop over d, before its Boot.
func NewGuardLoop(d GuardDeps) *GuardLoop { return &guardLoop{d: d} }

// Boot is Run's first routine.
func (l *guardLoop) Boot(ctx context.Context) Status { return l.boot(ctx) }

// Tick is one of Run's passes after Boot.
func (l *guardLoop) Tick(ctx context.Context) Status { return l.tick(ctx) }

// OwnerSpy is a Prober that records the owner token Build hands its proof
// Prober, the way the product's Prober takes it for its canary rules and
// hv_sock test key.
type OwnerSpy struct {
	Prober
	Got *string
}

func (s OwnerSpy) forPlan(p Plan) Prober {
	*s.Got = p.Owner
	return s.Prober
}
