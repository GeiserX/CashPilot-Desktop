package collectors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
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
	case "honeygain":
		result, err = r.collectHoneygain(ctx, credentials)
	case "earnfm":
		result, err = r.collectEarnFM(ctx, credentials)
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

func (r *Registry) doJSON(ctx context.Context, method, url string, body any, headers map[string]string, out any) error {
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "CashPilot Desktop")
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s returned HTTP %d", url, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
