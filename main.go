package main

import (
	"embed"
	"log"

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
