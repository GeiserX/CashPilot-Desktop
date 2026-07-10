package collectors

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

type Registry struct {
	store *store.Store
	http  *http.Client
}

type Result struct {
	Platform string
	Balance  float64
	Currency string
	Error    string
}

func NewRegistry(st *store.Store) *Registry {
	return &Registry{
		store: st,
		http:  &http.Client{Timeout: 30 * time.Second},
	}
}

// collectorFunc runs one service's collector against the given credentials.
type collectorFunc func(*Registry, context.Context, map[string]string) (Result, error)

// collectorDispatch is the single source of truth for which slugs have a native
// collector. Collect dispatches through it and Supports reports membership, so the
// two can never drift: a slug either has an entry here or it does not.
var collectorDispatch = map[string]collectorFunc{
	"anyone-protocol": (*Registry).collectAnyone,
	"bitping":         (*Registry).collectBitping,
	"bytelixir":       (*Registry).collectBytelixir,
	"earnapp":         (*Registry).collectEarnApp,
	"honeygain":       (*Registry).collectHoneygain,
	"earnfm":          (*Registry).collectEarnFM,
	"grass":           (*Registry).collectGrass,
	"iproyal":         (*Registry).collectIPRoyal,
	"mysterium":       (*Registry).collectMystNodes,
	"packetstream":    (*Registry).collectPacketStream,
	"proxyrack":       (*Registry).collectProxyRack,
	"repocket":        (*Registry).collectRepocket,
	"salad":           (*Registry).collectSalad,
	"storj":           (*Registry).collectStorj,
	"traffmonetizer":  (*Registry).collectTraffmonetizer,
	"vast-ai":         (*Registry).collectVast,
}

// Supports reports whether slug has a native collector wired up. The scheduler
// uses it to skip deployed services that have no automated collector rather than
// persisting a spurious "not ported yet" error row for them on every cycle.
func (r *Registry) Supports(slug string) bool {
	_, ok := collectorDispatch[slug]
	return ok
}

func (r *Registry) Collect(ctx context.Context, slug string, credentials map[string]string) (store.EarningsRecord, error) {
	var result Result
	var err error
	if fn, ok := collectorDispatch[slug]; ok {
		result, err = fn(r, ctx, credentials)
	} else {
		err = fmt.Errorf("Collector for %s is not ported yet", slug)
	}
	if err != nil {
		result = Result{Platform: slug, Balance: 0, Currency: "USD", Error: err.Error()}
	}
	record := store.EarningsRecord{
		Platform: result.Platform,
		Balance:  result.Balance,
		Currency: result.Currency,
		// Redact any secret embedded in a URL query (e.g. the Repocket Firebase
		// sign-in URL carries ?key=<FIREBASE_KEY>) before the error is persisted
		// verbatim into earnings.error, where it would be readable in the dashboard/DB.
		Error: redactURLSecrets(result.Error),
	}
	return r.store.SaveEarnings(record)
}

func (r *Registry) collectAnyone(ctx context.Context, credentials map[string]string) (Result, error) {
	fingerprints := splitCSV(firstCredential(credentials, "ANYONE_FINGERPRINTS", "fingerprints"))
	if len(fingerprints) == 0 {
		return Result{}, fmt.Errorf("Anyone relay fingerprints are required")
	}
	totalTokens := 0.0
	for _, fingerprint := range fingerprints {
		payload := map[string]any{
			"Id":     "1234",
			"Target": "QZJTY63XZtHOHo_qPaEX7VdtemZh4rpj821xcanPGXA",
			"Owner":  "1234",
			"Anchor": "0",
			"Tags": []map[string]string{
				{"name": "Action", "value": "Get-Rewards"},
				{"name": "Fingerprint", "value": fingerprint},
				{"name": "Address", "value": "0x0000000000000000000000000000000000000000"},
				{"name": "Data-Protocol", "value": "ao"},
				{"name": "Type", "value": "Message"},
				{"name": "Variant", "value": "ao.TN.1"},
			},
			"Data": "1234",
		}
		var resp struct {
			Messages []struct {
				Data string `json:"Data"`
			} `json:"Messages"`
			Error string `json:"error"`
		}
		if err := r.doJSON(ctx, "POST", "https://cu.anyone.tech/dry-run?process-id=QZJTY63XZtHOHo_qPaEX7VdtemZh4rpj821xcanPGXA", payload, nil, &resp); err != nil {
			return Result{}, err
		}
		if resp.Error != "" {
			return Result{}, fmt.Errorf("Anyone AO error: %s", resp.Error)
		}
		if len(resp.Messages) == 0 || resp.Messages[0].Data == "" || resp.Messages[0].Data == "null" {
			continue
		}
		raw, err := strconv.ParseFloat(resp.Messages[0].Data, 64)
		if err != nil {
			return Result{}, err
		}
		totalTokens += raw / 1_000_000_000_000_000_000
	}
	// Always emit the raw ANYONE token count and let the exchange layer price it
	// (internal/exchange maps ANYONE -> airtor-protocol). Pricing here previously
	// flipped the currency to USD on a successful CoinGecko fetch and back to
	// ANYONE on failure/zero; because computeEarningsSummary collapses each
	// platform to a single currency, that flip corrupted Today/Month accrual. A
	// single, stable currency keeps the summary's conversion consistent.
	return Result{Platform: "anyone-protocol", Balance: round(totalTokens, 6), Currency: "ANYONE"}, nil
}

