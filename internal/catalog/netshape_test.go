package catalog

import (
	"slices"
	"testing"
)

func TestUIBindings(t *testing.T) {
	got, err := UIBindings(" 127.0.0.1 ,172.18.0.1,localhost,127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].String() != "127.0.0.1" || got[1].String() != "172.18.0.1" {
		t.Fatalf("UIBindings = %v, want 127.0.0.1 and 172.18.0.1 once each", got)
	}
	if got, _ := UIBindings(""); len(got) != 1 || got[0].String() != "127.0.0.1" {
		t.Fatalf("an empty list = %v, want loopback", got)
	}
	for _, bad := range []string{"256.0.0.1", "example.com", "::1", "127.0.0.1 10.0.0.1"} {
		if _, err := UIBindings(bad); err == nil {
			t.Errorf("UIBindings(%q) accepted it", bad)
		}
	}
}

func TestExpandUDPRange(t *testing.T) {
	got, err := ExpandUDPRange("23000:23002", 1000)
	if err != nil || !slices.Equal(got, []int{23000, 23001, 23002}) {
		t.Fatalf("ExpandUDPRange = %v, %v", got, err)
	}
	if got, err := ExpandUDPRange("  ", 1000); err != nil || got != nil {
		t.Fatalf("a blank value = %v, %v, want nothing published", got, err)
	}
	if _, err := ExpandUDPRange("1:1001", 1000); err == nil {
		t.Error("1001 ports passed a cap of 1000")
	}
	if _, err := ExpandUDPRange("1:1000", 1000); err != nil {
		t.Errorf("exactly the cap was refused: %v", err)
	}
	for _, bad := range []string{"23002:23000", "0:10", "60000:70000", "23000", "a:b"} {
		if _, err := ExpandUDPRange(bad, 1000); err == nil {
			t.Errorf("ExpandUDPRange(%q) accepted it", bad)
		}
	}
}

func TestUDPCapStaysWithinTheCeiling(t *testing.T) {
	for in, want := range map[int]int{0: 1000, -1: 1000, 5000: 1000, 30: 30} {
		if got := (UDPPortRange{MaxPorts: in}).Cap(); got != want {
			t.Errorf("Cap(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestPublishesPorts(t *testing.T) {
	if (DockerConfig{}).PublishesPorts() {
		t.Fatal("nothing declared, nothing published")
	}
	for _, d := range []DockerConfig{
		{Ports: []string{"1:1"}},
		{UIPort: &UIPort{Env: "A", Port: 1}},
		{UDPPortRange: &UDPPortRange{Env: "B"}},
	} {
		if !d.PublishesPorts() {
			t.Errorf("%+v publishes a port", d)
		}
	}
}
