package winvm

import (
	"net/netip"
	"strings"
	"testing"
)

func testAnswer() AnswerFile {
	return AnswerFile{
		ImageIndex: 6, ComputerName: "CASHPILOT-VM", GuestMACDash: "00-15-5D-0A-00-02", HostMACDash: "00-15-5D-00-00-02",
		GuestIP: netip.MustParseAddr("172.31.250.2"), Gateway: netip.MustParseAddr("172.31.250.1"), PrefixLen: 29,
		AdminPassword: `Adm1n<&>"'-Pass-01`, EarnerPassword: "Earn3r-Pass-0123456789",
	}
}

// TestAnswerFileEscapesAndChecks: every value is XML-escaped, so a password
// with markup characters cannot change the document, and every value is
// checked before it goes in.
func TestAnswerFileEscapesAndChecks(t *testing.T) {
	b, err := testAnswer().Render()
	if err != nil {
		t.Fatal(err)
	}
	x := string(b)
	for _, want := range []string{`Adm1n&lt;&amp;&gt;&#34;&#39;-Pass-01`, "<NextHopAddress>172.31.250.1</NextHopAddress>",
		"-IPAddress 172.31.250.1 -LinkLayerAddress 00-15-5D-00-00-02", "<ComputerName>CASHPILOT-VM</ComputerName>"} {
		if !strings.Contains(x, want) {
			t.Errorf("the answer file lacks %q", want)
		}
	}
	if strings.Contains(x, `Adm1n<&>`) {
		t.Fatal("a password went in unescaped")
	}
	bad := map[string]func(*AnswerFile){
		"image index 0":      func(a *AnswerFile) { a.ImageIndex = 0 },
		"short password":     func(a *AnswerFile) { a.AdminPassword = "short" },
		"space in password":  func(a *AnswerFile) { a.EarnerPassword = "Earn3r Pass 0123456789" },
		"lowercase name":     func(a *AnswerFile) { a.ComputerName = "cashpilot" },
		"colon MAC":          func(a *AnswerFile) { a.GuestMACDash = "00:15:5D:0A:00:02" },
		"IPv6 guest":         func(a *AnswerFile) { a.GuestIP = netip.MustParseAddr("fe80::1") },
		"bad key":            func(a *AnswerFile) { a.ProductKey = "<Key/>" },
		"markup in the zone": func(a *AnswerFile) { a.TimeZone = "UTC</TimeZone>" },
	}
	for name, edit := range bad {
		a := testAnswer()
		edit(&a)
		if _, err := a.Render(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
