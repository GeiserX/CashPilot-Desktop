package winvm

import (
	"testing"
	"time"
)

func TestHVListenerShutdownWaitsForAcceptWithoutClosingItsSocket(t *testing.T) {
	stop := make(chan struct{})
	polling := make(chan struct{})
	resume := make(chan struct{})
	closed := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = hvAcceptLoop(stop, func() (bool, error) {
			close(polling)
			<-resume
			return false, nil
		}, func() error {
			close(closed)
			return nil
		})
	}()
	<-polling
	close(stop)
	err := waitHVShutdown(done, 10*time.Millisecond)
	select {
	case <-closed:
		t.Error("socket closed while an accept was in progress")
	default:
	}
	close(resume)
	if err == nil {
		t.Error("shutdown ignored the deadline")
	}
	if err := waitHVShutdown(done, time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	default:
		t.Fatal("the listener leaked the socket")
	}
}
