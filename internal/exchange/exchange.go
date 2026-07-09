// Package exchange converts service-earnings amounts between currencies for
// the CashPilot Desktop dashboard.
//
// It mirrors the CashPilot server's app/exchange_rates.py logic: crypto-to-USD
// rates come from CoinGecko (free, no key) and USD-to-fiat rates come from the
// Frankfurter API (free, no key). Rates are cached in memory and refreshed
// periodically. The cache is stale-graceful: a failed refresh keeps the last
// good rates rather than blanking them, and callers can check Stale to avoid
// silently summing balances against rates that may be badly out of date.
//
// The service is injectable (no package globals) and safe for concurrent use.
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// CacheTTL is how long cached rates are considered fresh; EnsureFresh triggers
// a refresh once the cache is older than this. StaleThreshold (2x the TTL) is
// how long rates may survive failing refreshes before Stale reports true.
const (
	CacheTTL       = 15 * time.Minute
	StaleThreshold = 30 * time.Minute
)

// DefaultCryptoIDs maps CashPilot's internal currency codes to CoinGecko coin
// ids. Grass *points* are intentionally absent: they are an internal reward
// that converts to the GRASS token only during airdrops at unknown ratios, so
// they are never priced here (see PointsCurrencies).
var DefaultCryptoIDs = map[string]string{
	"MYST":   "mysterium",
	"ANYONE": "airtor-protocol",
	"STORJ":  "storj",
	"HNT":    "helium",
	"GLM":    "golem",
	"PRE":    "presearch",
	"DVPN":   "sentinel",
	"DPR":    "deeper-network",
	"NOS":    "nosana",
	"FLUX":   "zelcash",
	"KOII":   "koii",
	"NODL":   "nodle-network",
	"TFUEL":  "theta-fuel",
}

// PointsCurrencies lists reward "points" that are deliberately non-convertible
// (they have no market price), so they are never summed into fiat/USD totals.
var PointsCurrencies = map[string]bool{
	"GRASS": true,
}

// Rates is a JSON-friendly snapshot of the cache for the frontend.
type Rates struct {
	Fiat        map[string]float64 `json:"fiat"`
	CryptoUSD   map[string]float64 `json:"cryptoUsd"`
	LastUpdated string             `json:"lastUpdated"`
}

// Service holds the cached rates and the configuration needed to refresh them.
// The zero value is not usable; construct one with NewService.
type Service struct {
	mu             sync.RWMutex
	http           *http.Client
	coingeckoURL   string
	frankfurterURL string
	cryptoIDs      map[string]string

	fiat      map[string]float64
	cryptoUSD map[string]float64
	lastFetch time.Time

	// refreshing is a single-flight guard for the background refresh kicked by
	// EnsureFresh, so concurrent/awaited callers never stack refreshes or block.
	refreshing atomic.Bool
}

// Option customises a Service at construction time.
type Option func(*Service)

// WithHTTPClient overrides the HTTP client used for rate fetches. A nil client
// is ignored so the default (15s timeout) is preserved.
func WithHTTPClient(c *http.Client) Option {
	return func(s *Service) {
		if c != nil {
			s.http = c
		}
	}
}

// WithBaseURLs overrides the CoinGecko and Frankfurter base URLs (handy for
// tests). Empty strings leave the corresponding default in place. Trailing
// slashes are trimmed so path joining stays correct.
func WithBaseURLs(coingecko, frankfurter string) Option {
	return func(s *Service) {
		if coingecko != "" {
			s.coingeckoURL = strings.TrimRight(coingecko, "/")
		}
		if frankfurter != "" {
			s.frankfurterURL = strings.TrimRight(frankfurter, "/")
		}
	}
}

// WithCryptoIDs overrides the currency-code -> CoinGecko-id map. A nil map is
// ignored so DefaultCryptoIDs is preserved.
func WithCryptoIDs(m map[string]string) Option {
	return func(s *Service) {
		if m != nil {
			s.cryptoIDs = m
		}
	}
}

// NewService builds a Service with sensible defaults, applying any options.
func NewService(opts ...Option) *Service {
	s := &Service{
		http:           &http.Client{Timeout: 15 * time.Second},
		coingeckoURL:   "https://api.coingecko.com",
		frankfurterURL: "https://api.frankfurter.app",
		cryptoIDs:      DefaultCryptoIDs,
		fiat:           map[string]float64{},
		cryptoUSD:      map[string]float64{},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Refresh fetches the latest crypto and fiat rates. It is stale-graceful: on a
// non-200 response or a transport error from either source the old cache is
// left untouched and the error is returned. The cache and lastFetch are only
// replaced when both fetches succeed, so lastFetch marks the last fully
// successful refresh.
func (s *Service) Refresh(ctx context.Context) error {
	crypto, err := s.fetchCrypto(ctx)
	if err != nil {
		return err
	}
	fiat, err := s.fetchFiat(ctx)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.cryptoUSD = crypto
	s.fiat = fiat
	s.lastFetch = time.Now()
	s.mu.Unlock()
	return nil
}

// EnsureFresh triggers a refresh when the cache is older than CacheTTL (or has
// never been fetched), but is NON-blocking: it kicks the refresh onto a
// background goroutine and returns immediately, so the awaited dashboard path
// (computeEarningsSummary / GetAppState) never stalls on up to ~30s of
// sequential HTTP GETs. A single-flight guard (refreshing) means concurrent or
// repeatedly-awaited callers never stack refreshes or block on one another;
// they keep serving the last snapshot, and Stale reports whether that data is
// trustworthy. Refresh errors are swallowed (the cache is stale-graceful).
func (s *Service) EnsureFresh(ctx context.Context) {
	s.mu.RLock()
	fresh := !s.lastFetch.IsZero() && time.Since(s.lastFetch) <= CacheTTL
	s.mu.RUnlock()
	if fresh {
		return
	}
	// Only one background refresh may be in flight at a time; if one already is,
	// return immediately and let it land.
	if !s.refreshing.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer s.refreshing.Store(false)
		_ = s.Refresh(ctx)
	}()
}

// Snapshot returns a deep copy of the current cache for serialisation.
func (s *Service) Snapshot() Rates {
	s.mu.RLock()
	defer s.mu.RUnlock()

	fiat := make(map[string]float64, len(s.fiat))
	for k, v := range s.fiat {
		fiat[k] = v
	}
	cryptoUSD := make(map[string]float64, len(s.cryptoUSD))
	for k, v := range s.cryptoUSD {
		cryptoUSD[k] = v
	}
	last := ""
	if !s.lastFetch.IsZero() {
		last = s.lastFetch.UTC().Format(time.RFC3339)
	}
	return Rates{Fiat: fiat, CryptoUSD: cryptoUSD, LastUpdated: last}
}

// Stale reports whether more than StaleThreshold has elapsed since the last
// successful refresh (a never-fetched cache is always stale).
func (s *Service) Stale() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.lastFetch.IsZero() {
		return true
	}
	return time.Since(s.lastFetch) > StaleThreshold
}

