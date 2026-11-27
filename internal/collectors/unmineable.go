package collectors

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/i18n"
)

// unMineable pays in the coin the miner chose, so its public stats are keyed by
// the payout address and the coin. The collector reads the two values the deploy
// form already asks for (services/mining/unmineable.yml docker.env).
const (
	unMineableAddressKey = "WALLET"
	unMineableCoinKey    = "PAYOUT_COIN"
)

// The characters a coin ticker and a payout address may have, as the web
// collector and the catalog patterns. Anything else (a ".", ":", "#", "/" or
// "?") would change the request, so it is refused before one is sent. Coins
// that need a memo or tag are not supported.
var (
	unMineableCoin    = regexp.MustCompile(`^[A-Za-z0-9]{2,10}$`)
	unMineableAddress = regexp.MustCompile(`^[A-Za-z0-9]{20,128}$`)
	// unMineable writes amounts as decimal strings in coin units. Same forms as
	// the web's Decimal for a finite non-negative number, so ".5" and "1." too.
	unMineableDecimal = regexp.MustCompile(`^(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][-+]?[0-9]+)?$`)
)

// unMineableIdentity is what a balance belongs to: the coin and the address.
// Switching coins starts a new balance, exactly as switching addresses does.
func unMineableIdentity(credentials map[string]string) string {
	coin := strings.ToUpper(firstCredential(credentials, unMineableCoinKey))
	address := firstCredential(credentials, unMineableAddressKey)
	if coin == "" || address == "" {
		return ""
	}
	return coin + ":" + address
}

// unMineableAmount reads a non-negative decimal string, or reports false.
func unMineableAmount(raw json.RawMessage) (float64, bool) {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return 0, false
	}
	text = strings.TrimSpace(text)
	if !unMineableDecimal.MatchString(text) {
		return 0, false
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
		return 0, false
	}
	return value, true
}

func (r *Registry) collectUnMineable(ctx context.Context, credentials map[string]string) (Result, error) {
	coin := strings.ToUpper(firstCredential(credentials, unMineableCoinKey))
	if !unMineableCoin.MatchString(coin) {
		return Result{}, collectorError{"auth", i18n.T("go.collectors.unmineable.enterCoin", "provider", "unMineable", "examples", "XMR, SHIB or DOGE")}
	}
	address := firstCredential(credentials, unMineableAddressKey)
	if !unMineableAddress.MatchString(address) {
		return Result{}, collectorError{"auth", i18n.T("go.collectors.unmineable.enterAddress", "coin", coin, "provider", "unMineable")}
	}

	endpoint := "https://api.unminable.com/v4/address/" + address + "?coin=" + url.QueryEscape(coin)
	body, status, _, err := r.doRaw(ctx, http.MethodGet, endpoint, nil, nil, nil)
	if err != nil {
		// The request URL holds the payout address, so the transport error is not shown.
		return Result{}, collectorError{"transient", i18n.T("go.collectors.unmineable.readFailed", "provider", "unMineable")}
	}

	// The endpoint is public. A refused request says nothing about the address,
	// whatever its body says, so it is checked before the pool's message.
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return Result{}, collectorError{"transient", i18n.T("go.collectors.pool.httpPeriod", "provider", "unMineable", "status", status)}
	}

	var payload map[string]json.RawMessage
	_ = json.Unmarshal(body, &payload)
	var success *bool
	_ = json.Unmarshal(payload["success"], &success)
	if success != nil && !*success {
		var message string
		_ = json.Unmarshal(payload["msg"], &message)
		// Both come back as HTTP 404. Only a person can fix either one.
		if strings.HasPrefix(message, "Invalid address") {
			return Result{}, collectorError{"auth", i18n.T("go.collectors.unmineable.addressRefused", "provider", "unMineable", "coin", coin)}
		}
		if strings.HasPrefix(message, "Invalid or missing coin") {
			return Result{}, collectorError{"auth", i18n.T("go.collectors.unmineable.unknownCoin", "provider", "unMineable", "coin", coin)}
		}
	}

	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		// A 429 or 5xx never gets here: the retries end in the transport error above.
		return Result{}, errors.New(i18n.T("go.collectors.pool.httpPeriod", "provider", "unMineable", "status", status))
	}

	var data map[string]json.RawMessage
	if success == nil || !*success || json.Unmarshal(payload["data"], &data) != nil || data == nil {
		return Result{}, collectorError{"shape", i18n.T("go.collectors.unmineable.noStats", "provider", "unMineable")}
	}

	// A fresh flag that is not a JSON boolean is a shape the collector does not
	// know. It must not read as false: that would save the 0 of a fresh address.
	var fresh *bool
	if json.Unmarshal(data["fresh"], &fresh) != nil {
		return Result{}, collectorError{"shape", i18n.T("go.collectors.unmineable.badBalance", "provider", "unMineable")}
	}
	if fresh != nil && *fresh {
		// Unknown cause, so no kind: a typo needs a person, while a miner that
		// has not sent its first shares heals by itself. Never a 0 balance.
		return Result{}, errors.New(i18n.T("go.collectors.unmineable.noRecord", "provider", "unMineable", "coin", coin))
	}

	balance, balanceOK := unMineableAmount(data["balance"])
	threshold, thresholdOK := unMineableAmount(data["payment_threshold"])
	if fresh == nil || !balanceOK || !thresholdOK || threshold == 0 {
		return Result{}, collectorError{"shape", i18n.T("go.collectors.unmineable.badBalance", "provider", "unMineable")}
	}

	// The balance is accrued at unMineable until it pays the address, in the chosen coin.
	return Result{
		Platform: "unmineable", Currency: coin, Balance: balance,
		PoolAddressHash: poolAddressHash(unMineableIdentity(credentials)),
	}, nil
}
