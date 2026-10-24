package main

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// TestTheAppRunsTraffmonetizerThreeTimes scales the real Traffmonetizer entry through
// the bindings the interface calls, against the fake daemon. Its command line carries
// the device name, so it is the entry where a copy built from the first copy's env
// would register three containers as one device.
//
// It also follows each copy's number from the daemon's container list to the JSON
// the interface reads, and back into a Stop: a number dropped on the way would show
// every copy as #1 and point its buttons at the first copy.
func TestTheAppRunsTraffmonetizerThreeTimes(t *testing.T) {
	app := newAppUnderTest(t)
	creds := map[string]string{"TRAFFMONETIZER_TOKEN": "not-a-real-token"}
	three := 3
	deployment, err := app.DeployService("traffmonetizer", creds, nil, &three)
	if err != nil {
		t.Fatalf("DeployService(3): %v", err)
	}
	if deployment.Instances != 3 {
		t.Fatalf("Instances = %d, want 3", deployment.Instances)
	}

	device := "cashpilot-" + runtime.DeviceHostname()
	for name, want := range map[string]string{
		"cashpilot-traffmonetizer":   device,
		"cashpilot-traffmonetizer-2": device + "-2",
		"cashpilot-traffmonetizer-3": device + "-3",
	} {
		c, ok := app.Docker.Container(name)
		if !ok {
			t.Fatalf("%s is not running; have %v", name, app.Docker.Names())
		}
		i := slices.Index(c.Cmd, "--device-name")
		if i < 0 || i+1 >= len(c.Cmd) || c.Cmd[i+1] != want {
			t.Errorf("%s runs %v, want --device-name %s", name, c.Cmd, want)
		}
	}

	if _, err := app.RefreshDeployments(); err != nil {
		t.Fatalf("RefreshDeployments: %v", err)
	}
	state, err := app.GetAppState()
	if err != nil {
		t.Fatalf("GetAppState: %v", err)
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal AppState: %v", err)
	}
	var wire struct {
		Copies  map[string][]struct{ Instance int } `json:"copies"`
		Scaling map[string]struct{ Max int }        `json:"scaling"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal AppState: %v", err)
	}
	copies := wire.Copies["traffmonetizer"]
	if len(copies) != 2 || copies[0].Instance != 2 || copies[1].Instance != 3 {
		t.Fatalf("the interface reads copies %+v, want instances 2 and 3", copies)
	}
	if wire.Scaling["traffmonetizer"].Max != 10 {
		t.Fatalf("the Instances box ceiling is %d, want 10", wire.Scaling["traffmonetizer"].Max)
	}

	// The number the interface read is the copy the Stop reaches.
	if err := app.StopServiceCopy("traffmonetizer", copies[1].Instance); err != nil {
		t.Fatalf("StopServiceCopy: %v", err)
	}
	for name, want := range map[string]string{
		"cashpilot-traffmonetizer":   "running",
		"cashpilot-traffmonetizer-2": "running",
		"cashpilot-traffmonetizer-3": "exited",
	} {
		if c, _ := app.Docker.Container(name); c.State != want {
			t.Errorf("after stopping copy 3, %s is %s, want %s", name, c.State, want)
		}
	}
	if err := app.StartServiceCopy("traffmonetizer", 3); err != nil {
		t.Fatalf("StartServiceCopy: %v", err)
	}
	if _, err := app.GetCopyLogs("traffmonetizer", 2, 20); err != nil {
		t.Fatalf("GetCopyLogs: %v", err)
	}

	if err := app.RemoveService("traffmonetizer", false, false); err != nil {
		t.Fatalf("RemoveService: %v", err)
	}
	if names := app.Docker.Names(); len(names) != 0 {
		t.Fatalf("containers left after RemoveService: %v", names)
	}
}
