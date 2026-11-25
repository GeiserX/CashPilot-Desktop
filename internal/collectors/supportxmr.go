package collectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
)

// Mainnet standard and subaddresses have 95 Base58 characters. An integrated
// address has 106. The pool checks the address after this format check.
var moneroAddress = regexp.MustCompile(`^(?:[48][1-9A-HJ-NP-Za-km-z]{94}|4[1-9A-HJ-NP-Za-km-z]{105})$`)

const supportXMRAddressKey = "SUPPORTXMR_ADDRESS"

func (r *Registry) collectSupportXMR(ctx context.Context, credentials map[string]string) (Result, error) {
	address := firstCredential(credentials, supportXMRAddressKey)
	if !moneroAddress.MatchString(address) {
		return Result{}, collectorError{"auth", "Enter the Monero payout address your miner uses on SupportXMR, without .workername or +difficulty."}
	}

	body, status, _, err := r.doRaw(ctx, http.MethodGet, "https://www.supportxmr.com/api/miner/"+address+"/stats", nil, nil, nil)
	if err != nil {
		return Result{}, collectorError{"transient", "Could not read the SupportXMR pool balance. Try again shortly."}
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return Result{}, fmt.Errorf("SupportXMR returned HTTP %d", status)
	}

	var stats map[string]json.RawMessage
	if err := json.Unmarshal(body, &stats); err != nil || stats == nil {
		return Result{}, collectorError{"shape", "SupportXMR returned an invalid reply. CashPilot expected a JSON object."}
	}
	due, dueErr := strconv.ParseUint(string(stats["amtDue"]), 10, 64)
	paid, paidErr := strconv.ParseUint(string(stats["amtPaid"]), 10, 64)
	lastHash, lastHashErr := strconv.ParseUint(string(stats["lastHash"]), 10, 64)
	totalHashes, totalHashesErr := strconv.ParseUint(string(stats["totalHashes"]), 10, 64)
	if dueErr != nil || paidErr != nil || lastHashErr != nil || totalHashesErr != nil {
		return Result{}, collectorError{"shape", "SupportXMR's reply has missing or invalid balance or mining history fields."}
	}
	// An unseen address returns zeros. txnCount can be zero even after a payment.
	if due == 0 && paid == 0 && lastHash == 0 && totalHashes == 0 {
		return Result{}, errors.New("SupportXMR has no record of this address yet. Check the payout address and whether your miner has sent shares.")
	}

	const piconeroPerXMR = 1e12
	return Result{
		Platform: "supportxmr", Currency: "XMR", Balance: float64(due) / piconeroPerXMR,
		PoolAddressHash: poolAddressHash(address),
	}, nil
}
