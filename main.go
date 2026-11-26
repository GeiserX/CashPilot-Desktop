package main

import (
	"embed"
	"flag"
	"io"
	"log"
	"os"

	"github.com/GeiserX/CashPilot-Desktop/internal/setupresume"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed all:frontend/dist
var assets embed.FS

// services/_ratings.yml is the published grading method; the service files alone
// cannot be graded, and a release without it would show every provider not rated.
//
//go:embed services/**/*.yml services/_ratings.yml
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
	// --resume-setup is what Windows' RunOnce passes after a restart a guided setup
	// asked for: the app opens on the setup checklist (docker_guide.go).
	resume := fs.Bool(setupresume.Flag, false, "open on the setup checklist after a restart")
	// --vm-inspect <desired.json> prints the Secure VM's live Hyper-V state and its
	// drift as JSON, reads only, and exits (vm_inspect.go).
	inspect := fs.String(vmInspectFlag, "", "print the Secure VM's live state and drift from this desired.json, as JSON; changes nothing")
	// --vm-guard <data folder> is the Secure VM's SYSTEM guard: it keeps the VM
	// off the network unless its fence is exactly the proven one (vm_guard.go).
	// It never opens a window. --once boot|pass runs one routine, for the lab.
	guard := fs.String(vmGuardFlag, "", "run the Secure VM's guard on this data folder; no window")
	once := fs.String(vmGuardOnceFlag, "", "with --vm-guard: run one routine (boot or pass) and exit")
	// --vm-setup <params.json> and --vm-remove <data folder> are the Secure VM's
	// elevated setup and removal, run by the admin bootstraps (vm_setup.go).
	setup := fs.String(vmSetupFlag, "", "build or repair the Secure VM from this params file and prove its fence; admin only, no window")
	remove := fs.String(vmRemoveFlag, "", "remove the Secure VM of this data folder; admin only, no window")
	// --uninstall-cleanup is what the Windows uninstaller runs before it deletes the
	// files: it removes the background helper, the RunOnce value and, when asked,
	// the earning containers and the user's data (uninstall.go). No window.
	uninstall := fs.Bool(uninstallFlag, false, "remove what the app set up on this computer, for the uninstaller; no window")
	removeEarners := fs.Bool(removeEarnersFlag, false, "with --uninstall-cleanup: also stop and remove the earners CashPilot Desktop deployed")
	deleteData := fs.Bool(deleteDataFlag, false, "with --uninstall-cleanup: also delete the settings, database, logs and saved keys")
	parseErr := fs.Parse(os.Args[1:])

	if *inspect != "" {
		os.Exit(runVMInspectMain(*inspect))
	}
	if *guard != "" {
		os.Exit(vmGuardMain(*guard, *once, parseErr, runVMGuardMain))
	}
	if *setup != "" || *remove != "" {
		os.Exit(vmSetupMain(*setup, *remove, parseErr))
	}

	if *uninstall {
		os.Exit(runUninstallMain(*removeEarners, *deleteData, parseErr))
	}

	if *daemon {
		if err := runDaemon(); err != nil {
			log.Fatal(err)
		}
		return
	}

	app := NewApp()
	app.trayIcon = trayIcon
	app.resumeSetup.Store(*resume)

	err := wails.Run(&options.App{
		Title:            "CashPilot Desktop",
		Width:            windowWidth,
		Height:           windowHeight,
		MinWidth:         windowMinWidth,
		MinHeight:        windowMinHeight,
		Frameless:        true,
		BackgroundColour: options.NewRGB(6, 6, 15),
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		// One window per user: RunOnce and a manual start at the same sign-in would
		// otherwise open two windows on one store. A second start brings this
		// window forward and passes --resume-setup through.
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId:               "com.cashpilot.desktop.window",
			OnSecondInstanceLaunch: app.onSecondInstance,
		},
		OnStartup:      app.Startup,
		OnDomReady:     app.DomReady,
		OnShutdown:     app.Shutdown,
		ErrorFormatter: formatBindingError,
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
