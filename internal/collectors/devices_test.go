package collectors

import (
	"context"
	"slices"
	"testing"
)

// Response bodies are the shapes EarnApp and ProxyRack returned on 2026-10-29.

const (
	testEarnAppUUID   = "sdk-node-0123456789abcdef0123456789abcdef"
	testProxyRackUUID = "ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789"
	earnAppRotate     = "GET https://earnapp.com/dashboard/api/sec/rotate_xsrf"
	earnAppMoney      = "GET https://earnapp.com/dashboard/api/money"
	earnAppDevices    = "GET https://earnapp.com/dashboard/api/devices"
	proxyRackBalance  = "POST https://peer.proxyrack.com/api/balance"
	proxyRackStatus   = "POST https://peer.proxyrack.com/api/device/status"
	proxyRackAdd      = "POST https://peer.proxyrack.com/api/device/add"
	invalidDeviceBody = `{"status":"error","error":"Invalid request parameters","details":{"device_id":"The selected device id is invalid."}}`
	rateLimitedBody   = `{"status":"error","error":"Requests limit exceeded (5 per minute)."}`
	onlineBody        = `{"status":"success","data":{"device_id":"x","status":"Online: 192.0.2.1"}}`
)

func collectEarnAppWith(t *testing.T, r *Registry, stub *stubTransport, devices stubResponse) Result {
	t.Helper()
	stub.routes = map[string]stubResponse{
		earnAppRotate:  {},
		earnAppMoney:   {body: `{"balance":1.5}`},
		earnAppDevices: devices,
	}
	result, err := r.collectEarnApp(context.Background(), map[string]string{"EARNAPP_OAUTH_TOKEN": "t", "EARNAPP_UUID": testEarnAppUUID})
	if err != nil || result.Balance != 1.5 {
		t.Fatalf("collectEarnApp = %+v, %v; the balance must not depend on the device check", result, err)
	}
	return result
}

func TestEarnAppReportsAUUIDMissingFromTheAccount(t *testing.T) {
	stub := &stubTransport{}
	r := newTestRegistry(t, stub)

	collectEarnAppWith(t, r, stub, stubResponse{body: `[{"uuid":"sdk-node-ffffffffffffffffffffffffffffffff","title":"other"}]`})
	if got := r.Unlinked("earnapp"); !slices.Equal(got, []string{testEarnAppUUID}) {
		t.Fatalf("Unlinked = %v, want the deployed UUID", got)
	}

	// An unreadable list proves nothing: the last answer stands.
	collectEarnAppWith(t, r, stub, stubResponse{status: 403, body: `{}`})
	if got := r.Unlinked("earnapp"); !slices.Equal(got, []string{testEarnAppUUID}) {
		t.Fatalf("after a failed check Unlinked = %v, want the previous answer kept", got)
	}

	collectEarnAppWith(t, r, stub, stubResponse{body: `[{"uuid":"` + testEarnAppUUID + `","title":"box"}]`})
	if got := r.Unlinked("earnapp"); got != nil {
		t.Fatalf("a listed UUID is linked, Unlinked = %v", got)
	}
}

func collectProxyRackWith(t *testing.T, r *Registry, stub *stubTransport, status stubResponse) {
	t.Helper()
	stub.routes = map[string]stubResponse{
		proxyRackBalance: {body: `{"data":{"balance":"$2.00"}}`},
		proxyRackStatus:  status,
	}
	result, err := r.collectProxyRack(context.Background(), map[string]string{"API_KEY": "k", "UUID": testProxyRackUUID})
	if err != nil || result.Balance != 2 {
		t.Fatalf("collectProxyRack = %+v, %v; the balance must not depend on the device check", result, err)
	}
}

func TestProxyRackReportsOnlyTheInvalidIDAnswer(t *testing.T) {
	stub := &stubTransport{}
	r := newTestRegistry(t, stub)

	collectProxyRackWith(t, r, stub, stubResponse{status: 400, body: invalidDeviceBody})
	if got := r.Unlinked("proxyrack"); !slices.Equal(got, []string{testProxyRackUUID}) {
		t.Fatalf("Unlinked = %v, want the deployed UUID", got)
	}

	// A rate-limited answer is never "not linked" and never "linked".
	collectProxyRackWith(t, r, stub, stubResponse{status: 400, body: rateLimitedBody})
	if got := r.Unlinked("proxyrack"); !slices.Equal(got, []string{testProxyRackUUID}) {
		t.Fatalf("after a rate-limited check Unlinked = %v, want the previous answer kept", got)
	}

	collectProxyRackWith(t, r, stub, stubResponse{body: onlineBody})
	if got := r.Unlinked("proxyrack"); got != nil {
		t.Fatalf("a known device is linked, Unlinked = %v", got)
	}

	fresh := newTestRegistry(t, stub)
	collectProxyRackWith(t, fresh, stub, stubResponse{status: 400, body: rateLimitedBody})
	if got := fresh.Unlinked("proxyrack"); got != nil {
		t.Fatalf("a rate limit alone must never report a device, Unlinked = %v", got)
	}
}

func TestRegisterDeviceAddsAProxyRackUUIDWithTheSavedKey(t *testing.T) {
	creds := map[string]string{"API_KEY": "k", "UUID": testProxyRackUUID, "DEVICE_NAME": "box"}
	for _, tc := range []struct {
		name   string
		routes map[string]stubResponse
		want   bool
	}{
		{"added", map[string]stubResponse{proxyRackAdd: {body: `{"status":"success","message":"Device added successfully","data":{}}`}}, true},
		{"already on the account", map[string]stubResponse{
			proxyRackAdd:    {status: 400, body: `{"status":"error","error":"Invalid request parameters","details":{"device_id":"taken"}}`},
			proxyRackStatus: {body: onlineBody},
		}, true},
		{"rate limited", map[string]stubResponse{
			proxyRackAdd:    {status: 400, body: rateLimitedBody},
			proxyRackStatus: {status: 400, body: rateLimitedBody},
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubTransport{routes: tc.routes}
			r := newTestRegistry(t, stub)
			if got := r.RegisterDevice(context.Background(), "proxyrack", creds); got != tc.want {
				t.Fatalf("RegisterDevice = %v, want %v", got, tc.want)
			}
		})
	}

	stub := &stubTransport{routes: map[string]stubResponse{proxyRackAdd: {body: `{"status":"success"}`}}}
	r := newTestRegistry(t, stub)
	if r.RegisterDevice(context.Background(), "proxyrack", map[string]string{"UUID": testProxyRackUUID}) {
		t.Fatal("without an API key nothing can be added")
	}
	if r.RegisterDevice(context.Background(), "earnapp", map[string]string{"EARNAPP_UUID": testEarnAppUUID, "EARNAPP_OAUTH_TOKEN": "t"}) {
		t.Fatal("EarnApp has no API to add a device; its step stays manual")
	}
}
