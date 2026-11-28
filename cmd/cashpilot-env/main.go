//go:build linux

// Command cashpilot-env starts an earner whose credentials Docker never records
// (CASHPILOT_EARNER_SECRETS=file, internal/runtime/secretenv.go).
//
// CashPilot Desktop creates the earner with this program as its entrypoint and the
// earner's own entrypoint and command as its arguments, and writes the secret
// variables to /run/cashpilot-secret/env before the start. The program sets them,
// fills each ${NAME} in its arguments and execs the earner. It needs no shell, so it
// runs in any image. It is built for linux/amd64 and linux/arm64 and embedded in the
// app (internal/shimbin).
//
// Exit codes: 126 when the env file is missing or broken, 127 when the earner cannot
// be run. A missing file stops the earner rather than start it without its login.
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/GeiserX/CashPilot-Desktop/internal/secretshim"
)

const envFile = "/run/cashpilot-secret/env"

func die(code int, format string, args ...any) {
	fmt.Fprintf(os.Stderr, "cashpilot-env: "+format+"\n", args...)
	os.Exit(code)
}

func main() {
	if len(os.Args) < 2 {
		die(127, "no program to run")
	}
	f, err := os.Open(envFile)
	if err != nil {
		die(126, "cannot open %s: %v", envFile, err)
	}
	data, err := io.ReadAll(io.LimitReader(f, secretshim.MaxFile+1))
	f.Close()
	if err != nil {
		die(126, "cannot read %s: %v", envFile, err)
	}
	vars, err := secretshim.Parse(data)
	if err != nil {
		die(126, "%s: %v", envFile, err)
	}
	for _, v := range vars {
		if err := os.Setenv(v.Name, v.Value); err != nil {
			die(126, "cannot set a variable: %v", err)
		}
	}
	args := make([]string, len(os.Args)-1)
	for i, arg := range os.Args[1:] {
		args[i] = secretshim.Expand(arg, vars)
	}
	path, err := exec.LookPath(args[0])
	if err != nil {
		die(127, "%s: %v", args[0], err)
	}
	err = syscall.Exec(path, args, os.Environ())
	die(127, "%s: %v", args[0], err)
}