func (r *Registry) collectBitping(ctx context.Context, credentials map[string]string) (Result, error) {
	email, password, err := requirePair(credentials, "BITPING_EMAIL", "email", "BITPING_PASSWORD", "password", "Bitping email and password are required")
	if err != nil {
		return Result{}, err
	}
	body, status, headers, err := r.doRaw(ctx, "POST", "https://nodes.bitping.com/auth/login", map[string]string{"email": email, "password": password}, nil, nil)
	if err != nil {
		return Result{}, err
	}
	if status < 200 || status >= 300 {
		return Result{}, fmt.Errorf("Bitping login returned HTTP %d", status)
	}
	token := ""
	for _, cookie := range (&http.Response{Header: headers}).Cookies() {
		if cookie.Name == "token" {
			token = cookie.Value
			break
		}
	}
	if token == "" {
		var login map[string]any
		_ = json.Unmarshal(body, &login)
		token, _ = login["token"].(string)
	}
	if token == "" {
		return Result{}, fmt.Errorf("Bitping login did not return a token")
	}
	var earnings struct {
		USDEarnings float64 `json:"usdEarnings"`
	}
	if err := r.doJSON(ctx, "GET", "https://nodes.bitping.com/api/v2/payouts/earnings", nil, map[string]string{"Authorization": "Bearer " + token}, &earnings); err != nil {
		return Result{}, err
	}
	return Result{Platform: "bitping", Balance: round(earnings.USDEarnings, 4), Currency: "USD"}, nil
}

func (r *Registry) collectBytelixir(ctx context.Context, credentials map[string]string) (Result, error) {
	session := firstCredential(credentials, "BYTELIXIR_SESSION", "bytelixir_session", "session_cookie")
	if session == "" {
		return Result{}, fmt.Errorf("Bytelixir session cookie is required")
	}
	cookies := []*http.Cookie{{Name: "bytelixir_session", Value: session}}
	if remember := firstCredential(credentials, "BYTELIXIR_REMEMBER_WEB", "remember_web"); remember != "" {
		cookies = append(cookies, &http.Cookie{Name: "remember_web_59ba36addc2b2f9401580f014c7f58ea4e30989d", Value: remember})
	}
	if xsrf := firstCredential(credentials, "BYTELIXIR_XSRF_TOKEN", "xsrf_token"); xsrf != "" {
		cookies = append(cookies, &http.Cookie{Name: "XSRF-TOKEN", Value: xsrf})
	}
	body, status, _, err := r.doRaw(ctx, "GET", "https://dash.bytelixir.com/", nil, map[string]string{"Accept": "text/html"}, cookies)
	if err != nil {
		return Result{}, err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return Result{}, fmt.Errorf("Bytelixir session expired")
	}
	if status >= 200 && status < 300 {
		if balance, ok := parseBytelixirBalance(string(body)); ok {
			return Result{Platform: "bytelixir", Balance: round(balance, 4), Currency: "USD"}, nil
		}
	}
	var api struct {
		Data struct {
			Balance string `json:"balance"`
		} `json:"data"`
	}
	if err := r.doJSONWithCookies(ctx, "GET", "https://dash.bytelixir.com/api/v1/user", nil, map[string]string{"Accept": "application/json", "X-Requested-With": "XMLHttpRequest"}, cookies, &api); err != nil {
		return Result{}, fmt.Errorf("Could not parse Bytelixir dashboard balance: %w", err)
	}
	balance, _ := strconv.ParseFloat(api.Data.Balance, 64)
	return Result{Platform: "bytelixir", Balance: round(balance, 4), Currency: "USD", Error: "Withdrawable balance only; dashboard scrape did not expose total earned"}, nil
}

func (r *Registry) collectEarnApp(ctx context.Context, credentials map[string]string) (Result, error) {
	oauthToken := firstCredential(credentials, "EARNAPP_OAUTH_TOKEN", "oauth_token")
	if oauthToken == "" {
		return Result{}, fmt.Errorf("EarnApp OAuth refresh token is required for earnings collection")
	}
	cookies := []*http.Cookie{
		{Name: "auth", Value: "1"},
		{Name: "auth-method", Value: "google"},
		{Name: "oauth-refresh-token", Value: oauthToken},
	}
	if sess := firstCredential(credentials, "EARNAPP_BRD_SESS_ID", "brd_sess_id"); sess != "" {
		cookies = append(cookies, &http.Cookie{Name: "brd_sess_id", Value: sess})
	}
	_, _, rotateHeaders, err := r.doRaw(ctx, "GET", "https://earnapp.com/dashboard/api/sec/rotate_xsrf?appid=earnapp&version=1.627.783", nil, nil, cookies)
	if err != nil {
		return Result{}, err
	}
	headers := map[string]string{"X-Requested-With": "XMLHttpRequest"}
	for _, cookie := range (&http.Response{Header: rotateHeaders}).Cookies() {
		if cookie.Name == "xsrf-token" {
			headers["xsrf-token"] = cookie.Value
			cookies = append(cookies, cookie)
			break
		}
	}
	var money struct {
		Balance float64 `json:"balance"`
		Error   string  `json:"error"`
	}
	if err := r.doJSONWithCookies(ctx, "GET", "https://earnapp.com/dashboard/api/money?appid=earnapp&version=1.627.783", nil, headers, cookies, &money); err != nil {
		return Result{}, err
	}
	if money.Error != "" {
		return Result{}, errors.New(money.Error)
	}
	return Result{Platform: "earnapp", Balance: round(money.Balance, 4), Currency: "USD"}, nil
}

