package upstream

import (
	"context"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/filelock"
)

// SendLock orders the window and helper's counter reads and heartbeat sends.
// Without that order the server can mistake an older counter for a reset.
func SendLock(ctx context.Context, path string, wait time.Duration) (func(), bool) {
	return filelock.Acquire(ctx, path, wait)
}
