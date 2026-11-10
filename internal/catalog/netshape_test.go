package catalog

import (
	"slices"
	"strings"
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
	for in, want := range map[int]int{0: MaxUDPPorts, -1: MaxUDPPorts, 5000: MaxUDPPorts, MaxUDPPorts + 1: MaxUDPPorts, 30: 30} {
		if got := (UDPPortRange{MaxPorts: in}).Cap(); got != want {
			t.Errorf("Cap(%d) = %d, want %d", in, got, want)
		}
	}
}

// The web catalog's ceiling (app/catalog.py MAX_UDP_PORTS): 300 ports start in
// about 12 s, 1,000 in about 51 s.
func TestTheUDPCeilingIsTheWebCatalogs(t *testing.T) {
	if MaxUDPPorts != 300 {
		t.Fatalf("MaxUDPPorts = %d, want the web catalog's 300", MaxUDPPorts)
	}
}

// A range over the cap is refused with what to do instead: a range of the cap from
// the same first port, and the router forward to match.
func TestARangeOverTheCapSaysHowToNarrowIt(t *testing.T) {
	_, err := ExpandUDPRange("23000:23999", 300)
	if err == nil {
		t.Fatal("1000 ports passed a cap of 300")
	}
	for _, want := range []string{"spans 1000 ports", "at most 300", "such as 23000:23299", "router"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err, want)
		}
	}
	// Near the top of the port space the example stays a valid range.
	if _, err := ExpandUDPRange("65400:65535", 100); err == nil || !strings.Contains(err.Error(), "such as 65400:65499") {
		t.Errorf("error = %v, want the example 65400:65499", err)
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
