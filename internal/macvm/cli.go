package macvm

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

// Flag is the app's command-line switch for the Secure VM's root roles:
//
//	--macvm daemon                       the LaunchDaemon (the guard)
//	--macvm install --owner U --tart-home H --tools T [--deny ip,...] [--app-version V]
//	--macvm remove  --owner U --tart-home H --tools T
//
// install and remove are the admin step: the app runs them through macOS's
// administrator prompt. remove prints its report as JSON.
const Flag = "macvm"

// Main runs one root role and returns the exit code.
func Main(role string, args []string, appVersion string, stdout io.Writer) int {
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "the Secure VM's admin step must run as root")
		return 2
	}
	fs := flag.NewFlagSet("macvm "+role, flag.ContinueOnError)
	owner := fs.String("owner", "", "the user the VM belongs to")
	tartHome := fs.String("tart-home", "", "the owner's Tart folder")
	tools := fs.String("tools", "", "the owner's tool cache")
	deny := fs.String("deny", "", "extra addresses to block, comma separated")
	ver := fs.String("app-version", appVersion, "the app's version")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	logger := log.New(os.Stderr, "", log.LstdFlags)
	d := NewDaemon(logger.Printf)
	switch role {
	case "daemon":
		if err := d.Run(ctx); err != nil {
			logger.Printf("daemon: %v", err)
			return 1
		}
		return 0
	case "install":
		var extra []netip.Addr
		for _, s := range strings.Split(*deny, ",") {
			if s = strings.TrimSpace(s); s == "" {
				continue
			}
			a, err := netip.ParseAddr(s)
			if err != nil {
				fmt.Fprintf(os.Stderr, "--deny: %v\n", err)
				return 2
			}
			extra = append(extra, a)
		}
		err := d.Install(ctx, InstallRequest{Owner: *owner, TartHome: *tartHome, Tools: *tools, Deny: extra, AppVer: *ver})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	case "remove":
		rep := d.Remove(ctx, RemoveRequest{Owner: *owner, TartHome: *tartHome, Tools: *tools})
		// Exit 0 even when something is left: the app runs this through
		// AppleScript, which drops stdout on a nonzero exit, and the report is
		// how the page lists what is left. A failed admin prompt never gets
		// here, so it stays a separate error.
		if err := json.NewEncoder(stdout).Encode(rep); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
	fmt.Fprintf(os.Stderr, "unknown role %q: daemon, install or remove\n", role)
	return 2
}
