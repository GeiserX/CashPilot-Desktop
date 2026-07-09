package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
)

// runDaemon runs CashPilot headless: it brings the app core up (the native-earner
// supervisor, the loopback fleet API, the exchange refresh and the background
// scheduler) with NO Wails window, tray or webview, then blocks until the process is
// signalled — so an OS service manager can keep native earners supervised while the GUI
// is closed. It is the --daemon counterpart of the Wails OnStartup + OnShutdown pair:
// startCore is the exact same core init the GUI uses, and app.Shutdown is the exact same
// teardown OnShutdown runs (stop the scheduler, close the fleet API, close the store).
//
// This is Phase A of the native-supervision plan: it only proves the one binary can run
// headless and keep the supervisor + fleet server alive. It deliberately does NOT
// register itself with launchd / systemd / Task Scheduler — that is Phase B.
func runDaemon() error {
	// Cancel the core's context on SIGINT/SIGTERM so a service manager (or Ctrl-C)
	// drives a clean shutdown. This signal context — NOT a Wails runtime context — is
	// what startCore stores as a.ctx, which is exactly why emitEvent/emitError safely
	// no-op in this mode (the context carries no "events" value, so no window/tray code
	// is ever reached). SIGTERM is a no-op signal on Windows but harmless to register.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app := NewApp()

	log.Println("cashpilot: starting daemon (headless native-earner supervisor + fleet API, no GUI)")
	if err := app.startCore(ctx); err != nil {
		// A fatal core failure (config/store/catalog) may have left part of the engine
		// initialised (e.g. an opened store). Run the normal teardown before exiting so
		// nothing is leaked, then report the error to the caller (main log.Fatals it).
		app.Shutdown(ctx)
		return fmt.Errorf("daemon core init failed: %w", err)
	}

	log.Println("cashpilot: daemon started; supervising native earners — send SIGINT/SIGTERM to stop")
	<-ctx.Done()
	log.Println("cashpilot: signal received, shutting down daemon")

	// Same shutdown the Wails OnShutdown hook runs: stop the scheduler (waits for the
	// collection loop to drain), close the fleet API, close the store.
	app.Shutdown(ctx)
	log.Println("cashpilot: daemon stopped")
	return nil
}
