package harness

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// Providers is a stand-in for the earning platforms and the exchange-rate feeds.
// One loopback server answers for every hostname the collectors call, so a test
// can drive the real collectors — real request building, real auth round-trip,
// real JSON decode, real HTTP over TCP — with no accounts and no internet.
//
// Requests reach it because Client() hands back an http.Client whose transport
// sends every request to this server while leaving the Host header alone; the
// handler routes on that Host, the way a real host would.
type Providers struct {
	srv *httptest.Server

	mu        sync.Mutex
	balances  map[string]float64
	cryptoUSD map[string]float64
	fiat      map[string]float64
	failing   map[string]bool
	hits      map[string]int
}

// tokens the fake platforms issue at login and then require on the balance call,
// so a collector that forgets to pass the token it was just given fails here too.
const (
	honeygainToken = "fake-honeygain-token"
	pawnsToken     = "fake-pawns-token"
	mystToken      = "fake-myst-token"
	repocketToken  = "fake-repocket-id-token"
)

// NewProviders starts the fake platform and rate server on a loopback TCP port
// with a usable set of balances and rates already in place.
func NewProviders() *Providers {
	p := &Providers{
		balances: map[string]float64{
			"honeygain":      12.50,
			"iproyal":        8.75,
			"traffmonetizer": 7.25,
			"mysterium":      40.00, // MYST, not dollars
			"repocket":       3.40,
		},
		cryptoUSD: map[string]float64{"mysterium": 0.25},
		fiat:      map[string]float64{"EUR": 0.90},
		failing:   map[string]bool{},
		hits:      map[string]int{},
	}
	p.srv = httptest.NewServer(http.HandlerFunc(p.handle))
	return p
}

// Close shuts the fake down.
func (p *Providers) Close() { p.srv.Close() }

// URL is the base URL of the fake, for the rate feeds which take one directly.
func (p *Providers) URL() string { return p.srv.URL }

// Client is the http.Client to hand the collectors: it keeps their real, hardcoded
// URLs and simply delivers the requests here.
func (p *Providers) Client() *http.Client {
	return &http.Client{Transport: &redirectTransport{host: strings.TrimPrefix(p.srv.URL, "http://")}}
}

// SetBalance changes what a platform reports next time it is asked.
func (p *Providers) SetBalance(platform string, amount float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.balances[platform] = amount
}

// SetFailing makes a platform answer HTTP 500, the way an outage does.
func (p *Providers) SetFailing(platform string, failing bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failing[platform] = failing
}

// SetCryptoUSD sets the dollar price of a token, keyed by its CoinGecko id.
func (p *Providers) SetCryptoUSD(coingeckoID string, usd float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cryptoUSD[coingeckoID] = usd
}

// SetFiat sets a USD -> fiat rate.
func (p *Providers) SetFiat(code string, rate float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fiat[code] = rate
}

// Hits reports how many requests a platform has had, so a test can prove a
// collector really went out to the network rather than reading a cache.
func (p *Providers) Hits(platform string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.hits[platform]
}

func (p *Providers) balance(platform string) (float64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hits[platform]++
	return p.balances[platform], !p.failing[platform]
}

// redirectTransport sends every request to one loopback server without touching
// the Host header, so the collectors' own URLs stay exactly as production builds
// them and the fake can still tell the platforms apart.
type redirectTransport struct {
	host string
}

func (t *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if clone.Host == "" {
		clone.Host = req.URL.Host
	}
	clone.URL.Scheme = "http"
	clone.URL.Host = t.host
	return http.DefaultTransport.RoundTrip(clone)
}

func (p *Providers) handle(w http.ResponseWriter, r *http.Request) {
	// The rate feeds are addressed by base URL, so they are matched on path before
	// the platforms, which are matched on host.
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/v3/simple/price"):
		p.coingecko(w, r)
		return
	case r.URL.Path == "/latest":
		p.frankfurter(w)
		return
	}

	host, _, _ := strings.Cut(r.Host, ":")
	switch host {
	case "dashboard.honeygain.com":
		p.honeygain(w, r)
	case "api.pawns.app":
		p.iproyal(w, r)
	case "data.traffmonetizer.com":
		p.traffmonetizer(w, r)
	case "my.mystnodes.com":
		p.mysterium(w, r)
	case "identitytoolkit.googleapis.com":
		p.firebaseSignIn(w, r)
	case "api.repocket.com":
		p.repocket(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "fake provider: no host " + r.Host})
	}
}

