//go:build !linux

// Command cashpilot-env runs only inside a Linux container; see main.go.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "cashpilot-env runs only inside a Linux container")
	os.Exit(127)
}