func (r *Registry) collectHoneygain(ctx context.Context, credentials map[string]string) (Result, error) {
	email := credentials["HONEYGAIN_EMAIL"]
	password := credentials["HONEYGAIN_PASSWORD"]
	if email == "" || password == "" {
		return Result{}, fmt.Errorf("Honeygain email and password are required")
	}
	tokenReq := map[string]string{"email": email, "password": password}
	var tokenResp struct {
		Data struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	if err := r.doJSON(ctx, "POST", "https://dashboard.honeygain.com/api/v1/users/tokens", tokenReq, nil, &tokenResp); err != nil {
		return Result{}, err
	}
	if tokenResp.Data.AccessToken == "" {
		return Result{}, fmt.Errorf("Honeygain login did not return an access token")
	}
	var balanceResp struct {
		Data struct {
			Payout struct {
				USDCents float64 `json:"usd_cents"`
			} `json:"payout"`
		} `json:"data"`
	}
	headers := map[string]string{"Authorization": "Bearer " + tokenResp.Data.AccessToken}
	if err := r.doJSON(ctx, "GET", "https://dashboard.honeygain.com/api/v1/users/balances", nil, headers, &balanceResp); err != nil {
		return Result{}, err
	}
	return Result{Platform: "honeygain", Balance: balanceResp.Data.Payout.USDCents / 100, Currency: "USD"}, nil
}

func (r *Registry) collectEarnFM(ctx context.Context, credentials map[string]string) (Result, error) {
	email := credentials["EARNFM_EMAIL"]
	password := credentials["EARNFM_PASSWORD"]
	if email == "" || password == "" {
		return Result{}, fmt.Errorf("Earn.fm email and password are required for earnings collection")
	}
	anonKey := os.Getenv("CASHPILOT_EARNFM_SUPABASE_ANON_KEY")
	if anonKey == "" {
		return Result{}, fmt.Errorf("CASHPILOT_EARNFM_SUPABASE_ANON_KEY must be configured to collect Earn.fm earnings")
	}
	authReq := map[string]string{"email": email, "password": password}
	headers := map[string]string{"apikey": anonKey}
	var authResp struct {
		AccessToken string `json:"access_token"`
	}
	if err := r.doJSON(ctx, "POST", "https://sb.earn.fm/auth/v1/token?grant_type=password", authReq, headers, &authResp); err != nil {
		return Result{}, err
	}
	if authResp.AccessToken == "" {
		return Result{}, fmt.Errorf("Earn.fm login did not return an access token")
	}
	var balanceResp struct {
		Data struct {
			TotalBalance float64 `json:"totalBalance"`
		} `json:"data"`
	}
	if err := r.doJSON(ctx, "GET", "https://api.earn.fm/v2/harvester/view_balance", nil, map[string]string{"X-API-Key": authResp.AccessToken}, &balanceResp); err != nil {
		return Result{}, err
	}
	return Result{Platform: "earnfm", Balance: balanceResp.Data.TotalBalance, Currency: "USD"}, nil
}

func (r *Registry) collectGrass(ctx context.Context, credentials map[string]string) (Result, error) {
	token := firstCredential(credentials, "GRASS_ACCESS_TOKEN", "access_token")
	if token == "" {
		return Result{}, fmt.Errorf("Grass access token is required")
	}
	headers := grassHeaders(token)
	var user struct {
		Result struct {
			Data struct {
				TotalPoints float64 `json:"totalPoints"`
			} `json:"data"`
		} `json:"result"`
	}
	status, err := r.doJSONStatus(ctx, "GET", "https://api.getgrass.io/retrieveUser", nil, headers, nil, &user)
	if err != nil {
		return Result{}, err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return Result{}, fmt.Errorf("Grass token expired")
	}
	if status == http.StatusTooManyRequests {
		return Result{}, fmt.Errorf("Grass API rate limited")
	}
	// Any other non-2xx (a 400/404/422, or the endpoint changing shape) must be an
	// error, not a silent success: doJSONStatus leaves the out-struct zero-valued on
	// a non-2xx, so without this the collector would book Balance:0 with no Error and
	// overwrite the real balance. Every other collector routes non-2xx through
	// doJSON, which converts it to an error; do the same here.
	if status < 200 || status >= 300 {
		return Result{}, fmt.Errorf("Grass returned unexpected status %d", status)
	}
	if user.Result.Data.TotalPoints > 0 {
		return Result{Platform: "grass", Balance: round(user.Result.Data.TotalPoints, 4), Currency: "GRASS"}, nil
	}
	var active struct {
		Result struct {
			Data []struct {
				AggUptime  float64 `json:"aggUptime"`
				IPScore    float64 `json:"ipScore"`
				Multiplier float64 `json:"multiplier"`
			} `json:"data"`
		} `json:"result"`
	}
	status, err = r.doJSONStatus(ctx, "GET", "https://api.getgrass.io/activeDevices", nil, headers, nil, &active)
	if err != nil {
		return Result{}, err
	}
	if status == http.StatusTooManyRequests {
		return Result{}, fmt.Errorf("Grass API rate limited")
	}
	// As with retrieveUser above: a non-2xx here leaves active zero-valued, so treat
	// it as an error rather than booking a spurious Balance:0.
	if status < 200 || status >= 300 {
		return Result{}, fmt.Errorf("Grass returned unexpected status %d", status)
	}
	total := 0.0
	for _, device := range active.Result.Data {
		multiplier := device.Multiplier
		if multiplier == 0 {
			multiplier = 1
		}
		if device.AggUptime > 0 && device.IPScore > 0 {
			total += (device.AggUptime / 3600) * 50 * (device.IPScore / 100) * multiplier
		}
	}
	return Result{Platform: "grass", Balance: round(total, 4), Currency: "GRASS"}, nil
}

func (r *Registry) collectIPRoyal(ctx context.Context, credentials map[string]string) (Result, error) {
	email := firstCredential(credentials, "IPROYALPAWNS_EMAIL", "IPROYAL_EMAIL", "email")
	password := firstCredential(credentials, "IPROYALPAWNS_PASSWORD", "IPROYAL_PASSWORD", "password")
	if email == "" || password == "" {
		return Result{}, fmt.Errorf("IPRoyal Pawns email and password are required")
	}
	var login struct {
		AccessToken string `json:"access_token"`
	}
	payload := map[string]string{
		"identifier":         randomIdentifier(21),
		"email":              email,
		"password":           password,
		"h_captcha_response": "",
	}
	if err := r.doJSON(ctx, "POST", "https://api.pawns.app/api/v1/users/tokens", payload, nil, &login); err != nil {
		return Result{}, err
	}
	if login.AccessToken == "" {
		return Result{}, fmt.Errorf("IPRoyal login did not return an access token")
	}
	var balance struct {
		Balance float64 `json:"balance"`
	}
	if err := r.doJSON(ctx, "GET", "https://api.pawns.app/api/v1/users/me/balance-dashboard", nil, map[string]string{"Authorization": "Bearer " + login.AccessToken}, &balance); err != nil {
		return Result{}, err
	}
	return Result{Platform: "iproyal", Balance: round(balance.Balance, 4), Currency: "USD"}, nil
}

func (r *Registry) collectMystNodes(ctx context.Context, credentials map[string]string) (Result, error) {
	email, password, err := requirePair(credentials, "MYSTNODES_EMAIL", "email", "MYSTNODES_PASSWORD", "password", "MystNodes email and password are required")
	if err != nil {
		return Result{}, err
	}
	var login struct {
		AccessToken string `json:"accessToken"`
	}
	if err := r.doJSON(ctx, "POST", "https://my.mystnodes.com/api/v2/auth/login", map[string]any{"email": email, "password": password, "remember": true}, nil, &login); err != nil {
		return Result{}, err
	}
	if login.AccessToken == "" {
		return Result{}, fmt.Errorf("MystNodes login did not return an access token")
	}
	authHeaders := map[string]string{"Authorization": "Bearer " + login.AccessToken}
	var earnings struct {
		EarningsTotal float64 `json:"earningsTotal"`
	}
	if err := r.doJSON(ctx, "GET", "https://my.mystnodes.com/api/v2/node/total-earnings", nil, authHeaders, &earnings); err != nil {
		return Result{}, err
	}

	// The per-node breakdown is a best-effort BONUS on top of the flat total-earnings
	// balance: the operator runs Myst on several servers, so a per-node view is
	// genuinely useful, but a failure here must NEVER fail the whole collect — the
	// total Result below is the contract. It reuses the SAME bearer token (and the
	// shared doJSON retry/backoff path); log and swallow any error (fetch, decode, or
	// a nil store) so the total still returns.
	if err := r.collectMystPerNode(ctx, authHeaders); err != nil {
		log.Printf("mysterium per-node earnings unavailable (total earnings still collected): %v", err)
	}

	return Result{Platform: "mysterium", Balance: round(earnings.EarningsTotal, 4), Currency: "MYST"}, nil
}

// mystNode is one Mysterium node's per-node earnings, flattened from the MystNodes
// cloud API's GET /api/v2/node list into the shape the dashboard renders. The
// operator runs Myst on several servers, so this per-node breakdown is stashed —
// marshaled to JSON — in the generic service_details store under the "mysterium"
// slug, alongside (not replacing) the flat total-earnings balance.
type mystNode struct {
	Identity              string  `json:"identity"`
	Name                  string  `json:"name"`
	LocalIP               string  `json:"localIp"`
	Country               string  `json:"country"`
	Version               string  `json:"version"`
	Online                bool    `json:"online"`
	Earnings30dMYST       float64 `json:"earnings30dMyst"`
	LifetimeMYST          float64 `json:"lifetimeMyst"`
	LifetimeSettledMYST   float64 `json:"lifetimeSettledMyst"`
	LifetimeUnsettledMYST float64 `json:"lifetimeUnsettledMyst"`
}

// collectMystPerNode fetches the per-node earnings breakdown with an already-obtained
// bearer token and persists it as a JSON blob in the generic service_details store
// under the "mysterium" slug. It mirrors the production collector's per-node fetch:
// GET /api/v2/node?page=1&itemsPerPage=100, then per node maps identity, name, localIp,
// nodeStatus.online, country.code, version, the 30-day earnings (sum of
// earnings[].etherAmount), and the lifetimeEarnings split. It funnels through the shared
// doJSON path, so it inherits the same retry/backoff as every other collector.
//
// It is deliberately best-effort: the caller (collectMystNodes) swallows the returned
// error and still returns the flat total-earnings Result, so a missing store, a failed
// fetch, or a decode error degrades to "no per-node detail this cycle" rather than
// failing the whole collect. The error is returned only so the caller can log it (and
// tests can assert on it).
func (r *Registry) collectMystPerNode(ctx context.Context, authHeaders map[string]string) error {
	if r.store == nil {
		return fmt.Errorf("No store configured for per-node persistence")
	}
	var resp struct {
		Nodes []struct {
			Identity   string `json:"identity"`
			Name       string `json:"name"`
			LocalIP    string `json:"localIp"`
			Version    string `json:"version"`
			NodeStatus struct {
				Online bool `json:"online"`
			} `json:"nodeStatus"`
			Country struct {
				Code string `json:"code"`
			} `json:"country"`
			Earnings []struct {
				EtherAmount float64 `json:"etherAmount"`
			} `json:"earnings"`
			LifetimeEarnings struct {
				TotalEther     float64 `json:"totalEther"`
				SettledEther   float64 `json:"settledEther"`
				UnsettledEther float64 `json:"unsettledEther"`
			} `json:"lifetimeEarnings"`
		} `json:"nodes"`
	}
	if err := r.doJSON(ctx, "GET", "https://my.mystnodes.com/api/v2/node?page=1&itemsPerPage=100", nil, authHeaders, &resp); err != nil {
		return err
	}
	nodes := make([]mystNode, 0, len(resp.Nodes))
	for _, n := range resp.Nodes {
		// 30-day earnings is the sum of etherAmount across every service type the node
		// earned from in the window (mirrors the production collector).
		var earnings30d float64
		for _, e := range n.Earnings {
			earnings30d += e.EtherAmount
		}
		nodes = append(nodes, mystNode{
			Identity:              n.Identity,
			Name:                  n.Name,
			LocalIP:               n.LocalIP,
			Country:               n.Country.Code,
			Version:               n.Version,
			Online:                n.NodeStatus.Online,
			Earnings30dMYST:       round(earnings30d, 6),
			LifetimeMYST:          round(n.LifetimeEarnings.TotalEther, 6),
			LifetimeSettledMYST:   round(n.LifetimeEarnings.SettledEther, 6),
			LifetimeUnsettledMYST: round(n.LifetimeEarnings.UnsettledEther, 6),
		})
	}
	detail, err := json.Marshal(nodes)
	if err != nil {
		return err
	}
	return r.store.SaveServiceDetail("mysterium", string(detail))
}

func (r *Registry) collectPacketStream(ctx context.Context, credentials map[string]string) (Result, error) {
	auth := firstCredential(credentials, "PACKETSTREAM_AUTH_TOKEN", "auth_token")
	if auth == "" {
		return Result{}, fmt.Errorf("PacketStream auth cookie is required")
	}
	body, status, _, err := r.doRaw(ctx, "GET", "https://app.packetstream.io/dashboard", nil, nil, []*http.Cookie{{Name: "auth", Value: auth}})
	if err != nil {
		return Result{}, err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return Result{}, fmt.Errorf("PacketStream authentication failed")
	}
	balance, ok := parsePacketStreamBalance(string(body))
	if !ok {
		return Result{}, fmt.Errorf("Could not parse PacketStream balance from dashboard")
	}
	return Result{Platform: "packetstream", Balance: round(balance, 4), Currency: "USD"}, nil
}

func (r *Registry) collectProxyRack(ctx context.Context, credentials map[string]string) (Result, error) {
	apiKey := firstCredential(credentials, "API_KEY", "PROXYRACK_API_KEY", "api_key")
	if apiKey == "" {
		return Result{}, fmt.Errorf("ProxyRack API key is required")
	}
	var resp struct {
		Data struct {
			Balance string `json:"balance"`
		} `json:"data"`
	}
	headers := map[string]string{"Api-Key": apiKey, "Accept": "application/json"}
	if err := r.doJSON(ctx, "POST", "https://peer.proxyrack.com/api/balance", nil, headers, &resp); err != nil {
		return Result{}, err
	}
	balance, _ := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(resp.Data.Balance, "$")), 64)
	return Result{Platform: "proxyrack", Balance: round(balance, 4), Currency: "USD"}, nil
}

