package winvm

import (
	"errors"
	"time"
)

// hvAcceptLoop owns the listener socket. poll performs one nonblocking
// accept and reports whether it accepted a connection.
func hvAcceptLoop(stop <-chan struct{}, poll func() (bool, error), closeSocket func() error) error {
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return closeSocket()
		default:
		}
		accepted, err := poll()
		if err != nil {
			return errors.Join(err, closeSocket())
		}
		if !accepted {
			select {
			case <-stop:
				return closeSocket()
			case <-tick.C:
			}
		}
	}
}

func waitHVShutdown(done <-chan struct{}, limit time.Duration) error {
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		return errors.New("timed out closing the hv_sock listener")
	}
}
