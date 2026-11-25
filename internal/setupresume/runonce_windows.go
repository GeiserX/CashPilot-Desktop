//go:build windows

package setupresume

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows/registry"
)

// Arm writes the RunOnce value that reopens exe with --resume-setup at the
// signed-in user's next sign-in. The unelevated app writes it: an elevated
// script may run as another account, whose HKCU is not this user's.
func Arm(exe string) error {
	return arm(registry.CURRENT_USER, RunOnceKey, exe)
}

// Disarm removes the RunOnce value. A value that is not there is not an error.
func Disarm() error {
	return deleteValue(registry.CURRENT_USER, RunOnceKey, ValueName)
}

func arm(root registry.Key, path, exe string) error {
	cmd, err := Command(exe)
	if err != nil {
		return err
	}
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("the app is not at %s: %w", exe, err)
	}
	return setValue(root, path, ValueName, cmd)
}

func setValue(root registry.Key, path, name, value string) error {
	k, _, err := registry.CreateKey(root, path, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(name, value)
}

func deleteValue(root registry.Key, path, name string) error {
	k, err := registry.OpenKey(root, path, registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}
