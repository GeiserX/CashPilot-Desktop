package collectors

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

// EarnApp and ProxyRack pay a device only once its id is on the user's account; a
// node left unlinked runs, looks healthy and earns for nobody. Where the collector
// already holds the account credentials it asks the provider whether the deployed id
// is on the account, and only the provider saying "not on this account" counts. A
// check that cannot finish (an error, ProxyRack's 5-a-minute rate limit) proves
// nothing, so it leaves the last answer standing rather than clearing it now and
// raising it again an hour later. The web app does the same (GeiserX/CashPilot#469).

// deviceState is the last finished answer per slug: the deployed ids the provider
// says are not on the account. A slug is absent when the last finished check found
// nothing missing, or when no check has finished yet.
type deviceState struct {
	mu       sync.Mutex
	unlinked map[string][]string
}

// Unlinked returns the deployed device ids the provider last said are not on the
// account for slug, or nil.
func (r *Registry) Unlinked(slug string) []string {
	r.devices.mu.Lock()
	defer r.devices.mu.Unlock()
	return append([]string(nil), r.devices.unlinked[slug]...)
}

// recordDeviceCheck stores a finished check. Callers skip it when the check did not
// finish, which leaves the previous answer as it was.
func (r *Registry) recordDeviceCheck(slug string, missing []string) {
	r.devices.mu.Lock()
	defer r.devices.mu.Unlock()
	if len(missing) == 0 {
		delete(r.devices.unlinked, slug)
		return
	}
	if r.devices.unlinked == nil {
		r.devices.unlinked = map[string][]string{}
	}
	r.devices.unlinked[slug] = missing
}

const (
	earnAppDevicesURL    = "https://earnapp.com/dashboard/api/devices?appid=earnapp&version=1.627.783"
	proxyRackStatusURL   = "https://peer.proxyrack.com/api/device/status"
	proxyRackAddURL      = "https://peer.proxyrack.com/api/device/add"
	proxyRackInvalidText = "device id is invalid"
)

// checkEarnAppDevice asks EarnApp's device list whether uuid is on the account. The
// list holds every device on the account, banned ones too, so listed means linked.
func (r *Registry) checkEarnAppDevice(ctx context.Context, uuid string, headers map[string]string, cookies []*http.Cookie) {
	if uuid == "" {
		return
	}
	var list []struct {
		UUID string `json:"uuid"`
	}
	if err := r.doJSONWithCookies(ctx, "GET", earnAppDevicesURL, nil, headers, cookies, &list); err != nil {
		return
	}
	for _, device := range list {
		if device.UUID == uuid {
			r.recordDeviceCheck("earnapp", nil)
			return
		}
	}
	r.recordDeviceCheck("earnapp", []string{uuid})
}

// proxyRackStatus is what /device/status says about deviceID: "linked" for a 200
// success, "unlinked" for the exact 400 "The selected device id is invalid.", and ""
// for anything else (rate limit, outage), which is not an answer.
func (r *Registry) proxyRackStatus(ctx context.Context, apiKey, deviceID string) string {
	raw, status, _, err := r.doRaw(ctx, "POST", proxyRackStatusURL, map[string]string{"device_id": deviceID}, proxyRackHeaders(apiKey), nil)
	if err != nil {
		return ""
	}
	var body struct {
		Status  string `json:"status"`
		Details struct {
			DeviceID string `json:"device_id"`
		} `json:"details"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return ""
	}
	switch {
	case status == http.StatusOK && body.Status == "success":
		return "linked"
	case status == http.StatusBadRequest && strings.Contains(strings.ToLower(body.Details.DeviceID), proxyRackInvalidText):
		return "unlinked"
	}
	return ""
}

func (r *Registry) checkProxyRackDevice(ctx context.Context, apiKey, uuid string) {
	if uuid == "" {
		return
	}
	switch r.proxyRackStatus(ctx, apiKey, uuid) {
	case "linked":
		r.recordDeviceCheck("proxyrack", nil)
	case "unlinked":
		r.recordDeviceCheck("proxyrack", []string{uuid})
	}
}

// RegisterDevice adds a just-deployed device to the user's account, where the
// provider's API allows it and the credentials hold the key: today ProxyRack, with
// /device/add, the call its own dashboard makes. A redeploy adds a UUID that is
// already there, which the API refuses, so a refused add is followed by one status
// check: a known id is linked. Returns whether the device is on the account
// afterwards; false also means "this provider cannot do it", and the deploy output
// then shows the manual step.
func (r *Registry) RegisterDevice(ctx context.Context, slug string, credentials map[string]string) bool {
	if slug != "proxyrack" {
		return false
	}
	apiKey := firstCredential(credentials, "API_KEY", "PROXYRACK_API_KEY", "api_key")
	uuid := strings.TrimSpace(credentials["UUID"])
	if apiKey == "" || uuid == "" {
		return false
	}
	name := strings.TrimSpace(credentials["DEVICE_NAME"])
	if name == "" || strings.Contains(name, "${") {
		name = "cashpilot-" + uuid[:min(8, len(uuid))]
	}
	var added struct {
		Status string `json:"status"`
	}
	status, err := r.doJSONStatus(ctx, "POST", proxyRackAddURL, map[string]string{"device_id": uuid, "device_name": name}, proxyRackHeaders(apiKey), nil, &added)
	linked := err == nil && status == http.StatusOK && added.Status == "success"
	if !linked {
		linked = r.proxyRackStatus(ctx, apiKey, uuid) == "linked"
	}
	if linked {
		r.recordDeviceCheck("proxyrack", nil)
	}
	return linked
}

func proxyRackHeaders(apiKey string) map[string]string {
	return map[string]string{"Api-Key": apiKey, "Accept": "application/json"}
}
