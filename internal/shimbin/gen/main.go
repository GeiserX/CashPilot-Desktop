// Command gen builds cmd/cashpilot-env for linux/amd64 and linux/arm64 into
// internal/shimbin/bin. It is go generate's step for package shimbin, written in Go
// so it runs the same on the Linux, macOS and Windows release runners.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func main() {
	for _, arch := range []string{"amd64", "arm64"} {
		out := filepath.Join("bin", "cashpilot-env-linux-"+arch)
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w -buildid=", "-o", out, "../../cmd/cashpilot-env")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "building %s: %v\n", out, err)
			os.Exit(1)
		}
	}
}
