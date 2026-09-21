package collectors

import "sort"

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

var collectorFields = map[string][]Field{
	"anyone-protocol": {
		{Key: "ANYONE_FINGERPRINTS", Label: "Relay fingerprints", Description: "Comma-separated relay fingerprints", Required: true},
	},
	"bitping": {
		{Key: "BITPING_EMAIL", Label: "Bitping email", Description: "Email used for app.bitping.com", Required: true},
		{Key: "BITPING_PASSWORD", Label: "Bitping password", Description: "Password used for app.bitping.com", Secret: true, Required: true},
	},
	"bytelixir": {
		{Key: "BYTELIXIR_SESSION", Label: "Bytelixir session", Description: "bytelixir_session browser cookie", Secret: true, Required: true},
		{Key: "BYTELIXIR_REMEMBER_WEB", Label: "Remember cookie", Description: "Optional remember_web cookie", Secret: true},
		{Key: "BYTELIXIR_XSRF_TOKEN", Label: "XSRF token", Description: "Optional XSRF-TOKEN cookie", Secret: true},
	},
	"earnapp": {
		{Key: "EARNAPP_OAUTH_TOKEN", Label: "OAuth refresh token", Description: "oauth-refresh-token browser cookie", Secret: true, Required: true},
		{Key: "EARNAPP_BRD_SESS_ID", Label: "Bright Data session", Description: "Optional brd_sess_id cookie", Secret: true},
	},
	"earnfm": {
		{Key: "EARNFM_EMAIL", Label: "Earn.fm email", Description: "Email used for app.earn.fm", Required: true},
		{Key: "EARNFM_PASSWORD", Label: "Earn.fm password", Description: "Password used for app.earn.fm", Secret: true, Required: true},
	},
	"grass": {
		{Key: "GRASS_ACCESS_TOKEN", Label: "Grass access token", Description: "accessToken from app.grass.io local storage", Secret: true, Required: true},
	},
	"mysterium": {
		{Key: "MYSTNODES_EMAIL", Label: "MystNodes email", Description: "Email used for my.mystnodes.com", Required: true},
		{Key: "MYSTNODES_PASSWORD", Label: "MystNodes password", Description: "Password used for my.mystnodes.com", Secret: true, Required: true},
	},
	"packetstream": {
		{Key: "PACKETSTREAM_AUTH_TOKEN", Label: "PacketStream auth cookie", Description: "auth cookie from app.packetstream.io", Secret: true, Required: true},
	},
	// The container reads RP_EMAIL and RP_API_KEY (the key shown on the Repocket
	// bandwidth-earnings page). The collector signs in to Firebase with the ACCOUNT
	// password instead, which the API key cannot replace, so it has to be asked for.
	"repocket": {
		{Key: "REPOCKET_EMAIL", Label: "Repocket account email", Description: "Email used for app.repocket.com (usually the same as the container's email)", Required: true},
		{Key: "REPOCKET_PASSWORD", Label: "Repocket account password", Description: "Account password for app.repocket.com — NOT the API key the container uses", Secret: true, Required: true},
	},
	"salad": {
		{Key: "SALAD_AUTH_COOKIE", Label: "Salad auth cookie", Description: "auth cookie from salad.com", Secret: true, Required: true},
	},
	"storj": {
		{Key: "STORJ_API_URL", Label: "Storj API URL", Description: "Local node dashboard URL, default http://localhost:14002"},
	},
}

// Fields returns the collector-only credential inputs for slug, or nil when the
// service's collector needs nothing beyond the container's own environment.
func Fields(slug string) []Field {
	declared := collectorFields[slug]
	if len(declared) == 0 {
		return nil
	}
	out := make([]Field, len(declared))
	copy(out, declared)
	return out
}

// AllFields returns a copy of the whole table, keyed by slug, for the frontend.
func AllFields() map[string][]Field {
	out := make(map[string][]Field, len(collectorFields))
	for slug := range collectorFields {
		out[slug] = Fields(slug)
	}
	return out
}

// FieldSlugs lists the slugs that declare collector-only fields, sorted.
func FieldSlugs() []string {
	out := make([]string, 0, len(collectorFields))
	for slug := range collectorFields {
		out = append(out, slug)
	}
	sort.Strings(out)
	return out
}
