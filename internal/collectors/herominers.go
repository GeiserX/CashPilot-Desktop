package collectors

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/url"
	"regexp"

	"github.com/GeiserX/CashPilot-Desktop/internal/i18n"
)

// Kaspa mainnet uses 61 characters for Schnorr and script hashes, 63 for ECDSA.
// The pool checks the checksum after this format check.
var kaspaAddress = regexp.MustCompile(`^kaspa:[qpzry9x8gf2tvdw0s3jn54khce6mua7l]{61}(?:[qpzry9x8gf2tvdw0s3jn54khce6mua7l]{2})?$`)

// HeroMiners writes amounts as unsigned decimal strings in the coin's base unit.
var heroMinersUnits = regexp.MustCompile(`^[0-9]{1,20}$`)

const heroMinersKaspaAddressKey = "HEROMINERS_KAS_ADDRESS"

func (r *Registry) collectHeroMinersKaspa(ctx context.Context, credentials map[string]string) (Result, error) {
	address := firstCredential(credentials, heroMinersKaspaAddressKey)
	addressError := collectorError{"auth", i18n.T("go.collectors.herominers.enterAddress", "coin", "KAS", "provider", "HeroMiners", "prefix", "kaspa:")}
	if !kaspaAddress.MatchString(address) {
		return Result{}, addressError
	}

	query := url.Values{"address": {address}, "recentBlocksAmount": {"0"}, "longpoll": {"false"}}
	body, status, _, err := r.doRaw(ctx, http.MethodGet, "https://kaspa.herominers.com/api/stats_address?"+query.Encode(), nil, nil, nil)
	if err != nil {
		// The request URL holds the payout address, so the transport error is not shown.
		return Result{}, collectorError{"transient", i18n.T("go.collectors.pool.readFailed", "provider", "HeroMiners")}
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		message := i18n.T("go.collectors.pool.httpPeriod", "provider", "HeroMiners", "status", status)
		// The endpoint is public. Only its JSON can reject an address.
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return Result{}, collectorError{"transient", message}
		}
		return Result{}, errors.New(message)
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return Result{}, collectorError{"shape", i18n.T("go.collectors.pool.invalidJSON", "provider", "HeroMiners")}
	}
	if payload == nil {
		return Result{}, collectorError{"shape", i18n.T("go.collectors.pool.notObject", "provider", "HeroMiners")}
	}

	// Both errors come back as HTTP 200.
	rawError, hasError := payload["error"]
	var providerError string
	_ = json.Unmarshal(rawError, &providerError)
	switch {
	case hasError && providerError == "Invalid address":
		return Result{}, addressError
	case hasError && providerError == "Not found":
		return Result{}, errors.New(i18n.T("go.collectors.pool.noRecord", "provider", "HeroMiners"))
	}

	var stats map[string]json.RawMessage
	var balance string
	statsErr := json.Unmarshal(payload["stats"], &stats)
	balanceErr := json.Unmarshal(stats["balance"], &balance)
	if hasError || statsErr != nil || stats == nil || balanceErr != nil || !heroMinersUnits.MatchString(balance) {
		return Result{}, collectorError{"shape", i18n.T("go.collectors.pool.badUnpaid", "provider", "HeroMiners")}
	}

	// The balance is in sompi, 100 million per KAS. It is accrued at the pool, not received.
	sompi, _ := new(big.Int).SetString(balance, 10)
	kas, _ := new(big.Rat).SetFrac(sompi, big.NewInt(100_000_000)).Float64()
	return Result{
		Platform: "herominers-kas", Currency: "KAS", Balance: kas,
		PoolAddressHash: poolAddressHash(address),
	}, nil
}
