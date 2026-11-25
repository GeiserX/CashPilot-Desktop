package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/bgservice"
	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/setupresume"
	"github.com/GeiserX/CashPilot-Desktop/internal/winvm"
)

// The Windows uninstaller (build/windows/installer/project.nsi) runs the
// installed exe with --uninstall-cleanup before it deletes the files. The app
// removes what only it knows about; the installer removes its own files,
// shortcuts and its Apps & Features entry.
const (
	uninstallFlag     = "uninstall-cleanup"
	removeEarnersFlag = "remove-earners"
	deleteDataFlag    = "delete-data"
)

// uninstallSteps are the parts of the cleanup that touch the machine, so a test
// can run the whole sequence against fakes.
type uninstallSteps struct {
	out io.Writer
	// agent is the background helper's login registration (a Scheduled Task on Windows).
	agent bgservice.Agent
	// disarmRunOnce removes the HKCU RunOnce value a guided Docker setup sets
	// (!CashPilotResumeSetup).
	disarmRunOnce func() error
	// removeContainers stops and removes the containers CashPilot Desktop made,
	// and no other container.
	removeContainers func(context.Context) ([]string, error)
	// removeNative stops the native earners, deletes their files and their
	// Windows Firewall rules.
	removeNative func(context.Context) ([]string, error)
	// deleteSecrets removes the app's entries from the OS keychain.
	deleteSecrets func() error
	// appDir is the app's data folder; removeAll deletes it.
	appDir    string
	removeAll func(string) error
	// secureVMDir is the Secure VM's data folder, which this cleanup never removes.
	secureVMDir string
}

// runUninstallMain is the --uninstall-cleanup entry point. It returns the exit code.
func runUninstallMain(removeEarners, deleteData bool, parseErr error) int {
	if parseErr != nil {
		fmt.Fprintln(os.Stderr, "uninstall-cleanup:", parseErr)
		return 2
	}
	appDir, err := config.AppDataDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "uninstall-cleanup: could not find the data folder:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return runUninstall(ctx, uninstallSteps{
		out:           os.Stdout,
		agent:         bgservice.New(bgservice.DefaultLabel, filepath.Join(appDir, "logs")),
		disarmRunOnce: setupresume.Disarm,
		removeContainers: func(ctx context.Context) ([]string, error) {
			ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			return runtime.RemoveDesktopContainers(ctx)
		},
		removeNative:  func(ctx context.Context) ([]string, error) { return removeNativeEarners(ctx, appDir) },
		deleteSecrets: config.DeleteKeychainSecrets,
		appDir:        appDir,
		removeAll:     os.RemoveAll,
		secureVMDir:   winvm.ProductNames().DataDir,
	}, removeEarners, deleteData)
}

// runUninstall removes what the app put on this computer outside its install
// folder. It always removes the background helper and the RunOnce value. It
// removes the earning containers only with removeEarners, and the user's data
// only with deleteData. Native earners go with either, since their files live in
// the data folder. Each step runs even when an earlier one failed; the exit code
// is 1 when any failed, and every line says what happened.
func runUninstall(ctx context.Context, s uninstallSteps, removeEarners, deleteData bool) int {
	failed := false
	fail := func(format string, args ...any) {
		failed = true
		fmt.Fprintf(s.out, "FAILED: "+format+"\n", args...)
	}
	say := func(format string, args ...any) { fmt.Fprintf(s.out, format+"\n", args...) }

	// The background helper. Removing a Scheduled Task does not end the process it
	// started, so it is stopped first. Status reads the exit code, never the text,
	// so this works on a Windows in any language.
	if stopper, ok := s.agent.(bgservice.Stopper); ok {
		_ = stopper.Stop()
	}
	if st, err := s.agent.Status(); err != nil {
		fail("could not read the background helper's registration: %v", err)
	} else if !st.Installed {
		say("No background helper was registered.")
	} else if err := s.agent.Uninstall(); err != nil {
		fail("could not remove the background helper %s: %v", st.Label, err)
	} else {
		say("Removed the background helper (%s).", st.Label)
	}

	if err := s.disarmRunOnce(); err != nil {
		fail("could not remove the RunOnce value %s: %v", setupresume.ValueName, err)
	} else {
		say("Removed the RunOnce value %s, if it was there.", setupresume.ValueName)
	}

	if removeEarners {
		names, err := s.removeContainers(ctx)
		if len(names) > 0 {
			say("Removed %d container(s) CashPilot Desktop made: %s. Their volumes stay.", len(names), strings.Join(names, ", "))
		}
		if err != nil {
			fail("could not remove the earning containers: %v. Start Docker and remove them with: docker rm -f $(docker ps -aq --filter label=cashpilot.runtime=existing-docker)", err)
		} else if len(names) == 0 {
			say("Found no container CashPilot Desktop made.")
		}
	} else {
		say("Kept the earning containers, as asked. They keep running in Docker.")
	}

	if removeEarners || deleteData {
		names, err := s.removeNative(ctx)
		if len(names) > 0 {
			say("Removed %d native earner(s) and their firewall rules: %s.", len(names), strings.Join(names, ", "))
		}
		if err != nil {
			fail("could not remove every native earner: %v", err)
		}
	}

	if deleteData {
		if err := s.deleteSecrets(); err != nil {
			fail("could not remove the saved keys from the system keychain: %v", err)
		} else {
			say("Removed the saved keys from the system keychain (the Windows Credential Manager on Windows).")
		}
		if err := safeDataDir(s.appDir); err != nil {
			fail("did not delete the data folder: %v", err)
		} else if err := s.removeAll(s.appDir); err != nil {
			fail("could not delete the data folder %s: %v", s.appDir, err)
		} else {
			say("Deleted the data folder %s.", s.appDir)
		}
	} else {
		say("Kept your data in %s. Install CashPilot Desktop again, or open the portable exe, to use it.", s.appDir)
	}

	if s.secureVMDir != "" {
		if _, err := os.Stat(s.secureVMDir); err == nil {
			say("The Secure VM (Windows 11 Pro) was not removed. Its files are in %s.", s.secureVMDir)
		}
	}

	if failed {
		return 1
	}
	return 0
}

// safeDataDir refuses a data folder that is not one: empty, relative, a drive
// root, or the user's home folder. The folder can come from
// CASHPILOT_DESKTOP_DATA_DIR, and a recursive delete must never follow a typo
// there to a folder full of other files.
func safeDataDir(dir string) error {
	if dir == "" || !filepath.IsAbs(dir) {
		return fmt.Errorf("%q is not an absolute path", dir)
	}
	clean := filepath.Clean(dir)
	if filepath.Dir(clean) == clean {
		return fmt.Errorf("%s is the root of a drive", clean)
	}
	if home, err := os.UserHomeDir(); err == nil && samePath(clean, home) {
		return fmt.Errorf("%s is your home folder", clean)
	}
	return nil
}

// removeNativeEarners stops every native earner in the registry and removes it
// the way the app's Remove button does: the process, its folder under
// native\<slug>, and its Windows Firewall rule (one admin prompt when a rule
// exists).
func removeNativeEarners(ctx context.Context, appDir string) ([]string, error) {
	native := runtime.NewNativeProcessProvider(appDir)
	infos, err := native.List(ctx)
	if err != nil {
		return nil, err
	}
	var removed []string
	var errs []error
	for _, info := range infos {
		if err := native.Remove(ctx, info.Slug, runtime.RemoveOptions{}); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", info.Slug, err))
			continue
		}
		removed = append(removed, info.Slug)
	}
	return removed, errors.Join(errs...)
}
