//go:build windows

package main

import (
	"context"
	"os"
	"sync"
	"unsafe"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/sys/windows"
)

var (
	user32                = windows.NewLazySystemDLL("user32.dll")
	procMonitorFromWindow = user32.NewProc("MonitorFromWindow")
	procGetMonitorInfoW   = user32.NewProc("GetMonitorInfoW")
	procGetDpiForWindow   = user32.NewProc("GetDpiForWindow")
)

const monitorDefaultToNearest = 2

// monitorInfo is MONITORINFO: the monitor's rectangle and its work area, the
// part the taskbar and docked bars leave free, in screen pixels.
type monitorInfo struct {
	cbSize    uint32
	rcMonitor windows.Rect
	rcWork    windows.Rect
	dwFlags   uint32
}

// wailsWindowClass is the class Wails registers for its window
// (internal/frontend/desktop/windows/window.go).
const wailsWindowClass = "wailsWindow"

var fitOnce sync.Once

// fitMainWindow shrinks the main window into the work area of its monitor when
// the default size does not fit there, then centres it. It runs once, at the
// first DomReady, so a size the user picks later is left alone.
func fitMainWindow(ctx context.Context) {
	fitOnce.Do(func() {
		hwnd := mainWindowHandle()
		if hwnd == 0 {
			return
		}
		workW, workH, dpi, ok := workArea(hwnd)
		if !ok {
			return
		}
		fit, changed := fitWindow(logicalPixels(workW, dpi), logicalPixels(workH, dpi))
		if !changed {
			return
		}
		wailsruntime.WindowSetMinSize(ctx, fit.MinW, fit.MinH)
		wailsruntime.WindowSetSize(ctx, fit.W, fit.H)
		wailsruntime.WindowCenter(ctx)
	})
}

// mainWindowHandle finds this process's Wails window. Wails does not hand out
// its handle, so it is the top-level window of this process with Wails' class.
func mainWindowHandle() windows.HWND {
	pid := uint32(os.Getpid())
	var found windows.HWND
	cb := windows.NewCallback(func(hwnd windows.HWND, _ uintptr) uintptr {
		var owner uint32
		if _, err := windows.GetWindowThreadProcessId(hwnd, &owner); err != nil || owner != pid {
			return 1
		}
		buf := make([]uint16, 64)
		n, err := windows.GetClassName(hwnd, &buf[0], int32(len(buf)))
		if err != nil || windows.UTF16ToString(buf[:n]) != wailsWindowClass {
			return 1
		}
		found = hwnd
		return 0 // stop
	})
	// EnumWindows reports an error when the callback stops it early; found says
	// whether the window was seen.
	_ = windows.EnumWindows(cb, nil)
	return found
}

// workArea reads the work area of the window's monitor, in screen pixels, and
// the window's DPI (96 when Windows cannot say).
func workArea(hwnd windows.HWND) (w, h, dpi int, ok bool) {
	monitor, _, _ := procMonitorFromWindow.Call(uintptr(hwnd), monitorDefaultToNearest)
	if monitor == 0 {
		return 0, 0, 0, false
	}
	info := monitorInfo{}
	info.cbSize = uint32(unsafe.Sizeof(info))
	if r, _, _ := procGetMonitorInfoW.Call(monitor, uintptr(unsafe.Pointer(&info))); r == 0 {
		return 0, 0, 0, false
	}
	dpi = 96
	if procGetDpiForWindow.Find() == nil {
		if d, _, _ := procGetDpiForWindow.Call(uintptr(hwnd)); d != 0 {
			dpi = int(d)
		}
	}
	return int(info.rcWork.Right - info.rcWork.Left), int(info.rcWork.Bottom - info.rcWork.Top), dpi, true
}
