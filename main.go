package main

import (
	"embed"
	"flag"
	"io"
	"log"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed services/**/*.yml
var serviceFiles embed.FS

//go:embed build/appicon.png
var appIcon []byte

//go:embed trayicons/solarized-sun.png
var trayIcon []byte

func main() {
	// Dual-role binary: with --daemon (or -daemon) the SAME executable runs headless —
	// no Wails window, tray or webview — bringing the core engine (native-earner
	// supervisor + loopback fleet API + scheduler) up so an OS service manager can keep
	// earners alive after the GUI is closed (see docs/NATIVE-SUPERVISION.md, Phase A).
	// Without the flag it is the normal GUI app, unchanged.
	//
	// Parse with a private ContinueOnError FlagSet whose errors/usage are discarded,
	// never the default ExitOnError parser: a GUI launch (Finder / Wails) may carry OS
	// args we do not define (e.g. a macOS -psn_… process serial), and those must fall
	// through to wails.Run rather than exit(2) the process. flag.Parse never mutates
	// os.Args, so Wails still sees the full argument list.
	fs := flag.NewFlagSet("cashpilot-desktop", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	daemon := fs.Bool("daemon", false, "run headless: supervise native earners and serve the loopback fleet API with no GUI window")
	_ = fs.Parse(os.Args[1:])

	if *daemon {
		if err := runDaemon(); err != nil {
			log.Fatal(err)
		}
		return
	}

	app := NewApp()
	app.trayIcon = trayIcon

	err := wails.Run(&options.App{
		Title:            "CashPilot Desktop",
		Width:            1200,
		Height:           800,
		MinWidth:         900,
		MinHeight:        640,
		Frameless:        true,
		BackgroundColour: options.NewRGB(6, 6, 15),
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup:  app.Startup,
		OnDomReady: app.DomReady,
		OnShutdown: app.Shutdown,
		Mac: &mac.Options{
			Appearance: mac.NSAppearanceNameDarkAqua,
			About: &mac.AboutInfo{
				Title:   "CashPilot Desktop",
				Message: "Local-first passive income and DePIN service manager",
				Icon:    appIcon,
			},
		},
		Linux: &linux.Options{
			Icon:        appIcon,
			ProgramName: "CashPilot Desktop",
		},
		Bind: []interface{}{
			app,
		},
	})
	if err != nil {
		log.Fatal(err)
	}
}
