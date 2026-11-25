//go:build windows

package winvm

import (
	"context"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The drop counter's fallback read: PDH with the English counter names, which
// work on a host in any language. It is a Go call into pdh.dll, not PowerShell.

var (
	modpdh                     = windows.NewLazySystemDLL("pdh.dll")
	procPdhOpenQueryW          = modpdh.NewProc("PdhOpenQueryW")
	procPdhAddEnglishCounterW  = modpdh.NewProc("PdhAddEnglishCounterW")
	procPdhCollectQueryData    = modpdh.NewProc("PdhCollectQueryData")
	procPdhGetRawCounterArrayW = modpdh.NewProc("PdhGetRawCounterArrayW")
	procPdhCloseQuery          = modpdh.NewProc("PdhCloseQuery")
)

const (
	pdhMoreData = 0x800007D2
	// The wildcard instance adds every adapter; pickDropInstance keeps ours.
	pdhOutPath = `\Hyper-V Virtual Network Adapter Drop Reasons(*)\Outgoing FailedSecurityPolicy`
	pdhInPath  = `\Hyper-V Virtual Network Adapter Drop Reasons(*)\Incoming FailedSecurityPolicy`
)

// pdhRawCounter is PDH_RAW_COUNTER. The explicit pad puts FirstValue at offset
// 16, where MSVC puts it.
type pdhRawCounter struct {
	CStatus     uint32
	TimeStamp   windows.Filetime
	_           uint32
	FirstValue  int64
	SecondValue int64
	MultiCount  uint32
	_           uint32
}

// pdhRawCounterItem is PDH_RAW_COUNTER_ITEM_W on 64-bit Windows, the only
// Windows the app ships for.
type pdhRawCounterItem struct {
	Name     *uint16
	RawValue pdhRawCounter
}

// pdhDropInstances reads every instance of the two FailedSecurityPolicy drop
// counters.
func pdhDropInstances(ctx context.Context) ([]DropInstance, error) {
	if err := modpdh.Load(); err != nil {
		return nil, err
	}
	var q windows.Handle
	if r, _, _ := procPdhOpenQueryW.Call(0, 0, uintptr(unsafe.Pointer(&q))); r != 0 {
		return nil, fmt.Errorf("PdhOpenQueryW: 0x%X", r)
	}
	defer procPdhCloseQuery.Call(uintptr(q))
	var out, in windows.Handle
	for _, c := range []struct {
		path string
		h    *windows.Handle
	}{{pdhOutPath, &out}, {pdhInPath, &in}} {
		p, err := windows.UTF16PtrFromString(c.path)
		if err != nil {
			return nil, err
		}
		if r, _, _ := procPdhAddEnglishCounterW.Call(uintptr(q), uintptr(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(c.h))); r != 0 {
			return nil, fmt.Errorf("PdhAddEnglishCounterW: 0x%X", r)
		}
	}
	if r, _, _ := procPdhCollectQueryData.Call(uintptr(q)); r != 0 {
		return nil, fmt.Errorf("PdhCollectQueryData: 0x%X", r)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	outs, err := pdhRawArray(out)
	if err != nil {
		return nil, err
	}
	ins, err := pdhRawArray(in)
	if err != nil {
		return nil, err
	}
	var all []DropInstance
	for name, v := range outs {
		iv, ok := ins[name]
		if !ok {
			continue
		}
		all = append(all, DropInstance{Name: name, Out: v, In: iv})
	}
	return all, nil
}

// pdhRawArray reads one wildcard counter as instance name to raw value. A value
// whose status is not valid is left out.
func pdhRawArray(c windows.Handle) (map[string]uint64, error) {
	var size, count uint32
	r, _, _ := procPdhGetRawCounterArrayW.Call(uintptr(c), uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), 0)
	if r != pdhMoreData {
		return nil, fmt.Errorf("PdhGetRawCounterArrayW (size): 0x%X", r)
	}
	if size == 0 || count == 0 {
		return map[string]uint64{}, nil
	}
	buf := make([]byte, size)
	r, _, _ = procPdhGetRawCounterArrayW.Call(uintptr(c), uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&buf[0])))
	if r != 0 {
		return nil, fmt.Errorf("PdhGetRawCounterArrayW: 0x%X", r)
	}
	items := unsafe.Slice((*pdhRawCounterItem)(unsafe.Pointer(&buf[0])), count)
	out := map[string]uint64{}
	for _, it := range items {
		// PDH_CSTATUS_VALID_DATA and PDH_CSTATUS_NEW_DATA.
		if it.RawValue.CStatus != 0 && it.RawValue.CStatus != 1 {
			continue
		}
		if it.Name == nil || it.RawValue.FirstValue < 0 {
			continue
		}
		out[windows.UTF16PtrToString(it.Name)] = uint64(it.RawValue.FirstValue)
	}
	return out, nil
}