func (r *Registry) collectRepocket(ctx context.Context, credentials map[string]string) (Result, error) {
	email, password, err := requirePair(credentials, "REPOCKET_EMAIL", "email", "REPOCKET_PASSWORD", "password", "Repocket email and password are required")
	if err != nil {
		return Result{}, err
	}
	firebaseKey := os.Getenv("CASHPILOT_REPOCKET_FIREBASE_KEY")
	if firebaseKey == "" {
		return Result{}, fmt.Errorf("CASHPILOT_REPOCKET_FIREBASE_KEY must be configured to collect Repocket earnings")
	}
	var login struct {
		IDToken string `json:"idToken"`
	}
	if err := r.doJSON(ctx, "POST", "https://identitytoolkit.googleapis.com/v1/accounts:signInWithPassword?key="+firebaseKey, map[string]any{"email": email, "password": password, "returnSecureToken": true}, nil, &login); err != nil {
		return Result{}, err
	}
	if login.IDToken == "" {
		return Result{}, fmt.Errorf("Repocket login did not return an ID token")
	}
	var report struct {
		CentsCredited float64 `json:"centsCredited"`
	}
	if err := r.doJSON(ctx, "GET", "https://api.repocket.com/api/reports/current", nil, map[string]string{"Auth-Token": login.IDToken}, &report); err != nil {
		return Result{}, err
	}
	return Result{Platform: "repocket", Balance: round(report.CentsCredited/100, 4), Currency: "USD"}, nil
}

