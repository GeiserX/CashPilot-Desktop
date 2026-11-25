package main

import "testing"

func TestFitWindowShrinksIntoTheWorkArea(t *testing.T) {
	cases := []struct {
		name         string
		workW, workH int
		want         windowFit
		changed      bool
	}{
		// 1024x768 with a 48-pixel taskbar: the PC of the first Windows walk.
		{"1024x768 screen", 1024, 720, windowFit{W: 1024, H: 720, MinW: 900, MinH: 640}, true},
		// 1366x768 laptop: wide enough, too short.
		{"1366x768 laptop", 1366, 720, windowFit{W: 1200, H: 720, MinW: 900, MinH: 640}, true},
		// Smaller than the minimum: the minimum shrinks too, or it would grow the window back.
		{"800x600 screen", 800, 560, windowFit{W: 800, H: 560, MinW: 800, MinH: 560}, true},
		// Big enough: nothing moves.
		{"1920x1080 screen", 1920, 1032, windowFit{W: 1200, H: 800, MinW: 900, MinH: 640}, false},
		{"exactly the default", 1200, 800, windowFit{W: 1200, H: 800, MinW: 900, MinH: 640}, false},
		// An unread work area changes nothing.
		{"unknown work area", 0, 0, windowFit{W: 1200, H: 800, MinW: 900, MinH: 640}, false},
	}
	for _, c := range cases {
		got, changed := fitWindow(c.workW, c.workH)
		if got != c.want || changed != c.changed {
			t.Errorf("%s: fitWindow(%d, %d) = %+v, %v; want %+v, %v", c.name, c.workW, c.workH, got, changed, c.want, c.changed)
		}
		if c.workW > 0 && (got.W > c.workW || got.H > c.workH) {
			t.Errorf("%s: window %dx%d is larger than the work area %dx%d", c.name, got.W, got.H, c.workW, c.workH)
		}
		if got.MinW > got.W || got.MinH > got.H {
			t.Errorf("%s: minimum %dx%d is larger than the window %dx%d", c.name, got.MinW, got.MinH, got.W, got.H)
		}
	}
}

func TestLogicalPixelsUseTheWindowDPI(t *testing.T) {
	cases := []struct{ physical, dpi, want int }{
		{720, 96, 720},   // 100%
		{1080, 144, 720}, // 150%
		{1440, 192, 720}, // 200%
		{720, 0, 720},    // unknown DPI reads as 96
	}
	for _, c := range cases {
		if got := logicalPixels(c.physical, c.dpi); got != c.want {
			t.Errorf("logicalPixels(%d, %d) = %d, want %d", c.physical, c.dpi, got, c.want)
		}
	}
	// A 1366x768 laptop at 125% has 1092x576 logical pixels free above a 48-pixel taskbar.
	fit, changed := fitWindow(logicalPixels(1366, 120), logicalPixels(768-48, 120))
	if !changed || fit.W != 1092 || fit.H != 576 {
		t.Errorf("1366x720 at 125%% = %+v, %v; want 1092x576", fit, changed)
	}
}
