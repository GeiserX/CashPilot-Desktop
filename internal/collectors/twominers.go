package collectors

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/i18n"
)

// An Ethereum Classic address is 0x and 40 hex digits. Case is only a checksum,
// so the collector sends and hashes the lowercase form.
var evmAddress = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

// 2Miners writes amounts and times as plain JSON integers.
var twoMinersInteger = regexp.MustCompile(`^[0-9]{1,20}$`)

const twoMinersETCAddressKey = "TWOMINERS_ETC_ADDRESS"

func twoMinersETCAddress(credentials map[string]string) string {
	return strings.ToLower(firstCredential(credentials, twoMinersETCAddressKey))
}

// twoMinersUint reads a JSON integer below 2^64. Strings, floats, booleans,
// negatives and null are not integers.
func twoMinersUint(raw json.RawMessage) (uint64, bool) {
	if !twoMinersInteger.Match(raw) {
		return 0, false
	}
	value, err := strconv.ParseUint(string(raw), 10, 64)
	return value, err == nil
}

func (r *Registry) collectTwoMinersETC(ctx context.Context, credentials map[string]string) (Result, error) {
	if !evmAddress.MatchString(firstCredential(credentials, twoMinersETCAddressKey)) {
		return Result{}, collectorError{"auth", i18n.T("go.collectors.twominers.enterAddress", "coin", "ETC", "provider", "2Miners", "prefix", "0x")}
	}
	address := twoMinersETCAddress(credentials)
	unseen := errors.New(i18n.T("go.collectors.pool.noRecord", "provider", "2Miners"))

	body, status, _, err := r.doRaw(ctx, http.MethodGet, "https://etc.2miners.com/api/accounts/"+address, nil, nil, nil)
	if err != nil {
		// The request URL holds the payout address, so the transport error is not shown.
		return Result{}, collectorError{"transient", i18n.T("go.collectors.pool.readFailed", "provider", "2Miners")}
	}
	if status == http.StatusNotFound {
		return Result{}, unseen
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		message := i18n.T("go.collectors.pool.httpPeriod", "provider", "2Miners", "status", status)
		// The endpoint is public. A refused request says nothing about the address.
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return Result{}, collectorError{"transient", message}
		}
		return Result{}, errors.New(message)
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return Result{}, collectorError{"shape", i18n.T("go.collectors.pool.invalidJSON", "provider", "2Miners")}
	}
	var stats map[string]json.RawMessage
	_, hasError := payload["error"]
	if payload == nil || hasError || json.Unmarshal(payload["stats"], &stats) != nil || stats == nil {
		return Result{}, collectorError{"shape", i18n.T("go.collectors.pool.noAccountStats", "provider", "2Miners")}
	}

	// Older replies for an unseen address carried a zero lastShare.
	lastShare, lastShareOK := twoMinersUint(stats["lastShare"])
	if lastShareOK && lastShare == 0 {
		return Result{}, unseen
	}
	// An address the pool has seen can answer with no balance at all. That is not 0.
	balance, balanceOK := twoMinersUint(stats["balance"])
	if !balanceOK || !lastShareOK {
		return Result{}, collectorError{"shape", i18n.T("go.collectors.pool.badUnpaidOrShare", "provider", "2Miners")}
	}

	// The pool counts 1 billion units per ETC. The balance is accrued at the pool, not received.
	const unitsPerETC = 1e9
	return Result{
		Platform: "2miners-etc", Currency: "ETC", Balance: float64(balance) / unitsPerETC,
		PoolAddressHash: poolAddressHash(address),
	}, nil
}