func (r *Registry) collectSalad(ctx context.Context, credentials map[string]string) (Result, error) {
	authCookie := firstCredential(credentials, "SALAD_AUTH_COOKIE", "auth_cookie")
	if authCookie == "" {
		return Result{}, fmt.Errorf("Salad auth cookie is required")
	}
	var balance struct {
		CurrentBalance float64 `json:"currentBalance"`
	}
	headers := map[string]string{"X-XSRF-TOKEN": authCookie}
	cookies := []*http.Cookie{{Name: "auth", Value: authCookie}}
	if err := r.doJSONWithCookies(ctx, "GET", "https://app-api.salad.com/api/v1/profile/balance", nil, headers, cookies, &balance); err != nil {
		return Result{}, err
	}
	return Result{Platform: "salad", Balance: round(balance.CurrentBalance, 4), Currency: "USD"}, nil
}

func (r *Registry) collectStorj(ctx context.Context, credentials map[string]string) (Result, error) {
	apiURL := firstCredential(credentials, "STORJ_API_URL", "api_url")
	if apiURL == "" {
		apiURL = "http://localhost:14002"
	}
	apiURL = strings.TrimRight(apiURL, "/")
	var payout map[string]any
	if err := r.doJSON(ctx, "GET", apiURL+"/api/sno/estimated-payout", nil, nil, &payout); err != nil {
		if err := r.doJSON(ctx, "GET", apiURL+"/api/sno", nil, nil, &payout); err != nil {
			return Result{}, err
		}
	}
	balance := storjPayoutUSD(payout)
	return Result{Platform: "storj", Balance: round(balance, 4), Currency: "USD"}, nil
}