// ToUSD converts amount in currency to USD. ok is false for USD-unpriceable
// inputs (unknown tokens, reward points, or a missing/zero rate) so the caller
// can avoid silently summing them.
func (s *Service) ToUSD(amount float64, currency string) (usd float64, ok bool) {
	if currency == "USD" {
		return amount, true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Crypto rates are stored token->USD, so multiply.
	if rate, found := s.cryptoUSD[currency]; found && rate > 0 {
		return amount * rate, true
	}
	// Fiat rates are stored USD->currency, so divide to go currency->USD.
	if rate, found := s.fiat[currency]; found && rate > 0 {
		return amount / rate, true
	}
	return 0, false
}

// FromUSD converts a USD amount into the display currency. ok is false when the
// display currency has no known rate.
func (s *Service) FromUSD(usd float64, display string) (float64, bool) {
	if display == "USD" {
		return usd, true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Fiat rates are stored USD->currency, so multiply.
	if rate, found := s.fiat[display]; found && rate > 0 {
		return usd * rate, true
	}
	// Crypto rates are stored token->USD, so divide to go USD->token.
	if rate, found := s.cryptoUSD[display]; found && rate > 0 {
		return usd / rate, true
	}
	return 0, false
}

// ToDisplay converts amount from one currency straight into the display
// currency, routing through USD. ok is false if either leg is non-convertible.
func (s *Service) ToDisplay(amount float64, from, display string) (float64, bool) {
	usd, ok := s.ToUSD(amount, from)
	if !ok {
		return 0, false
	}
	return s.FromUSD(usd, display)
}

// Convertible reports whether a currency can be converted (it is USD, a known
// fiat currency, or a priced crypto token).
func (s *Service) Convertible(currency string) bool {
	if currency == "USD" {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Require a positive rate, exactly as ToUSD/FromUSD do: a currency present in the
	// cache with a zero (or negative) rate is not actually convertible — it would
	// "convert" to 0 — so it must not report Convertible.
	if rate, ok := s.fiat[currency]; ok && rate > 0 {
		return true
	}
	if rate, ok := s.cryptoUSD[currency]; ok && rate > 0 {
		return true
	}
	return false
}

// IsPoints reports whether a currency is a declared reward "point" (it has no
// market price by design). Classification is by INTENT — membership in
// PointsCurrencies — not by whether a live rate happens to be available, so a
// priceable token that is momentarily unpriced (a rate outage) is never
// misclassified as a reward point. The lookup is case-insensitive.
func (s *Service) IsPoints(cur string) bool {
	return PointsCurrencies[strings.ToUpper(cur)]
}

// fetchCrypto pulls token->USD prices from CoinGecko's simple/price endpoint,
// mapping each configured CoinGecko id back to its currency code.
func (s *Service) fetchCrypto(ctx context.Context) (map[string]float64, error) {
	out := make(map[string]float64, len(s.cryptoIDs))
	if len(s.cryptoIDs) == 0 {
		return out, nil
	}

	ids := make([]string, 0, len(s.cryptoIDs))
	for _, id := range s.cryptoIDs {
		ids = append(ids, id)
	}
	endpoint := fmt.Sprintf(
		"%s/api/v3/simple/price?ids=%s&vs_currencies=usd",
		s.coingeckoURL, strings.Join(ids, ","),
	)

	var data map[string]struct {
		USD float64 `json:"usd"`
	}
	if err := s.getJSON(ctx, endpoint, &data); err != nil {
		return nil, err
	}
	for token, cgID := range s.cryptoIDs {
		if entry, found := data[cgID]; found {
			out[token] = entry.USD
		}
	}
	return out, nil
}

// fetchFiat pulls USD->fiat rates from Frankfurter, seeding USD=1.0.
func (s *Service) fetchFiat(ctx context.Context) (map[string]float64, error) {
	endpoint := s.frankfurterURL + "/latest?from=USD"

	var data struct {
		Rates map[string]float64 `json:"rates"`
	}
	if err := s.getJSON(ctx, endpoint, &data); err != nil {
		return nil, err
	}
	fiat := make(map[string]float64, len(data.Rates)+1)
	fiat["USD"] = 1.0
	for code, rate := range data.Rates {
		fiat[code] = rate
	}
	return fiat, nil
}

// getJSON performs a GET and decodes a 2xx JSON body into out, returning an
// error for transport failures and non-200 responses.
func (s *Service) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("exchange: GET %s returned HTTP %d", url, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
