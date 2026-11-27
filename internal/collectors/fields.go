package collectors

import (
	"sort"

	"github.com/GeiserX/CashPilot-Desktop/internal/i18n"
)

// Field is one credential input the earnings collector needs and the deploy form
// does not already ask for.
//
// A service's wizard form is built from the catalog's docker.env plus these fields.
// The container's environment contract and the collector's login are frequently NOT
// the same thing: Repocket's container authenticates with RP_EMAIL/RP_API_KEY while
// its collector signs in to Firebase with the account email and password, and a
// cookie-driven collector (EarnApp, Salad, PacketStream) needs a browser cookie the
// container never sees.
//
// These live here, next to the collectors that read them, because the frontend's
// copy silently went stale: a web-catalog rename of Repocket's docker.env keys left
// the wizard with no field for the collector's credentials at all, so the collector
// could only ever answer "Repocket email and password are required". The frontend now
// renders whatever this table declares (AppState.collectorFields), and
// TestEveryCollectorCanReachItsCredentialsFromTheWizardForm proves every collector's
// required keys are reachable from docker.env plus this table.
type Field struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Secret      bool   `json:"secret,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// collectorFields is the table, built when read so its labels are in the current
// language.
func collectorFields() map[string][]Field {
	return map[string][]Field{
		"2miners-etc": {
			{Key: twoMinersETCAddressKey, Label: i18n.T("go.collectors.field.payoutAddress", "coin", "ETC"), Required: true},
		},
		"anyone-protocol": {
			{Key: "ANYONE_FINGERPRINTS", Label: i18n.T("go.collectors.field.fingerprints.label"), Description: i18n.T("go.collectors.field.fingerprints.description"), Required: true},
		},
		"bitping": {
			{Key: "BITPING_EMAIL", Label: i18n.T("go.collectors.field.email", "name", "Bitping"), Description: i18n.T("go.collectors.field.emailFor", "site", "app.bitping.com"), Required: true},
			{Key: "BITPING_PASSWORD", Label: i18n.T("go.collectors.field.password", "name", "Bitping"), Description: i18n.T("go.collectors.field.passwordFor", "site", "app.bitping.com"), Secret: true, Required: true},
		},
		"bytelixir": {
			{Key: "BYTELIXIR_SESSION", Label: i18n.T("go.collectors.field.session", "name", "Bytelixir"), Description: i18n.T("go.collectors.field.browserCookie", "cookie", "bytelixir_session"), Secret: true, Required: true},
			{Key: "BYTELIXIR_REMEMBER_WEB", Label: i18n.T("go.collectors.field.rememberCookie"), Description: i18n.T("go.collectors.field.optionalCookie", "cookie", "remember_web"), Secret: true},
			{Key: "BYTELIXIR_XSRF_TOKEN", Label: i18n.T("go.collectors.field.xsrfToken", "name", "XSRF"), Description: i18n.T("go.collectors.field.optionalCookie", "cookie", "XSRF-TOKEN"), Secret: true},
		},
		"earnapp": {
			{Key: "EARNAPP_OAUTH_TOKEN", Label: i18n.T("go.collectors.field.oauthToken"), Description: i18n.T("go.collectors.field.browserCookie", "cookie", "oauth-refresh-token"), Secret: true, Required: true},
			{Key: "EARNAPP_BRD_SESS_ID", Label: i18n.T("go.collectors.field.session", "name", "Bright Data"), Description: i18n.T("go.collectors.field.optionalCookie", "cookie", "brd_sess_id"), Secret: true},
		},
		"earnfm": {
			{Key: "EARNFM_EMAIL", Label: i18n.T("go.collectors.field.email", "name", "Earn.fm"), Description: i18n.T("go.collectors.field.emailFor", "site", "app.earn.fm"), Required: true},
			{Key: "EARNFM_PASSWORD", Label: i18n.T("go.collectors.field.password", "name", "Earn.fm"), Description: i18n.T("go.collectors.field.passwordFor", "site", "app.earn.fm"), Secret: true, Required: true},
		},
		"grass": {
			{Key: "GRASS_ACCESS_TOKEN", Label: i18n.T("go.collectors.field.accessToken", "name", "Grass"), Description: i18n.T("go.collectors.field.localStorage", "key", "accessToken", "site", "app.grass.io"), Secret: true, Required: true},
		},
		"herominers-kas": {
			{Key: heroMinersKaspaAddressKey, Label: i18n.T("go.collectors.field.payoutAddress", "coin", "Kaspa"), Required: true},
		},
		"mysterium": {
			{Key: "MYSTNODES_EMAIL", Label: i18n.T("go.collectors.field.email", "name", "MystNodes"), Description: i18n.T("go.collectors.field.emailFor", "site", "my.mystnodes.com"), Required: true},
			{Key: "MYSTNODES_PASSWORD", Label: i18n.T("go.collectors.field.password", "name", "MystNodes"), Description: i18n.T("go.collectors.field.passwordFor", "site", "my.mystnodes.com"), Secret: true, Required: true},
		},
		"ocean": {
			{Key: oceanAddressKey, Label: i18n.T("go.collectors.field.payoutAddress", "coin", "BTC"), Required: true},
		},
		"packetstream": {
			{Key: "PACKETSTREAM_AUTH_TOKEN", Label: i18n.T("go.collectors.field.authCookie", "name", "PacketStream"), Description: i18n.T("go.collectors.field.authCookieFrom", "site", "app.packetstream.io"), Secret: true, Required: true},
		},
		// The container reads RP_EMAIL and RP_API_KEY (the key shown on the Repocket
		// bandwidth-earnings page). The collector signs in to Firebase with the ACCOUNT
		// password instead, which the API key cannot replace, so it has to be asked for.
		"repocket": {
			{Key: "REPOCKET_EMAIL", Label: i18n.T("go.collectors.field.accountEmail", "name", "Repocket"), Description: i18n.T("go.collectors.field.repocketEmail.description", "site", "app.repocket.com"), Required: true},
			{Key: "REPOCKET_PASSWORD", Label: i18n.T("go.collectors.field.accountPassword", "name", "Repocket"), Description: i18n.T("go.collectors.field.repocketPassword.description", "site", "app.repocket.com"), Secret: true, Required: true},
		},
		"salad": {
			{Key: "SALAD_AUTH_COOKIE", Label: i18n.T("go.collectors.field.authCookie", "name", "Salad"), Description: i18n.T("go.collectors.field.authCookieFrom", "site", "salad.com"), Secret: true, Required: true},
		},
		"storj": {
			{Key: "STORJ_API_URL", Label: i18n.T("go.collectors.field.apiURL", "name", "Storj"), Description: i18n.T("go.collectors.field.storjURL.description", "url", "http://localhost:14002")},
		},
		"supportxmr": {
			{Key: supportXMRAddressKey, Label: i18n.T("go.collectors.field.payoutAddress", "coin", "Monero"), Required: true},
		},
	}
}

// Fields returns the collector-only credential inputs for slug, or nil when the
// service's collector needs nothing beyond the container's own environment.
func Fields(slug string) []Field {
	declared := collectorFields()[slug]
	if len(declared) == 0 {
		return nil
	}
	out := make([]Field, len(declared))
	copy(out, declared)
	return out
}

// AllFields returns a copy of the whole table, keyed by slug, for the frontend.
func AllFields() map[string][]Field {
	table := collectorFields()
	out := make(map[string][]Field, len(table))
	for slug, declared := range table {
		out[slug] = append([]Field(nil), declared...)
	}
	return out
}

// FieldSlugs lists the slugs that declare collector-only fields, sorted.
func FieldSlugs() []string {
	table := collectorFields()
	out := make([]string, 0, len(table))
	for slug := range table {
		out = append(out, slug)
	}
	sort.Strings(out)
	return out
}
