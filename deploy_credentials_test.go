package main

import (
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

func TestQueuedDeployCredentials(t *testing.T) {
	for _, tc := range []struct {
		name        string
		save        bool
		saved, want string
	}{
		{name: "replace", save: true, saved: "explicit", want: "explicit"},
		{name: "keep saved secret", save: true, want: "original"},
		{name: "deploys only", want: "second"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &heldEngineProvider{
				engineProvider: engineProvider{status: runtime.Status{Available: true, Reason: runtime.ReasonRunning}},
				entered:        make(chan struct{}),
				release:        make(chan struct{}),
			}
			app := gateApp(t, provider)
			if err := app.SaveCredentials("earnfm", map[string]string{"EARNFM_TOKEN": "original"}); err != nil {
				t.Fatal(err)
			}
			var once sync.Once
			release := func() { once.Do(func() { close(provider.release) }) }
			defer release()

			done := make(chan error, 2)
			deploy := func(token string) {
				_, err := app.DeployService("earnfm", map[string]string{"EARNFM_TOKEN": token}, nil, nil)
				done <- err
			}
			go deploy("first")
			select {
			case <-provider.entered:
			case err := <-done:
				t.Fatalf("first deploy finished before reaching the runtime: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("first deploy did not reach the runtime")
			}

			go deploy("second")
			// The second deploy must be waiting on the per-service lock before
			// the explicit save. Seeing its goroutine blocked avoids a timing guess.
			buf := make([]byte, 1<<20)
			waitFor(t, "the second deploy to enter the queue", func() bool {
				stacks := string(buf[:goruntime.Stack(buf, true)])
				for _, stack := range strings.Split(stacks, "\n\n") {
					if strings.Contains(stack, "[sync.Mutex.Lock]") && strings.Contains(stack, "(*deployCredentialsLock).startDeploy") {
						return true
					}
				}
				return false
			})
			if tc.save {
				saved := make(chan error, 1)
				go func() {
					saved <- app.SaveCredentials("earnfm", map[string]string{"EARNFM_TOKEN": tc.saved})
				}()
				select {
				case err := <-saved:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("Save Credentials waited for the deploy queue")
				}
			}

			release()
			for range 2 {
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("deploy did not finish")
				}
			}
			got, err := app.store.GetCredentials("earnfm")
			if err != nil {
				t.Fatal(err)
			}
			if got["EARNFM_TOKEN"] != tc.want {
				t.Fatalf("stored token = %q, want %q", got["EARNFM_TOKEN"], tc.want)
			}
		})
	}
}
