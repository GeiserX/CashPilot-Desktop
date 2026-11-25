package main

// The main window's size. It opens at windowWidth x windowHeight, and the user
// cannot shrink it below windowMinWidth x windowMinHeight.
//
// On a screen smaller than that, Windows clamped the window to about the screen
// and Wails centred it on the work area (the screen minus the taskbar), so the
// window spilled past it: on a 1024x768 PC its top 32 pixels, the title bar with
// Close, sat above the screen, and its bottom covered the taskbar. fitWindow
// shrinks the window to the work area instead (window_fit_windows.go).
const (
	windowWidth     = 1200
	windowHeight    = 800
	windowMinWidth  = 900
	windowMinHeight = 640
)

// windowFit is the size the window takes, and the smallest size it may shrink to.
type windowFit struct {
	W, H, MinW, MinH int
}

// fitWindow fits the window into a work area of workW x workH logical pixels.
// changed is false when the default size already fits, or the work area is
// unknown (zero), and then nothing should move.
func fitWindow(workW, workH int) (fit windowFit, changed bool) {
	fit = windowFit{W: windowWidth, H: windowHeight, MinW: windowMinWidth, MinH: windowMinHeight}
	if workW <= 0 || workH <= 0 {
		return fit, false
	}
	if fit.W > workW {
		fit.W, changed = workW, true
	}
	if fit.H > workH {
		fit.H, changed = workH, true
	}
	// A minimum larger than the window would grow it past the work area again.
	fit.MinW = min(fit.MinW, fit.W)
	fit.MinH = min(fit.MinH, fit.H)
	return fit, changed
}

// logicalPixels turns a length in screen pixels into the 96-DPI pixels Wails
// sizes the window in. At 150% scaling (144 DPI) 1080 screen pixels are 720.
func logicalPixels(physical, dpi int) int {
	if dpi <= 0 {
		dpi = 96
	}
	return physical * 96 / dpi
}
