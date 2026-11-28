// Package shimbin holds cmd/cashpilot-env built for the Linux architectures an
// earner runs on, so CashPilot Desktop can copy it into an earner
// (internal/runtime/secretenv.go).
//
// The binaries are build output, not source: `go generate ./internal/shimbin` (or
// `make shim`) builds them into bin/, and the release workflow runs it before
// `wails build`. A build without them still works: For returns nil, and the earners
// keep their credentials in their environment, as before.
package shimbin

import "embed"

//go:generate go run ./gen

//go:embed bin
var files embed.FS

// Name is the file name of the program for an architecture family (ArchFamily).
func Name(family string) string { return "cashpilot-env-linux-" + family }

// For is the program for a daemon of the given architecture family ("amd64" or
// "arm64"), or nil when this build does not carry it.
func For(family string) []byte {
	if family != "amd64" && family != "arm64" {
		return nil
	}
	data, err := files.ReadFile("bin/" + Name(family))
	if err != nil || len(data) == 0 {
		return nil
	}
	return data
}
