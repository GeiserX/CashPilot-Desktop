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
