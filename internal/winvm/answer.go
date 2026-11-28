package winvm

import (
	"bytes"
	_ "embed"
	"encoding/xml"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"text/template"
)

//go:embed scripts/autounattend.tpl.xml
var answerTemplate string

// answerTpl renders the answer file. x escapes a value for XML text.
var answerTpl = template.Must(template.New("autounattend").Funcs(template.FuncMap{
	"x": func(v any) (string, error) {
		var b bytes.Buffer
		if err := xml.EscapeText(&b, []byte(fmt.Sprint(v))); err != nil {
			return "", err
		}
		return b.String(), nil
	},
}).Parse(answerTemplate))

// AnswerFile is what goes into the guest's answer file.
type AnswerFile struct {
	ImageIndex     int
	ProductKey     string // "": no key and no key page, so Setup takes the edition from ImageIndex
	ComputerName   string
	TimeZone       string // a Windows time zone ID, "" for Setup's default
	GuestMACDash   string // 00-15-5D-..
	HostMACDash    string // the host vNIC: the guest's static ARP entry for the gateway points here
	GuestIP        netip.Addr
	Gateway        netip.Addr
	PrefixLen      int
	AdminPassword  string // temporary: the first session replaces it
	EarnerPassword string // temporary: the first session replaces it
}

var (
	productKeyRe = regexp.MustCompile(`^[0-9A-Z]{5}(-[0-9A-Z]{5}){4}$`)
	timeZoneRe   = regexp.MustCompile(`^[A-Za-z0-9 .()+\-]{1,64}$`)
	computerRe   = regexp.MustCompile(`^[A-Z0-9-]{1,15}$`)
)

// checkPassword accepts 16 to 128 printable ASCII characters, so a password
// survives the answer file, PowerShell Direct and the guest's UAC prompt.
func checkPassword(what, pw string) error {
	if len(pw) < 16 || len(pw) > 128 {
		return fmt.Errorf("the %s password must have 16 to 128 characters", what)
	}
	for _, c := range pw {
		if c < 0x21 || c > 0x7e {
			return fmt.Errorf("the %s password may hold only printable ASCII characters, no spaces", what)
		}
	}
	return nil
}

// macDash writes a normalized MAC as 00-15-5D-0A-0B-0C, the form Windows
// Setup and New-NetNeighbor use.
func macDash(m string) string {
	var parts []string
	for i := 0; i+2 <= len(m); i += 2 {
		parts = append(parts, m[i:i+2])
	}
	return strings.Join(parts, "-")
}

// Render checks every value and writes the answer file.
func (a AnswerFile) Render() ([]byte, error) {
	if a.ImageIndex < 1 || a.ImageIndex > 32 {
		return nil, fmt.Errorf("image index %d is outside 1..32", a.ImageIndex)
	}
	if a.ProductKey != "" && !productKeyRe.MatchString(a.ProductKey) {
		return nil, fmt.Errorf("the product key is not five groups of five letters and digits")
	}
	if a.TimeZone != "" && !timeZoneRe.MatchString(a.TimeZone) {
		return nil, fmt.Errorf("time zone %q is not a Windows time zone ID", a.TimeZone)
	}
	if !computerRe.MatchString(a.ComputerName) {
		return nil, fmt.Errorf("computer name %q is not allowed", a.ComputerName)
	}
	for _, m := range []string{a.GuestMACDash, a.HostMACDash} {
		if n, err := NormalizeMAC(m); err != nil || macDash(n) != m {
			return nil, fmt.Errorf("MAC %q is not in the 00-15-5D-0A-0B-0C form", m)
		}
	}
	if !a.GuestIP.Is4() || !a.Gateway.Is4() || a.PrefixLen < 8 || a.PrefixLen > 30 {
		return nil, fmt.Errorf("guest address %v/%d or gateway %v is not IPv4", a.GuestIP, a.PrefixLen, a.Gateway)
	}
	if err := checkPassword("cpadmin", a.AdminPassword); err != nil {
		return nil, err
	}
	if err := checkPassword("earner", a.EarnerPassword); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if err := answerTpl.Execute(&b, a); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
