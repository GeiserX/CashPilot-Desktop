package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/bgservice"
	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/i18n"
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
	fail := func(message string) {
		failed = true
		fmt.Fprintln(s.out, i18n.T("go.app.uninstall.failed", "message", message))
	}
	say := func(message string) { fmt.Fprintln(s.out, message) }

	// The background helper. Removing a Scheduled Task does not end the process it
	// started, so it is stopped first. Status reads the exit code, never the text,
	// so this works on a Windows in any language.
	if stopper, ok := s.agent.(bgservice.Stopper); ok {
		_ = stopper.Stop()
	}
	if st, err := s.agent.Status(); err != nil {
		fail(i18n.T("go.app.uninstall.agentStatusFailed", "error", err))
	} else if !st.Installed {
		say(i18n.T("go.app.uninstall.noAgent"))
	} else if err := s.agent.Uninstall(); err != nil {
		fail(i18n.T("go.app.uninstall.agentRemoveFailed", "label", st.Label, "error", err))
	} else {
		say(i18n.T("go.app.uninstall.agentRemoved", "label", st.Label))
	}

	if err := s.disarmRunOnce(); err != nil {
		fail(i18n.T("go.app.uninstall.runOnceFailed", "value", setupresume.ValueName, "error", err))
	} else {
		say(i18n.T("go.app.uninstall.runOnceRemoved", "value", setupresume.ValueName))
	}

	if removeEarners {
		names, err := s.removeContainers(ctx)
		if len(names) > 0 {
			say(i18n.T("go.app.uninstall.containersRemoved", "n", len(names), "names", strings.Join(names, ", ")))
		}
		if err != nil {
			fail(i18n.T("go.app.uninstall.containersFailed", "error", err, "command", "docker rm -f $(docker ps -aq --filter label=cashpilot.runtime=existing-docker)"))
		} else if len(names) == 0 {
			say(i18n.T("go.app.uninstall.noContainers"))
		}
	} else {
		say(i18n.T("go.app.uninstall.containersKept"))
	}

	if removeEarners || deleteData {
		names, err := s.removeNative(ctx)
		if len(names) > 0 {
			say(i18n.T("go.app.uninstall.nativeRemoved", "n", len(names), "names", strings.Join(names, ", ")))
		}
		if err != nil {
			fail(i18n.T("go.app.uninstall.nativeFailed", "error", err))
		}
	}

	if deleteData {
		if err := s.deleteSecrets(); err != nil {
			fail(i18n.T("go.app.uninstall.secretsFailed", "error", err))
		} else {
			say(i18n.T("go.app.uninstall.secretsRemoved"))
		}
		if err := safeDataDir(s.appDir); err != nil {
			fail(i18n.T("go.app.uninstall.dataRefused", "error", err))
		} else if err := s.removeAll(s.appDir); err != nil {
			fail(i18n.T("go.app.uninstall.dataFailed", "path", s.appDir, "error", err))
		} else {
			say(i18n.T("go.app.uninstall.dataDeleted", "path", s.appDir))
		}
	} else {
		say(i18n.T("go.app.uninstall.dataKept", "path", s.appDir))
	}

	if s.secureVMDir != "" {
		if _, err := os.Stat(s.secureVMDir); err == nil {
			say(i18n.T("go.app.uninstall.secureVMKept", "path", s.secureVMDir))
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
		return errors.New(i18n.T("go.app.uninstall.notAbsolute", "path", strconv.Quote(dir)))
	}
	clean := filepath.Clean(dir)
	if filepath.Dir(clean) == clean {
		return errors.New(i18n.T("go.app.uninstall.driveRoot", "path", clean))
	}
	if home, err := os.UserHomeDir(); err == nil && samePath(clean, home) {
		return errors.New(i18n.T("go.app.uninstall.homeFolder", "path", clean))
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
