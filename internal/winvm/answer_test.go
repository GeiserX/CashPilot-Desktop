package winvm

import (
	"bytes"
	"encoding/xml"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// xmlSpecial is every character the answer file must escape.
const xmlSpecial = "<&>\"'"

func testAnswer() AnswerFile {
	return AnswerFile{
		ImageIndex: 6, ComputerName: "CASHPILOT-VM", GuestMACDash: "00-15-5D-0A-00-02", HostMACDash: "00-15-5D-00-00-02",
		GuestIP: netip.MustParseAddr("172.31.250.2"), Gateway: netip.MustParseAddr("172.31.250.1"), PrefixLen: 29,
		AdminPassword: testPassword(1) + xmlSpecial, EarnerPassword: testPassword(2),
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
	for _, want := range []string{testPassword(1) + "&lt;&amp;&gt;&#34;&#39;", "<NextHopAddress>172.31.250.1</NextHopAddress>",
		"MacAddress -eq '00-15-5D-0A-00-02'", "IPAddress='172.31.250.1'", "-LinkLayerAddress 00-15-5D-00-00-02 -State Permanent", "<ComputerName>CASHPILOT-VM</ComputerName>"} {
		if !strings.Contains(x, want) {
			t.Errorf("the answer file lacks %q", want)
		}
	}
	if strings.Contains(x, testPassword(1)+"<&>") {
		t.Fatal("a password went in unescaped")
	}
	bad := map[string]func(*AnswerFile){
		"image index 0":      func(a *AnswerFile) { a.ImageIndex = 0 },
		"short password":     func(a *AnswerFile) { a.AdminPassword = strings.Repeat("s", 5) },
		"space in password":  func(a *AnswerFile) { a.EarnerPassword = strings.Replace(testPassword(2), "Q", " ", 1) },
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

// testPassword is a made-up guest password for tests, built at run time from
// its number so that no password-shaped literal sits in the source.
func testPassword(n int) string { return "t" + strconv.Itoa(n) + strings.Repeat("Qz7", 6) }

// TestAnswerFileNeverStopsAtTheKeyPage: a retail ISO holds several editions.
// With the edition picked by index, Windows Setup stopped at its "Product key"
// page, both with no ProductKey element and with the all-zero key and
// WillShowUI OnError, and waited for a person nobody shows it to (live run 5).
// ProductKey with WillShowUI Never and no Key went straight to "Installing"
// on the same ISO. A real key replaces it.
func TestAnswerFileNeverStopsAtTheKeyPage(t *testing.T) {
	const placeholder = "<ProductKey><WillShowUI>Never</WillShowUI></ProductKey>"
	b, err := testAnswer().Render()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), placeholder) {
		t.Fatalf("with no key the answer file lacks %q", placeholder)
	}
	a := testAnswer()
	a.ProductKey = "ABCDE-12345-FGHIJ-67890-KLMNO"
	b, err = a.Render()
	if err != nil {
		t.Fatal(err)
	}
	x := string(b)
	if !strings.Contains(x, "<ProductKey><Key>ABCDE-12345-FGHIJ-67890-KLMNO</Key><WillShowUI>OnError</WillShowUI></ProductKey>") || strings.Count(x, "<ProductKey>") != 1 {
		t.Fatal("a given key must replace the placeholder, once")
	}
}

// TestAnswerFileCommandsFitSetupsLimit: Windows Setup caps a RunSynchronous
// Path at 259 characters and refuses the whole answer file at specialize when
// one is longer ("Value is invalid", then "The computer restarted
// unexpectedly"). On live run 5 the gateway neighbour command had 380
// characters, so no Windows install finished. Every command must fit with
// the longest values the checks allow.
func TestAnswerFileCommandsFitSetupsLimit(t *testing.T) {
	a := testAnswer()
	a.GuestMACDash, a.HostMACDash = "00-15-5D-FF-FF-FE", "00-15-5D-FF-FF-FD"
	a.GuestIP, a.Gateway, a.PrefixLen = netip.MustParseAddr("255.255.255.253"), netip.MustParseAddr("255.255.255.254"), 8
	b, err := a.Render()
	if err != nil {
		t.Fatal(err)
	}
	d := xml.NewDecoder(bytes.NewReader(b))
	var stack []string
	n := 0
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch e := tok.(type) {
		case xml.StartElement:
			stack = append(stack, e.Name.Local)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) >= 2 && stack[len(stack)-1] == "Path" && stack[len(stack)-2] == "RunSynchronousCommand" {
				n++
				if c := utf8.RuneCount(e); c > 259 {
					t.Errorf("a RunSynchronous Path has %d characters, over Setup's 259: %s", c, e)
				}
			}
		}
	}
	if n == 0 {
		t.Fatal("no RunSynchronous Path found")
	}
}