func (r *Registry) collectTraffmonetizer(ctx context.Context, credentials map[string]string) (Result, error) {
	token := firstCredential(credentials, "TRAFFMONETIZER_TOKEN", "token")
	if token == "" {
		return Result{}, fmt.Errorf("Traffmonetizer token is required")
	}
	var resp struct {
		Data struct {
			Balance float64 `json:"balance"`
		} `json:"data"`
	}
	headers := map[string]string{
		"Authorization": "Bearer " + token,
		"Origin":        "https://app.traffmonetizer.com",
		"Referer":       "https://app.traffmonetizer.com/",
	}
	if err := r.doJSON(ctx, "GET", "https://data.traffmonetizer.com/api/app_user/get_balance", nil, headers, &resp); err != nil {
		return Result{}, err
	}
	return Result{Platform: "traffmonetizer", Balance: round(resp.Data.Balance, 4), Currency: "USD"}, nil
}

func (r *Registry) collectVast(ctx context.Context, credentials map[string]string) (Result, error) {
	apiKey := firstCredential(credentials, "VAST_API_KEY", "vast_api_key", "api_key")
	if apiKey == "" {
		return Result{}, fmt.Errorf("Vast.ai API key is required")
	}
	var resp vastEarnings
	headers := map[string]string{"Authorization": "Bearer " + apiKey, "Accept": "application/json"}
	if err := r.doJSON(ctx, "GET", "https://console.vast.ai/api/v0/users/me/machine-earnings", nil, headers, &resp); err != nil {
		return Result{}, err
	}
	// A valid response with no earnings (no GPU rented yet, or a brand-new host) is
	// Balance 0, never an error — a GPU is required to earn, so an empty machine
	// list is expected. vastEarningsUSD returns 0 when every earnings field is absent.
	return Result{Platform: "vast-ai", Balance: round(vastEarningsUSD(resp), 4), Currency: "USD"}, nil
}

// vastMachineEarning is one row of Vast.ai's per-machine earnings. The endpoint
// splits earnings across GPU, storage, and up/down bandwidth; some responses also
// carry a pre-summed total on the row.
type vastMachineEarning struct {
	MachineID int     `json:"machine_id"`
	GPUEarn   float64 `json:"gpu_earn"`
	StoEarn   float64 `json:"sto_earn"`
	BWUEarn   float64 `json:"bwu_earn"`
	BWDEarn   float64 `json:"bwd_earn"`
	Total     float64 `json:"total"`
	TotalEarn float64 `json:"total_earn"`
}

func (m vastMachineEarning) earnings() float64 {
	if m.Total != 0 {
		return m.Total
	}
	if m.TotalEarn != 0 {
		return m.TotalEarn
	}
	return m.GPUEarn + m.StoEarn + m.BWUEarn + m.BWDEarn
}

// vastEarnings is the shape of GET /api/v0/users/me/machine-earnings: a per-machine
// breakdown (per_machine), a windowed summary, and a current account-balance block.
// machine_earnings is tolerated as an alternate name for the per-machine array.
type vastEarnings struct {
	Current struct {
		Balance float64 `json:"balance"`
		Total   float64 `json:"total"`
		Credit  float64 `json:"credit"`
	} `json:"current"`
	Summary struct {
		TotalGPU  float64 `json:"total_gpu"`
		TotalStor float64 `json:"total_stor"`
		TotalBWU  float64 `json:"total_bwu"`
		TotalBWD  float64 `json:"total_bwd"`
	} `json:"summary"`
	PerMachine      []vastMachineEarning `json:"per_machine"`
	MachineEarnings []vastMachineEarning `json:"machine_earnings"`
	Total           float64              `json:"total"`
}

// vastEarningsUSD reduces a machine-earnings response to a single USD figure: it
// sums the per-machine earnings the endpoint returned, and when that array is
// absent falls back to the current account total, then the current balance, then
// the windowed summary, then a top-level total. Everything absent -> 0 (a valid
// "earned nothing yet" response, not an error).
func vastEarningsUSD(e vastEarnings) float64 {
	machines := e.PerMachine
	if len(machines) == 0 {
		machines = e.MachineEarnings
	}
	total := 0.0
	for _, m := range machines {
		total += m.earnings()
	}
	if total != 0 {
		return total
	}
	if e.Current.Total != 0 {
		return e.Current.Total
	}
	if e.Current.Balance != 0 {
		return e.Current.Balance
	}
	if s := e.Summary.TotalGPU + e.Summary.TotalStor + e.Summary.TotalBWU + e.Summary.TotalBWD; s != 0 {
		return s
	}
	return e.Total
}

func (r *Registry) doJSON(ctx context.Context, method, url string, body any, headers map[string]string, out any) error {
	status, err := r.doJSONStatus(ctx, method, url, body, headers, nil, out)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("%s returned HTTP %d", url, status)
	}
	return nil
}

func (r *Registry) doJSONWithCookies(ctx context.Context, method, url string, body any, headers map[string]string, cookies []*http.Cookie, out any) error {
	status, err := r.doJSONStatus(ctx, method, url, body, headers, cookies, out)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("%s returned HTTP %d", url, status)
	}
	return nil
}

