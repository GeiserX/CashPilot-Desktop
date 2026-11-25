package winvm

import "time"

// SetGuardInterval shortens Run's pass interval for a test and returns a func
// that puts it back.
func SetGuardInterval(d time.Duration) (restore func()) {
	old := guardInterval
	guardInterval = d
	return func() { guardInterval = old }
}
