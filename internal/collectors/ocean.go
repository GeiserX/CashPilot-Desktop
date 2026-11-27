package collectors

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/i18n"
)

var bitcoinAddress = regexp.MustCompile(`^(bc1[02-9ac-hj-np-z]{11,71}|[13][1-9A-HJ-NP-Za-km-z]{25,34})$`)

const oceanAddressKey = "OCEAN_ADDRESS"

type collectorError struct {
	kind    string
	message string
}

func (e collectorError) Error() string { return e.message }

func oceanPayoutAddress(credentials map[string]string) string {
	address := firstCredential(credentials, oceanAddressKey)
	if strings.HasPrefix(strings.ToLower(address), "bc1") {
		return strings.ToLower(address)
	}
	return address
}

func (r *Registry) collectOcean(ctx context.Context, credentials map[string]string) (Result, error) {
	address := oceanPayoutAddress(credentials)
	if !bitcoinAddress.MatchString(address) {
		return Result{}, collectorError{"auth", i18n.T("go.collectors.ocean.enterAddress", "coin", "Bitcoin", "provider", "OCEAN")}
	}

	var response struct {
		Error  string `json:"error"`
		Result struct {
			Unpaid json.Number `json:"unpaid"`
		} `json:"result"`
	}
	status, err := r.doJSONStatus(ctx, http.MethodGet, "https://api.ocean.xyz/v1/statsnap/"+address, nil, nil, nil, &response)
	switch {
	case err != nil && (status == 0 || errors.Is(err, context.DeadlineExceeded)):
		return Result{}, collectorError{"transient", i18n.T("go.collectors.pool.unreachable", "provider", "OCEAN")}
	case status < http.StatusOK || status >= http.StatusMultipleChoices:
		return Result{}, errors.New(i18n.T("go.collectors.pool.http", "provider", "OCEAN", "status", status))
	case strings.Contains(strings.ToLower(response.Error), "no such user"):
		return Result{}, errors.New(i18n.T("go.collectors.pool.noRecordShares", "provider", "OCEAN"))
	case response.Error != "":
		return Result{}, collectorError{"shape", i18n.T("go.collectors.ocean.readError", "provider", "OCEAN")}
	}
	balance, parseErr := response.Result.Unpaid.Float64()
	if err != nil || parseErr != nil || balance < 0 || math.IsNaN(balance) || math.IsInf(balance, 0) {
		return Result{}, collectorError{"shape", i18n.T("go.collectors.ocean.noUnpaid", "provider", "OCEAN")}
	}
	return Result{
		Platform: "ocean", Currency: "BTC", Balance: balance,
		PoolAddressHash: poolAddressHash(address),
	}, nil
}
