//go:build windows

package winvm

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/yusufpapurcu/wmi"
)

var (
	cimClient  = wmi.Client{PtrNil: true}
	cpuWMI     sync.Mutex
	featureWMI sync.Mutex
)

// Keep at most one read of each kind in flight. Snapshot can give up waiting
// when a WMI provider stalls, but COM may still be inside the query.
func lockWMI(ctx context.Context, mu *sync.Mutex) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !mu.TryLock() {
		return nil, errors.New("previous WMI read is still running")
	}
	return mu.Unlock, nil
}

func readCPUWMI(ctx context.Context) (func(*HostFacts), error) {
	release, err := lockWMI(ctx, &cpuWMI)
	if err != nil {
		return nil, err
	}
	defer release()
	var computer []struct {
		HypervisorPresent *bool
		Manufacturer      *string
		Model             *string
	}
	var boot []struct{ LastBootUpTime *time.Time }
	var cpu []struct {
		Architecture                            *int
		VirtualizationFirmwareEnabled           *bool
		SecondLevelAddressTranslationExtensions *bool
	}

	var c cimFacts
	if err := cimClient.Query("SELECT HypervisorPresent, Manufacturer, Model FROM Win32_ComputerSystem", &computer); err == nil && len(computer) > 0 {
		c.HypervisorPresent, c.Manufacturer, c.Model = computer[0].HypervisorPresent, computer[0].Manufacturer, computer[0].Model
	}
	if ctx.Err() == nil {
		if err := cimClient.Query("SELECT LastBootUpTime FROM Win32_OperatingSystem", &boot); err == nil && len(boot) > 0 && boot[0].LastBootUpTime != nil {
			value := boot[0].LastBootUpTime.UTC().Format(time.RFC3339Nano)
			c.LastBoot = &value
		}
	}
	if ctx.Err() == nil {
		if err := cimClient.Query("SELECT Architecture, VirtualizationFirmwareEnabled, SecondLevelAddressTranslationExtensions FROM Win32_Processor", &cpu); err == nil && len(cpu) > 0 {
			c.Architecture = cpu[0].Architecture
			c.Firmware = cpu[0].VirtualizationFirmwareEnabled
			c.SLAT = cpu[0].SecondLevelAddressTranslationExtensions
		}
	}
	return c.apply, ctx.Err()
}

func readFeaturesWMI(ctx context.Context) (func(*HostFacts), error) {
	release, err := lockWMI(ctx, &featureWMI)
	if err != nil {
		return nil, err
	}
	defer release()
	var features []struct {
		Name         string
		InstallState *int
	}
	var c cimFacts
	err = cimClient.Query("SELECT Name, InstallState FROM Win32_OptionalFeature WHERE Name='Microsoft-Windows-Subsystem-Linux' OR Name='VirtualMachinePlatform'", &features)
	if err != nil {
		return nil, err
	}
	absent := 3
	c.WSLFeature, c.VMPFeature = &absent, &absent
	for _, feature := range features {
		switch feature.Name {
		case "Microsoft-Windows-Subsystem-Linux":
			c.WSLFeature = feature.InstallState
		case "VirtualMachinePlatform":
			c.VMPFeature = feature.InstallState
		}
	}
	return c.apply, ctx.Err()
}
