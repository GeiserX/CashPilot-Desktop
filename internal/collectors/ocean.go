package collectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strings"
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
		return Result{}, collectorError{"auth", "Enter the Bitcoin payout address your miner uses on OCEAN, without the worker name."}
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
		return Result{}, collectorError{"transient", "Could not reach OCEAN. Try again shortly."}
	case status < http.StatusOK || status >= http.StatusMultipleChoices:
		return Result{}, fmt.Errorf("OCEAN returned HTTP %d", status)
	case strings.Contains(strings.ToLower(response.Error), "no such user"):
		return Result{}, errors.New("OCEAN has no record of this address yet. Check the address and whether your miner has sent shares.")
	case response.Error != "":
		return Result{}, collectorError{"shape", "OCEAN returned an error while reading the pool balance."}
	}
	balance, parseErr := response.Result.Unpaid.Float64()
	if err != nil || parseErr != nil || balance < 0 || math.IsNaN(balance) || math.IsInf(balance, 0) {
		return Result{}, collectorError{"shape", "OCEAN's answer has no valid unpaid balance."}
	}
	return Result{
		Platform: "ocean", Currency: "BTC", Balance: balance,
		PoolAddressHash: poolAddressHash(address),
	}, nil
}