func (r *Registry) doJSONStatus(ctx context.Context, method, url string, body any, headers map[string]string, cookies []*http.Cookie, out any) (int, error) {
	raw, status, _, err := r.doRaw(ctx, method, url, body, headers, cookies)
	if err != nil {
		return status, err
	}
	if status >= 200 && status < 300 && out != nil {
		return status, json.Unmarshal(raw, out)
	}
	return status, nil
}

// Retry policy for the shared send path. Every collector funnels through doRaw ->
// doWithRetry, so tuning these covers all of them at once. A scheduled cycle used
// to make a SINGLE attempt: one transient blip (network reset, timeout) or a
// provider rate-limit (429) failed the collector and persisted a spurious error
// row until the next cycle.
const (
	// maxAttempts caps total tries (1 initial + up to maxAttempts-1 retries). Kept
	// small so a stalled provider cannot hold a collect cycle open for long.
	maxAttempts = 3
	// maxRetryWait bounds any single backoff wait — including a Retry-After the
	// server asks for — so a hostile or oversized header cannot hang a cycle.
	maxRetryWait = 30 * time.Second
	// maxResponseBytes caps how much of any response body is read (success path) or
	// drained (between retries) so an unbounded body cannot OOM the app.
	maxResponseBytes = 8 << 20 // 8 MiB
)

// retryBaseWait is the exponential backoff base: attempt 1 waits ~base, attempt 2
// ~2*base, and so on (plus jitter). It is a var rather than a const only so tests
// can shrink it to exercise the retry paths without real-time sleeps; production
// never reassigns it.
var retryBaseWait = 500 * time.Millisecond

func (r *Registry) doRaw(ctx context.Context, method, url string, body any, headers map[string]string, cookies []*http.Cookie) ([]byte, int, http.Header, error) {
	// Buffer the request body ONCE. doWithRetry may send the request several times
	// and a *bytes.Reader is consumed by the first Do, so each attempt needs a fresh
	// reader over these same bytes.
	var payload []byte
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, 0, nil, err
		}
		payload = raw
	}
	resp, err := r.doWithRetry(ctx, method, url, payload, headers, cookies)
	if err != nil {
		return nil, 0, nil, err
	}
	defer resp.Body.Close()
	// Cap the read at 8 MiB so a hostile or misconfigured endpoint returning a
	// huge (or unbounded) body cannot OOM the app — mirrors the fleet server's
	// http.MaxBytesReader defense on inbound requests.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, resp.StatusCode, resp.Header, err
	}
	return raw, resp.StatusCode, resp.Header, nil
}

// doWithRetry builds the request and calls r.http.Do, retrying only TRANSIENT
// failures with exponential backoff + jitter. Transient means a transport error
// (err != nil from Do — network reset, timeout, DNS) OR an HTTP status of 429 or
// >= 500. Every other status is returned immediately, live and unread: a 401/403/
// 404 is a real auth/logic error, so retrying it only wastes time and hammers the
// provider. On a 429 (or any retryable response carrying a Retry-After header) the
// server's requested delay is honored instead of the exponential value; both are
// capped at maxRetryWait. Each backoff races ctx.Done so the scheduler's shutdown
// cancel returns promptly and we never sleep past it. On success the returned
// *http.Response has an OPEN body the caller must close (identical to a single Do);
// bodies of retried responses are drained and closed here so connections are reused.
func (r *Registry) doWithRetry(ctx context.Context, method, url string, payload []byte, headers map[string]string, cookies []*http.Cookie) (*http.Response, error) {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := buildRequest(ctx, method, url, payload, headers, cookies)
		if err != nil {
			return nil, err // a bad method/URL is not transient — fail fast
		}

		wait := backoffFor(attempt)
		resp, err := r.http.Do(req)
		switch {
		case err != nil:
			// Transport-level failure (includes ctx cancel, surfaced as a *url.Error).
			lastErr = err
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			// Rate-limited or a server-side error: retry. Capture Retry-After before
			// draining, then drain (bounded) + close so the connection is reused.
			lastErr = fmt.Errorf("%s returned HTTP %d", url, resp.StatusCode)
			if after, ok := retryAfter(resp.Header); ok {
				wait = after
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
			resp.Body.Close()
		default:
			// 2xx/3xx or a non-retryable 4xx: hand the live response back untouched so
			// the caller reads its body exactly as a single Do would — the success
			// path is byte-for-byte identical to the pre-retry code.
			return resp, nil
		}

		if attempt == maxAttempts {
			break
		}
		if wait > maxRetryWait {
			wait = maxRetryWait
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
	return nil, lastErr
}

// buildRequest constructs the *http.Request for one attempt with a fresh body
// reader over payload (nil payload -> empty body), then applies the standard
// headers, caller headers, and cookies. Split out of doWithRetry so every attempt
// builds an identical request without duplicating the setup.
func buildRequest(ctx context.Context, method, url string, payload []byte, headers map[string]string, cookies []*http.Cookie) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "CashPilot Desktop")
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	return req, nil
}

// backoffFor returns the exponential wait for a 1-based attempt (base, 2*base,
// 4*base, ...) plus up to one base of jitter to keep many collectors from retrying
// in lockstep after a shared outage. The per-wait cap is applied by the caller.
func backoffFor(attempt int) time.Duration {
	base := retryBaseWait << (attempt - 1) // base * 2^(attempt-1)
	return base + jitterUpTo(retryBaseWait)
}

// jitterUpTo returns a random duration in [0, limit) using crypto/rand (the app's
// existing RNG). Jitter is best-effort smoothing, so a rand read failure just
// yields 0 rather than an error.
func jitterUpTo(limit time.Duration) time.Duration {
	if limit <= 0 {
		return 0
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0
	}
	return time.Duration(binary.BigEndian.Uint64(b[:]) % uint64(limit))
}

