package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"modernc.org/sqlite"
)

const deployOrderTokenKey = "TRAFFMONETIZER_TOKEN"

type credentialOrderProvider struct {
	engineProvider
	runtime.ProxyProvider
	usedToken string
}

func (p *credentialOrderProvider) Deploy(ctx context.Context, spec runtime.DeploySpec, progress func(string)) (runtime.ContainerInfo, error) {
	p.usedToken = spec.Env[deployOrderTokenKey]
	return p.engineProvider.Deploy(ctx, spec, progress)
}

func (p *credentialOrderProvider) DeployProxied(_ context.Context, req runtime.ProxiedDeploy) (runtime.Exit, error) {
	p.usedToken = req.Env[deployOrderTokenKey]
	return runtime.Exit{IP: "192.0.2.10"}, nil
}

type offlineDeployTransport struct{}

func (offlineDeployTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("external IP lookup disabled in test")
}

type credentialWriteGate struct {
	entered chan struct{}
	release chan struct{}
}

var firstCredentialWrite atomic.Pointer[credentialWriteGate]

func init() {
	// Only the trigger installed in this test calls this function.
	sqlite.MustRegisterScalarFunction("hold_first_credential_write", 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
		if gate := firstCredentialWrite.Swap(nil); gate != nil {
			close(gate.entered)
			<-gate.release
		}
		return nil, nil
	})
}

func TestDeploySavesTokenAfterAnExplicitSaveAlreadyWriting(t *testing.T) {
	const slug = "traffmonetizer"
	const savedToken, deployToken = "1|explicit-save-fixture", "2|deploy-fixture"
	for _, path := range []string{"direct", "routed"} {
		t.Run(path, func(t *testing.T) {
			provider := &credentialOrderProvider{engineProvider: engineProvider{status: runtime.Status{Available: true, Reason: runtime.ReasonRunning}}}
			app := gateApp(t, provider)
			var proxyID int64
			if path == "routed" {
				client := http.DefaultClient
				http.DefaultClient = &http.Client{Transport: offlineDeployTransport{}}
				t.Cleanup(func() { http.DefaultClient = client })
				var err error
				proxyID, err = app.store.InsertProxy([]byte(`{}`))
				if err != nil {
					t.Fatal(err)
				}
			}

			db, err := sql.Open("sqlite", filepath.Join(app.cfg.DataDir(), "cashpilot-desktop.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(`CREATE TRIGGER pause_credential_write BEFORE INSERT ON credentials
				BEGIN SELECT hold_first_credential_write(); END`); err != nil {
				t.Fatal(err)
			}
			gate := &credentialWriteGate{entered: make(chan struct{}), release: make(chan struct{})}
			firstCredentialWrite.Store(gate)
			defer firstCredentialWrite.Store(nil)
			release := sync.OnceFunc(func() { close(gate.release) })
			defer release()

			saved := make(chan error, 1)
			go func() {
				saved <- app.SaveCredentials(slug, map[string]string{deployOrderTokenKey: savedToken})
			}()
			select {
			case <-gate.entered:
			case err := <-saved:
				t.Fatalf("Save Credentials returned before reaching the SQLite write: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("Save Credentials did not reach the SQLite write")
			}

			deployed := make(chan error, 1)
			go func() {
				values := map[string]string{deployOrderTokenKey: deployToken}
				if path == "routed" {
					_, err := app.DeployServiceRouted(slug, values, proxyID)
					deployed <- err
					return
				}
				_, err := app.DeployService(slug, values, nil, nil)
				deployed <- err
			}()
			waitForDeployDuringCredentialWrite(t)
			release()
			if err := <-saved; err != nil {
				t.Fatalf("Save Credentials: %v", err)
			}
			if err := <-deployed; err != nil {
				t.Fatalf("deploy: %v", err)
			}

			if provider.usedToken != deployToken {
				t.Fatalf("runtime used %q, want %q", provider.usedToken, deployToken)
			}
			got, err := app.store.GetCredentials(slug)
			if err != nil {
				t.Fatal(err)
			}
			if got[deployOrderTokenKey] != provider.usedToken {
				t.Fatalf("runtime used %q, store retained %q", provider.usedToken, got[deployOrderTokenKey])
			}
		})
	}
}

func waitForDeployDuringCredentialWrite(t *testing.T) {
	t.Helper()
	// The held write owns both credentialsMu and the store's single connection.
	// A deploy either waits for the mutex to take its snapshot or, with the bug,
	// already took the snapshot and waits for the connection to read credentials.
	buf := make([]byte, 1<<20)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for stack := range strings.SplitSeq(string(buf[:goruntime.Stack(buf, true)]), "\n\n") {
			header, _, _ := strings.Cut(stack, "\n")
			if strings.Contains(header, "[sync.Mutex.Lock]") && strings.Contains(stack, "(*deployCredentialsLock).startDeploy") ||
				strings.Contains(header, "[select]") && strings.Contains(stack, "database/sql.(*DB).conn") && strings.Contains(stack, "(*App).prepareDeployCredentials") {
				return
			}
		}
		goruntime.Gosched()
	}
	t.Fatal("deploy did not wait for the credential write")
}

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
