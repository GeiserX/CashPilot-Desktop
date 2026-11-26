package collectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
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
		return Result{}, collectorError{"auth", "Enter the ETC payout address your miner uses on 2Miners, starting with 0x and without the worker name."}
	}
	address := twoMinersETCAddress(credentials)
	unseen := errors.New("2Miners has no record of this address yet. Check your payout address and wait for your first shares.")

	body, status, _, err := r.doRaw(ctx, http.MethodGet, "https://etc.2miners.com/api/accounts/"+address, nil, nil, nil)
	if err != nil {
		// The request URL holds the payout address, so the transport error is not shown.
		return Result{}, collectorError{"transient", "Could not read the 2Miners pool balance. Try again shortly."}
	}
	if status == http.StatusNotFound {
		return Result{}, unseen
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		message := fmt.Sprintf("2Miners returned HTTP %d.", status)
		// The endpoint is public. A refused request says nothing about the address.
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return Result{}, collectorError{"transient", message}
		}
		return Result{}, errors.New(message)
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return Result{}, collectorError{"shape", "2Miners did not return valid JSON."}
	}
	var stats map[string]json.RawMessage
	_, hasError := payload["error"]
	if payload == nil || hasError || json.Unmarshal(payload["stats"], &stats) != nil || stats == nil {
		return Result{}, collectorError{"shape", "2Miners did not return account stats."}
	}

	// Older replies for an unseen address carried a zero lastShare.
	lastShare, lastShareOK := twoMinersUint(stats["lastShare"])
	if lastShareOK && lastShare == 0 {
		return Result{}, unseen
	}
	// An address the pool has seen can answer with no balance at all. That is not 0.
	balance, balanceOK := twoMinersUint(stats["balance"])
	if !balanceOK || !lastShareOK {
		return Result{}, collectorError{"shape", "2Miners did not return a valid unpaid balance or share time."}
	}

	// The pool counts 1 billion units per ETC. The balance is accrued at the pool, not received.
	const unitsPerETC = 1e9
	return Result{
		Platform: "2miners-etc", Currency: "ETC", Balance: float64(balance) / unitsPerETC,
		PoolAddressHash: poolAddressHash(address),
	}, nil
}
