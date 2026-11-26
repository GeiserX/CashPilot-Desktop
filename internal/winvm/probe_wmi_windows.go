//go:build windows

package winvm

import "github.com/yusufpapurcu/wmi"

// SWbemServices serializes queries per session. Client.Query would share
// a process-wide lock with the optional-feature provider and other WMI callers.
var unelevatedWMI = wmiProbes{open: func() (wmiSession, error) {
	return wmi.InitializeSWbemServices(&wmi.Client{PtrNil: true})
}}
