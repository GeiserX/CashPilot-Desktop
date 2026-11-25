package upstream

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Target is the CashPilot server a user wants this computer to join: its address
// and the shared fleet key (CASHPILOT_API_KEY) that enrols a new box.
type Target struct {
	URL string
	Key string
}

// The keys of the block CashPilot's Fleet page copies under "Add a box"
// (app/templates/fleet.html, copyWorkerEnv):
//
//	CASHPILOT_UI_URL=http://192.168.1.10:8080
//	CASHPILOT_API_KEY=<the fleet key>
//	CASHPILOT_WORKER_NAME=server-name
//
// The worker name is a placeholder there, and this computer reports its own
// hostname, so that line is read past.
const (
	blockURLKey = "CASHPILOT_UI_URL"
	blockKeyKey = "CASHPILOT_API_KEY"
)

// ParseJoin reads what the user gave the join screen: the pasted "Add a box"
// block, or the two fields, or both. A value in the block wins; a value the block
// lacks comes from its field. Every error is a sentence the user can act on.
func ParseJoin(block, fieldURL, fieldKey string) (Target, error) {
	t := Target{URL: cleanValue(fieldURL), Key: cleanValue(fieldKey)}
	if strings.TrimSpace(block) != "" {
		u, k, found := parseBlock(block)
		if !found {
			return Target{}, errors.New("The pasted text has no CASHPILOT_UI_URL or CASHPILOT_API_KEY line. On your CashPilot, open Fleet, then Add a box, press Reveal API Key, then Copy, and paste it here.")
		}
		if u != "" {
			t.URL = u
		}
		if k != "" {
			t.Key = k
		}
	}
	if t.URL == "" {
		return Target{}, errors.New("The address of your CashPilot is missing. It is the CASHPILOT_UI_URL line, for example http://192.168.1.10:8080.")
	}
	if t.Key == "" {
		return Target{}, errors.New("The key is missing. It is the CASHPILOT_API_KEY line that Fleet, Add a box copies.")
	}
	if hiddenKey(t.Key) {
		return Target{}, errors.New("The key in the pasted text is hidden. On the Fleet page press Reveal API Key first, then Copy again.")
	}
	if strings.ContainsAny(t.Key, " \t\r\n") {
		return Target{}, errors.New("The key has spaces in it. Copy it again from Fleet, Add a box.")
	}
	u, err := normalizeURL(t.URL)
	if err != nil {
		return Target{}, err
	}
	t.URL = u
	return t, nil
}

// parseBlock reads KEY=VALUE lines. It accepts what a paste tends to carry: an
// "export " prefix, a "- " list marker from a compose file, KEY: VALUE, quotes,
// spaces and Windows line ends.
func parseBlock(block string) (serverURL, key string, found bool) {
	for _, line := range strings.FieldsFunc(block, func(r rune) bool { return r == '\n' || r == '\r' }) {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "- ")
		line = strings.TrimPrefix(line, "export ")
		line = strings.TrimSpace(line)
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			name, value, ok = strings.Cut(line, ":")
			// "CASHPILOT_UI_URL: http://x" is a key; "http://x" alone is not.
			if !ok || strings.Contains(name, "/") {
				continue
			}
		}
		switch strings.ToUpper(cleanValue(name)) {
		case blockURLKey:
			serverURL, found = cleanValue(value), true
		case blockKeyKey:
			key, found = cleanValue(value), true
		}
	}
	return serverURL, key, found
}

// cleanValue trims spaces and one pair of matching quotes.
func cleanValue(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			v = strings.TrimSpace(v[1 : len(v)-1])
		}
	}
	return v
}

// hiddenKey reports the stand-ins the Fleet page shows before the key is
// revealed, or when the reveal failed.
func hiddenKey(k string) bool {
	return strings.Contains(k, "•") || strings.HasPrefix(k, "(")
}

