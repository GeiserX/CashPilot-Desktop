//go:build windows

package macvm

// The Secure VM engine here runs only on macOS. These keep the package
// building on Windows, where the app never calls it.

import (
	"errors"
	"os"
	"syscall"
)

const oNoFollow = 0

func freeBytes(string) (int64, error) { return 0, errors.New("not on Windows") }

func statOwner(os.FileInfo) (int, bool) { return 0, false }

func ownGroup() *syscall.SysProcAttr { return nil }
