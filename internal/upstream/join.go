package upstream

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/i18n"
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
			return Target{}, errors.New(i18n.T("go.upstream.parse.noLines", "urlVar", "CASHPILOT_UI_URL", "keyVar", "CASHPILOT_API_KEY"))
		}
		if u != "" {
			t.URL = u
		}
		if k != "" {
			t.Key = k
		}
	}
	if t.URL == "" {
		return Target{}, errors.New(i18n.T("go.upstream.parse.noURL", "urlVar", "CASHPILOT_UI_URL", "example", "http://192.168.1.10:8080"))
	}
	if t.Key == "" {
		return Target{}, errors.New(i18n.T("go.upstream.parse.noKey", "keyVar", "CASHPILOT_API_KEY"))
	}
	if hiddenKey(t.Key) {
		return Target{}, errors.New(i18n.T("go.upstream.parse.hiddenKey"))
	}
	if strings.ContainsAny(t.Key, " \t\r\n") {
		return Target{}, errors.New(i18n.T("go.upstream.parse.keySpaces"))
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
	bad := errors.New(i18n.T("go.upstream.parse.badAddress", "lanExample", "http://192.168.1.10:8080", "httpsExample", "https://cashpilot.example.com"))
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
		return Failure{"dns", i18n.T("go.upstream.fail.dns", "host", host)}
	case errors.As(err, &certErr), errors.As(err, &unknownAuth), errors.As(err, &hostErr), errors.As(err, &invalid):
		return Failure{"certificate", i18n.T("go.upstream.fail.certificate", "host", host)}
	case strings.Contains(err.Error(), "refusing a redirect"), strings.Contains(err.Error(), "after 10 redirects"):
		return Failure{"address", i18n.T("go.upstream.fail.redirect", "host", host)}
	case strings.Contains(err.Error(), "refusing to contact"):
		return ExplainAddress(err)
	default:
		return Failure{"unreachable", i18n.T("go.upstream.fail.unreachable", "host", host)}
	}
}

// ExplainAddress turns a ValidateServerURL refusal into a Failure.
func ExplainAddress(err error) Failure {
	if strings.Contains(err.Error(), "plain http") {
		return Failure{"address", i18n.T("go.upstream.fail.plainHTTP")}
	}
	return Failure{"address", i18n.T("go.upstream.fail.address")}
}

func notCashPilot() Failure {
	return Failure{"not-cashpilot", i18n.T("go.upstream.fail.notCashPilot")}
}

func explainStatus(e *StatusError, name string) Failure {
	body := strings.ToLower(e.Body)
	switch {
	case strings.Contains(body, "cashpilot desktop"):
		// Desktop's own hub refuses another Desktop, and refuses everyone while off.
		return Failure{"desktop", i18n.T("go.upstream.fail.desktop")}
	case e.Code == http.StatusUnauthorized && (strings.Contains(body, "per-worker key") || strings.Contains(body, "enrollment was never completed")):
		return Failure{"already-joined", i18n.T("go.upstream.fail.alreadyJoined", "name", name)}
	case e.Code == http.StatusUnauthorized || e.Code == http.StatusForbidden:
		return Failure{"key-refused", i18n.T("go.upstream.fail.keyRefused")}
	case e.Code == http.StatusServiceUnavailable && strings.Contains(body, "fleet key not configured"):
		return Failure{"no-fleet-key", i18n.T("go.upstream.fail.noFleetKey", "keyVar", "CASHPILOT_API_KEY")}
	case (e.Code == http.StatusNotFound || e.Code == http.StatusMethodNotAllowed || e.Code == http.StatusUnprocessableEntity) && e.JSON:
		// CashPilot answers JSON even for a route it lacks: a 404 there means a
		// release from before workers could join, and a 422 one that does not
		// accept this computer's report.
		return Failure{"too-old", i18n.T("go.upstream.fail.tooOld")}
	case e.Code >= 500:
		return Failure{"server-error", i18n.T("go.upstream.fail.serverError", "status", http.StatusText(e.Code))}
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