// normalizeURL adds http:// to a bare host:port, drops a trailing slash and
// refuses what is not a web address.
func normalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	bad := errors.New("That address does not look right. It should look like http://192.168.1.10:8080 or https://cashpilot.example.com.")
	if err != nil || u.Hostname() == "" || strings.ContainsAny(u.Host, " \t") {
		return "", bad
	}
	if s := strings.ToLower(u.Scheme); s != "http" && s != "https" {
		return "", bad
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", bad
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// Failure is a failed join or heartbeat, said in plain words.
type Failure struct {
	// Code names the kind of failure, for the screen and the tests: address, dns,
	// unreachable, certificate, not-cashpilot, desktop, key-refused,
	// already-joined, too-old, no-fleet-key, server-error.
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Explain turns a heartbeat error into a Failure. host is the server's host as
// the user typed it; name is this computer's name on the Fleet page.
func Explain(err error, host, name string) Failure {
	var status *StatusError
	var dnsErr *net.DNSError
	var certErr *tls.CertificateVerificationError
	var unknownAuth x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var invalid x509.CertificateInvalidError
	switch {
	case err == nil:
		return Failure{}
	case errors.As(err, &status):
		return explainStatus(status, name)
	case errors.Is(err, ErrNotCashPilot):
		return notCashPilot()
	case errors.As(err, &dnsErr):
		return Failure{"dns", "Cannot find " + host + ". Check the address for a typo, and that this computer can reach that network."}
	case errors.As(err, &certErr), errors.As(err, &unknownAuth), errors.As(err, &hostErr), errors.As(err, &invalid):
		return Failure{"certificate", host + " uses https, but its certificate is not valid. Use an address with a valid certificate, or the LAN or Tailscale address over http."}
	case strings.Contains(err.Error(), "refusing a redirect"), strings.Contains(err.Error(), "after 10 redirects"):
		return Failure{"address", host + " sends this computer to another address, and CashPilot does not send the key there. Use the address your CashPilot shows on its Fleet page."}
	case strings.Contains(err.Error(), "refusing to contact"):
		return ExplainAddress(err)
	default:
		return Failure{"unreachable", "Cannot reach " + host + ". Check that CashPilot is running there, and that this computer is on the same network or Tailscale."}
	}
}

// ExplainAddress turns a ValidateServerURL refusal into a Failure.
func ExplainAddress(err error) Failure {
	if strings.Contains(err.Error(), "plain http") {
		return Failure{"address", "Use https, or a LAN or Tailscale address. Plain http to a public address would send the key in clear text."}
	}
	return Failure{"address", "CashPilot does not send the key to that address. Use the address your CashPilot shows on its Fleet page."}
}

func notCashPilot() Failure {
	return Failure{"not-cashpilot", "That address answers, but it is not a CashPilot dashboard. Copy the address from your CashPilot's Fleet page, Add a box."}
}

func explainStatus(e *StatusError, name string) Failure {
	body := strings.ToLower(e.Body)
	switch {
	case strings.Contains(body, "cashpilot desktop"):
		// Desktop's own hub refuses another Desktop, and refuses everyone while off.
		return Failure{"desktop", "That address is another CashPilot Desktop, not a CashPilot server. Join the CashPilot server instead."}
	case e.Code == http.StatusUnauthorized && (strings.Contains(body, "per-worker key") || strings.Contains(body, "enrollment was never completed")):
		return Failure{"already-joined", "Your CashPilot already knows a computer called " + name + ". Remove " + name + " on its Fleet page, then connect again."}
	case e.Code == http.StatusUnauthorized || e.Code == http.StatusForbidden:
		return Failure{"key-refused", "Your CashPilot refused the key. Copy it again from Fleet, Add a box, Reveal API Key."}
	case e.Code == http.StatusServiceUnavailable && strings.Contains(body, "fleet key not configured"):
		return Failure{"no-fleet-key", "Your CashPilot has no fleet key yet. Set CASHPILOT_API_KEY on the server, restart it, then connect again."}
	case (e.Code == http.StatusNotFound || e.Code == http.StatusMethodNotAllowed || e.Code == http.StatusUnprocessableEntity) && e.JSON:
		// CashPilot answers JSON even for a route it lacks: a 404 there means a
		// release from before workers could join, and a 422 one that does not
		// accept this computer's report.
		return Failure{"too-old", "Your CashPilot is too old to accept this computer. Update it, then connect again."}
	case e.Code >= 500:
		return Failure{"server-error", "Your CashPilot answered with an error (" + http.StatusText(e.Code) + "). Try again in a minute."}
	default:
		return notCashPilot()
	}
}

// Host is the host of a server URL, for messages. It falls back to the URL.
func Host(serverURL string) string {
	if u, err := url.Parse(serverURL); err == nil && u.Host != "" {
		return u.Host
	}
	return serverURL
}
