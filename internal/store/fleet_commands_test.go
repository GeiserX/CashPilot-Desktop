package store

import (
	"reflect"
	"testing"
)

func TestAnIssuedKeyIsKeptForCommands(t *testing.T) {
	st := openTestStore(t)
	dev, err := st.UpsertFleetHeartbeat(FleetDevice{Name: "box", Kind: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	if key, _, err := st.FleetCommandKey(dev.ID); err != nil || key != "" {
		t.Fatalf("before enrolment: %q, %v", key, err)
	}
	if err := st.SetFleetDeviceIssuedKey("worker", "box", "own-key-1"); err != nil {
		t.Fatal(err)
	}
	hash, confirmed, _ := st.FleetDeviceKeyState("worker", "box")
	if hash != HashFleetKey("own-key-1") || confirmed {
		t.Fatalf("hash %q confirmed %v: the heartbeat check must keep working", hash, confirmed)
	}
	var sealed string
	_ = st.db.QueryRow(`SELECT api_key_enc FROM fleet_devices WHERE id = ?`, dev.ID).Scan(&sealed)
	if sealed == "" || sealed == "own-key-1" {
		t.Fatalf("the key must be stored encrypted, got %q", sealed)
	}
	if err := st.ConfirmFleetDeviceKey("worker", "box"); err != nil {
		t.Fatal(err)
	}
	key, confirmed, err := st.FleetCommandKey(dev.ID)
	if err != nil || key != "own-key-1" || !confirmed {
		t.Fatalf("got %q %v %v", key, confirmed, err)
	}
	// A hash-only reissue (an older code path) leaves a stale copy: it is not used.
	if err := st.SetFleetDeviceKey("worker", "box", HashFleetKey("other")); err != nil {
		t.Fatal(err)
	}
	if key, _, _ := st.FleetCommandKey(dev.ID); key != "" {
		t.Fatalf("a key that no longer matches the hash was returned: %q", key)
	}
	if err := st.SetFleetDeviceIssuedKey("worker", "missing", "k"); err == nil {
		t.Fatal("a key for a device with no row must be refused")
	}
}

func TestTheReportFollowsTheCommands(t *testing.T) {
	st := openTestStore(t)
	dev, err := st.UpsertFleetHeartbeat(FleetDevice{Name: "box", Kind: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	report := FleetReport{ClientID: "c1", Features: []string{"instances"}, Containers: []FleetContainer{
		{Slug: "a", Status: "running", Instance: 1, Managed: true},
		{Slug: "a", Status: "running", Instance: 2, Managed: true},
		{Slug: "b", Status: "running", Instance: 1, Managed: true},
	}}
	if err := st.SetFleetReport("worker", "box", report); err != nil {
		t.Fatal(err)
	}
	reports, err := st.FleetReports()
	if err != nil || !reflect.DeepEqual(reports[dev.ID], report) {
		t.Fatalf("read back %+v, %v", reports[dev.ID], err)
	}
	step := func(slug string, n int, status string, want []FleetContainer) {
		t.Helper()
		if err := st.MarkFleetCopy(dev.ID, slug, n, status); err != nil {
			t.Fatal(err)
		}
		got, _ := st.FleetReports()
		if !reflect.DeepEqual(got[dev.ID].Containers, want) {
			t.Fatalf("%s %d %q: %+v", slug, n, status, got[dev.ID].Containers)
		}
	}
	step("a", 2, "exited", []FleetContainer{{"a", "running", 1, true}, {"a", "exited", 2, true}, {"b", "running", 1, true}})
	step("a", 2, "", []FleetContainer{{"a", "running", 1, true}, {"b", "running", 1, true}})
	step("c", 1, "running", []FleetContainer{{"a", "running", 1, true}, {"b", "running", 1, true}, {"c", "running", 1, true}})
	step("a", 1, "", []FleetContainer{{"b", "running", 1, true}, {"c", "running", 1, true}})
}