// retryAfter parses a Retry-After response header (RFC 7231): either delta-seconds
// ("120") or an HTTP-date ("Wed, 21 Oct 2026 07:28:00 GMT"). It returns the wait
// and ok=true when the header is present and parseable; a past date clamps to 0.
// The caller still applies maxRetryWait, so a huge far-future date cannot hang us.
// A missing/blank/garbage header returns ok=false -> caller uses exponential backoff.
func retryAfter(header http.Header) (time.Duration, bool) {
	value := strings.TrimSpace(header.Get("Retry-After"))
	if value == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(value); err == nil {
		if secs < 0 {
			return 0, false
		}
		// Clamp before multiplying: time.Duration(secs) * time.Second overflows int64
		// for a very large secs and wraps NEGATIVE, and since the caller only clamps
		// the UPPER bound (maxRetryWait) a negative wait would slip through and fire
		// immediately, defeating the cap. Anything at or beyond maxRetryWait is just
		// maxRetryWait, so cap secs there and never multiply an overflowing value.
		if secs >= int(maxRetryWait/time.Second) {
			return maxRetryWait, true
		}
		return time.Duration(secs) * time.Second, true
	}
	if when, err := http.ParseTime(value); err == nil {
		if d := time.Until(when); d > 0 {
			return d, true
		}
		return 0, true
	}
	return 0, false
}

// urlSecretRe matches a sensitive query parameter and its value inside any URL that
// may surface in an error string. Anchoring on a leading ? or & keeps it to real
// query params (so a path segment like signInWithPassword is never matched).
var urlSecretRe = regexp.MustCompile(`(?i)([?&](?:access_token|api_key|password|token|key)=)[^&\s"'<>]+`)

// redactURLSecrets replaces the value of any sensitive query parameter
// (key/token/password/access_token/api_key) with REDACTED. Collector errors can
// embed a request URL — the Repocket Firebase sign-in URL carries ?key=<FIREBASE_KEY>,
// and other collectors put keys in the query too — and Collect persists the error
// string verbatim into earnings.error, so the secret must be scrubbed first.
func redactURLSecrets(msg string) string {
	return urlSecretRe.ReplaceAllString(msg, "${1}REDACTED")
}

func firstCredential(credentials map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(credentials[key]); value != "" {
			return value
		}
	}
	return ""
}

func requirePair(credentials map[string]string, keyA, fallbackA, keyB, fallbackB, message string) (string, string, error) {
	a := firstCredential(credentials, keyA, fallbackA)
	b := firstCredential(credentials, keyB, fallbackB)
	if a == "" || b == "" {
		return "", "", errors.New(message)
	}
	return a, b, nil
}

func splitCSV(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func round(value float64, places int) float64 {
	scale := 1.0
	for i := 0; i < places; i++ {
		scale *= 10
	}
	// math.Round rounds half away from zero and handles negatives correctly; the
	// prior float64(int(value*scale+0.5)) truncated toward zero (wrong sign for
	// negative values) and risked overflow on very large balances.
	return math.Round(value*scale) / scale
}

func randomIdentifier(length int) string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("cashpilot-%d", time.Now().UnixNano())
	}
	for i := range buf {
		buf[i] = chars[int(buf[i])%len(chars)]
	}
	return string(buf)
}

func grassHeaders(token string) map[string]string {
	return map[string]string{
		"Authorization": token,
		"Accept":        "application/json, text/plain, */*",
		"Origin":        "https://app.grass.io",
		"Referer":       "https://app.grass.io/",
		"User-Agent":    "Mozilla/5.0",
	}
}

// bytelixirBalanceRe and packetStreamBalanceRes are compiled once at package init
// rather than on every collect: the parse functions run on the hot dashboard-scrape
// path, so recompiling the same patterns per call is wasted work.
var bytelixirBalanceRe = regexp.MustCompile(`<span>\$</span>(\d+\.\d+)<span[^>]*>(\d*)</span>`)

var packetStreamBalanceRes = []*regexp.Regexp{
	regexp.MustCompile(`(?s)<h3>Balance</h3>.*?<h2[^>]*>\$?([\d.]+)</h2>`),
	regexp.MustCompile(`"balance"\s*:\s*([\d.]+)`),
}

func parseBytelixirBalance(html string) (float64, bool) {
	matches := bytelixirBalanceRe.FindAllStringSubmatch(html, -1)
	for _, match := range matches {
		value, err := strconv.ParseFloat(match[1]+match[2], 64)
		if err == nil && value > 0 {
			return value, true
		}
	}
	if len(matches) > 0 {
		value, err := strconv.ParseFloat(matches[0][1]+matches[0][2], 64)
		return value, err == nil
	}
	return 0, false
}

func parsePacketStreamBalance(html string) (float64, bool) {
	for _, pattern := range packetStreamBalanceRes {
		if match := pattern.FindStringSubmatch(html); len(match) > 1 {
			value, err := strconv.ParseFloat(match[1], 64)
			return value, err == nil
		}
	}
	return 0, false
}

func storjPayoutUSD(data map[string]any) float64 {
	if month, ok := data["currentMonth"].(map[string]any); ok {
		return (asFloat(month["egressBandwidthPayout"]) + asFloat(month["egressRepairAuditPayout"]) + asFloat(month["diskSpacePayout"])) / 100
	}
	for _, key := range []string{"estimatedPayout", "currentMonthExpectations"} {
		if value := asFloat(data[key]); value > 0 {
			return value / 100
		}
	}
	return 0
}

func asFloat(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case json.Number:
		v, _ := typed.Float64()
		return v
	default:
		return 0
	}
}
