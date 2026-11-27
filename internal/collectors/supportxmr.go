package collectors

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"

	"github.com/GeiserX/CashPilot-Desktop/internal/i18n"
)

// Mainnet standard and subaddresses have 95 Base58 characters. An integrated
// address has 106. The pool checks the address after this format check.
var moneroAddress = regexp.MustCompile(`^(?:[48][1-9A-HJ-NP-Za-km-z]{94}|4[1-9A-HJ-NP-Za-km-z]{105})$`)

const supportXMRAddressKey = "SUPPORTXMR_ADDRESS"

func (r *Registry) collectSupportXMR(ctx context.Context, credentials map[string]string) (Result, error) {
	address := firstCredential(credentials, supportXMRAddressKey)
	if !moneroAddress.MatchString(address) {
		return Result{}, collectorError{"auth", i18n.T("go.collectors.supportxmr.enterAddress", "coin", "Monero", "provider", "SupportXMR", "worker", ".workername", "difficulty", "+difficulty")}
	}

	body, status, _, err := r.doRaw(ctx, http.MethodGet, "https://www.supportxmr.com/api/miner/"+address+"/stats", nil, nil, nil)
	if err != nil {
		return Result{}, collectorError{"transient", i18n.T("go.collectors.pool.readFailed", "provider", "SupportXMR")}
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return Result{}, errors.New(i18n.T("go.collectors.pool.http", "provider", "SupportXMR", "status", status))
	}

	var stats map[string]json.RawMessage
	if err := json.Unmarshal(body, &stats); err != nil || stats == nil {
		return Result{}, collectorError{"shape", i18n.T("go.collectors.supportxmr.invalidReply", "provider", "SupportXMR")}
	}
	due, dueErr := strconv.ParseUint(string(stats["amtDue"]), 10, 64)
	paid, paidErr := strconv.ParseUint(string(stats["amtPaid"]), 10, 64)
	lastHash, lastHashErr := strconv.ParseUint(string(stats["lastHash"]), 10, 64)
	totalHashes, totalHashesErr := strconv.ParseUint(string(stats["totalHashes"]), 10, 64)
	if dueErr != nil || paidErr != nil || lastHashErr != nil || totalHashesErr != nil {
		return Result{}, collectorError{"shape", i18n.T("go.collectors.supportxmr.badFields", "provider", "SupportXMR")}
	}
	// An unseen address returns zeros. txnCount can be zero even after a payment.
	if due == 0 && paid == 0 && lastHash == 0 && totalHashes == 0 {
		return Result{}, errors.New(i18n.T("go.collectors.supportxmr.noRecord", "provider", "SupportXMR"))
	}

	const piconeroPerXMR = 1e12
	return Result{
		Platform: "supportxmr", Currency: "XMR", Balance: float64(due) / piconeroPerXMR,
		PoolAddressHash: poolAddressHash(address),
	}, nil
}
