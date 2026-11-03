// Command cpbackup opens a CashPilot backup (.cpbackup) on your own machine.
//
//	go run github.com/GeiserX/CashPilot-Desktop/cmd/cpbackup@latest decrypt FILE --out DIR
//	go run github.com/GeiserX/CashPilot-Desktop/cmd/cpbackup@latest keygen
//
// decrypt writes one tar per backed-up folder into DIR, which you then extract into
// an empty volume before the service first starts (docs/backup.md). It opens files
// from CashPilot Desktop and from the CashPilot web app alike.
//
// The passphrase or private key is read from the terminal without echo (or from
// stdin when piped), never from the command line, where it would land in shell
// history and the process list.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/statebackup"
	"golang.org/x/term"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, readSecret))
}

// readSecret reads one line without echo from a terminal, or plainly from a pipe.
func readSecret(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, prompt)
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func run(args []string, stdout, stderr io.Writer, secret func(string) (string, error)) int {
	usage := "usage: cpbackup decrypt FILE --out DIR | cpbackup keygen"
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	switch args[0] {
	case "keygen":
		priv, pub, err := statebackup.GenerateKeypair()
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
		fmt.Fprintf(stdout, "private key (keep this, offline; it opens your backups): %s\n", priv)
		fmt.Fprintf(stdout, "public key (paste this into CashPilot): %s\n", pub)
		return 0
	case "decrypt":
	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}

	fs := flag.NewFlagSet("decrypt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "folder to write the tar files into (created if missing)")
	// Let the file come before or after --out.
	var file string
	rest := args[1:]
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		file, rest = rest[0], rest[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if file == "" && fs.NArg() > 0 {
		file = fs.Arg(0)
	}
	if file == "" || *out == "" {
		fmt.Fprintln(stderr, usage)
		return 2
	}

	bundle, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	header, err := statebackup.ReadHeader(bundle)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	var payload []byte
	if header.Mode == statebackup.ModePassphrase {
		s, rerr := secret("Passphrase: ")
		if rerr != nil {
			fmt.Fprintln(stderr, "error:", rerr)
			return 1
		}
		payload, err = statebackup.Open(bundle, s, "")
	} else {
		s, rerr := secret("Private key (hex): ")
		if rerr != nil {
			fmt.Fprintln(stderr, "error:", rerr)
			return 1
		}
		payload, err = statebackup.Open(bundle, "", strings.TrimSpace(s))
	}
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	archives, err := statebackup.SplitArchives(payload)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}

	targets := map[string]string{}
	if list, ok := header.Metadata["targets"].([]any); ok {
		for _, t := range list {
			if s, ok := t.(string); ok {
				trimmed := strings.TrimRight(s, "/")
				targets[trimmed[strings.LastIndex(trimmed, "/")+1:]] = s
			}
		}
	}
	fmt.Fprintf(stdout, "%v from %v, checksum verified.\n", orUnknown(header.Metadata["slug"]), orUnknown(header.Metadata["worker"]))
	if err := os.MkdirAll(*out, 0o700); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	names := make([]string, 0, len(archives))
	for name := range archives {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(*out, name+".tar")
		// O_EXCL: never overwrite, so an earlier restore is not lost to a typo.
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			fmt.Fprintf(stderr, "error: %s already exists or cannot be written (%v). Use an empty --out folder.\n", path, err)
			return 1
		}
		_, werr := f.Write(archives[name])
		cerr := f.Close()
		if werr != nil || cerr != nil {
			fmt.Fprintf(stderr, "error: could not write %s\n", path)
			return 1
		}
		where := targets[name]
		if where == "" {
			where = "/" + name
		}
		fmt.Fprintf(stdout, "  %s  ->  restores %s\n", path, where)
	}
	return 0
}

func orUnknown(v any) any {
	if v == nil || v == "" {
		return "?"
	}
	return v
}