func (p *Providers) honeygain(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/v1/users/tokens":
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"access_token": honeygainToken}})
	case "/api/v1/users/balances":
		if !hasBearer(r, honeygainToken) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing token"})
			return
		}
		amount, ok := p.balance("honeygain")
		if !ok {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "honeygain is down"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{"payout": map[string]any{"usd_cents": int(amount * 100)}},
		})
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no honeygain route " + r.URL.Path})
	}
}

func (p *Providers) iproyal(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/v1/users/tokens":
		writeJSON(w, http.StatusOK, map[string]any{"access_token": pawnsToken})
	case "/api/v1/users/me/balance-dashboard":
		if !hasBearer(r, pawnsToken) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing token"})
			return
		}
		amount, ok := p.balance("iproyal")
		if !ok {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "pawns is down"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"balance": amount})
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no pawns route " + r.URL.Path})
	}
}

func (p *Providers) traffmonetizer(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/app_user/get_balance" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no traffmonetizer route " + r.URL.Path})
		return
	}
	amount, ok := p.balance("traffmonetizer")
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "traffmonetizer is down"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"balance": amount}})
}

func (p *Providers) mysterium(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/v2/auth/login":
		writeJSON(w, http.StatusOK, map[string]any{"accessToken": mystToken})
	case r.URL.Path == "/api/v2/node/total-earnings":
		if !hasBearer(r, mystToken) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing token"})
			return
		}
		amount, ok := p.balance("mysterium")
		if !ok {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mystnodes is down"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"earningsTotal": amount})
	case r.URL.Path == "/api/v2/node":
		writeJSON(w, http.StatusOK, map[string]any{"nodes": []map[string]any{{
			"identity":         "0xfake",
			"name":             "fake-node",
			"localIp":          "127.0.0.1",
			"version":          "1.0.0",
			"nodeStatus":       map[string]any{"online": true},
			"country":          map[string]any{"code": "ES"},
			"earnings":         []map[string]any{{"etherAmount": 1.0}},
			"lifetimeEarnings": map[string]any{"totalEther": 2.0, "settledEther": 1.0, "unsettledEther": 1.0},
		}}})
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no mystnodes route " + r.URL.Path})
	}
}

// firebaseSignIn stands in for the Google sign-in Repocket logs in through. It is
// the one platform in this harness whose secret travels in the URL query
// (?key=<firebase key>), which is what makes it the right place to check that a
// failure is recorded without the secret in it.
func (p *Providers) firebaseSignIn(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/accounts:signInWithPassword" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no identitytoolkit route " + r.URL.Path})
		return
	}
	if r.URL.Query().Get("key") == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"message": "API key not valid"}})
		return
	}
	p.mu.Lock()
	failing := p.failing["repocket"]
	p.mu.Unlock()
	if failing {
		// Google answers a rejected sign-in with 400, which the app does not retry —
		// so the error it records is the one built from this request's URL.
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"message": "INVALID_PASSWORD"}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"idToken": repocketToken})
}

func (p *Providers) repocket(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/reports/current" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no repocket route " + r.URL.Path})
		return
	}
	if r.Header.Get("Auth-Token") != repocketToken {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing token"})
		return
	}
	amount, ok := p.balance("repocket")
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "repocket is down"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"centsCredited": amount * 100})
}

func (p *Providers) coingecko(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]map[string]float64{}
	for _, id := range strings.Split(r.URL.Query().Get("ids"), ",") {
		if price, ok := p.cryptoUSD[id]; ok {
			out[id] = map[string]float64{"usd": price}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (p *Providers) frankfurter(w http.ResponseWriter) {
	p.mu.Lock()
	rates := map[string]float64{}
	for code, rate := range p.fiat {
		rates[code] = rate
	}
	p.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"amount": 1, "base": "USD", "rates": rates})
}

func hasBearer(r *http.Request, token string) bool {
	return r.Header.Get("Authorization") == "Bearer "+token
}
