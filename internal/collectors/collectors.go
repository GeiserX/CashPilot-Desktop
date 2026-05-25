package collectors

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

func (r *Registry) Collect(ctx context.Context, slug string, credentials map[string]string) (store.EarningsRecord, error) {
	var result Result
	var err error
	switch slug {
	case "anyone-protocol":
		result, err = r.collectAnyone(ctx, credentials)
	case "bitping":
		result, err = r.collectBitping(ctx, credentials)
	case "bytelixir":
		result, err = r.collectBytelixir(ctx, credentials)
	case "earnapp":
		result, err = r.collectEarnApp(ctx, credentials)
	case "honeygain":
		result, err = r.collectHoneygain(ctx, credentials)
	case "earnfm":
		result, err = r.collectEarnFM(ctx, credentials)
	case "grass":
		result, err = r.collectGrass(ctx, credentials)
	case "iproyal":
		result, err = r.collectIPRoyal(ctx, credentials)
	case "mysterium":
		result, err = r.collectMystNodes(ctx, credentials)
	case "packetstream":
		result, err = r.collectPacketStream(ctx, credentials)
	case "proxyrack":
		result, err = r.collectProxyRack(ctx, credentials)
	case "repocket":
		result, err = r.collectRepocket(ctx, credentials)
	case "salad":
		result, err = r.collectSalad(ctx, credentials)
	case "storj":
		result, err = r.collectStorj(ctx, credentials)
	case "traffmonetizer":
		result, err = r.collectTraffmonetizer(ctx, credentials)
	default:
		err = fmt.Errorf("collector for %s is not ported yet", slug)
	}
	if err != nil {
		result = Result{Platform: slug, Balance: 0, Currency: "USD", Error: err.Error()}
	}
	record := store.EarningsRecord{
		Platform: result.Platform,
		Balance:  result.Balance,
		Currency: result.Currency,
		Error:    result.Error,
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
	if totalTokens <= 0 {
		return Result{Platform: "anyone-protocol", Balance: 0, Currency: "ANYONE"}, nil
	}
	var price struct {
		Anyone struct {
			USD float64 `json:"usd"`
		} `json:"airtor-protocol"`
	}
	if err := r.doJSON(ctx, "GET", "https://api.coingecko.com/api/v3/simple/price?ids=airtor-protocol&vs_currencies=usd", nil, nil, &price); err != nil || price.Anyone.USD <= 0 {
		return Result{Platform: "anyone-protocol", Balance: round(totalTokens, 6), Currency: "ANYONE"}, nil
	}
	return Result{Platform: "anyone-protocol", Balance: round(totalTokens*price.Anyone.USD, 2), Currency: "USD"}, nil
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
		return Result{}, fmt.Errorf("could not parse Bytelixir dashboard balance: %w", err)
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
	var earnings struct {
		EarningsTotal float64 `json:"earningsTotal"`
	}
	if err := r.doJSON(ctx, "GET", "https://my.mystnodes.com/api/v2/node/total-earnings", nil, map[string]string{"Authorization": "Bearer " + login.AccessToken}, &earnings); err != nil {
		return Result{}, err
	}
	return Result{Platform: "mysterium", Balance: round(earnings.EarningsTotal, 4), Currency: "MYST"}, nil
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
		return Result{}, fmt.Errorf("could not parse PacketStream balance from dashboard")
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

func (r *Registry) doRaw(ctx context.Context, method, url string, body any, headers map[string]string, cookies []*http.Cookie) ([]byte, int, http.Header, error) {
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, 0, nil, err
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, 0, nil, err
	}
	req.Header.Set("User-Agent", "CashPilot Desktop")
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, resp.Header, err
	}
	return raw, resp.StatusCode, resp.Header, nil
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
	return float64(int(value*scale+0.5)) / scale
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

func parseBytelixirBalance(html string) (float64, bool) {
	re := regexp.MustCompile(`<span>\$</span>(\d+\.\d+)<span[^>]*>(\d*)</span>`)
	matches := re.FindAllStringSubmatch(html, -1)
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
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?s)<h3>Balance</h3>.*?<h2[^>]*>\$?([\d.]+)</h2>`),
		regexp.MustCompile(`"balance"\s*:\s*([\d.]+)`),
	}
	for _, pattern := range patterns {
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
