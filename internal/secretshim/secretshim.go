// Package secretshim is the logic of cmd/cashpilot-env, the program that starts an
// earner whose credentials are kept out of Docker's container config
// (CASHPILOT_EARNER_SECRETS=file, internal/runtime/secretenv.go).
//
// The env file holds NAME=value records, each ended by a NUL byte. The program sets
// each variable, puts each value in place of ${NAME} in its arguments and execs the
// earner. It reads the file and expands the arguments as app/shim/cashpilot-env.c in
// the CashPilot repository does for the web worker, so both start an earner the same
// way.
package secretshim

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

const (
	// MaxFile is the largest env file the program reads.
	MaxFile = 1 << 20
	// MaxVars is the most variables the file may hold.
	MaxVars = 256
)

// Var is one record of the env file.
type Var struct {
	Name, Value string
}

// Parse reads the env file. A record with no "=", an empty or invalid name, too many
// records or a file that does not end in NUL is an error: the earner is not started
// on half its credentials.
func Parse(data []byte) ([]Var, error) {
	if len(data) > MaxFile {
		return nil, errors.New("the env file is too large")
	}
	if len(data) == 0 {
		return nil, nil
	}
	if data[len(data)-1] != 0 {
		return nil, errors.New("the env file does not end in a NUL byte")
	}
	records := bytes.Split(data[:len(data)-1], []byte{0})
	if len(records) > MaxVars {
		return nil, errors.New("the env file holds too many variables")
	}
	vars := make([]Var, 0, len(records))
	for i, record := range records {
		name, value, ok := strings.Cut(string(record), "=")
		if !ok || !validName(name) {
			return nil, fmt.Errorf("record %d of the env file is not NAME=value", i+1)
		}
		vars = append(vars, Var{name, value})
	}
	return vars, nil
}

func validName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// Expand puts each value in place of its ${NAME} in arg, in one pass from the left.
// A ${NAME} for a name the file does not hold stays as written, and a value put in
// is never read again.
func Expand(arg string, vars []Var) string {
	values := make(map[string]string, len(vars))
	for _, v := range vars {
		values[v.Name] = v.Value
	}
	var out strings.Builder
	for i := 0; i < len(arg); {
		if strings.HasPrefix(arg[i:], "${") {
			if end := strings.IndexByte(arg[i+2:], '}'); end >= 0 {
				if value, ok := values[arg[i+2:i+2+end]]; ok {
					out.WriteString(value)
					i += end + 3
					continue
				}
			}
		}
		out.WriteByte(arg[i])
		i++
	}
	return out.String()
}

// Hide is the reverse of Expand, for the worker side: arg with each value replaced by
// ${NAME}, longest value first at each position. A placeholder once written is never
// searched again, so Expand gives back exactly arg.
func Hide(arg string, vars []Var) string {
	ordered := make([]Var, 0, len(vars))
	for _, v := range vars {
		if v.Value != "" {
			ordered = append(ordered, v)
		}
	}
	// Stable by length, longest first.
	for i := 1; i < len(ordered); i++ {
		for j := i; j > 0 && len(ordered[j].Value) > len(ordered[j-1].Value); j-- {
			ordered[j], ordered[j-1] = ordered[j-1], ordered[j]
		}
	}
	var out strings.Builder
	for i := 0; i < len(arg); {
		matched := false
		for _, v := range ordered {
			if strings.HasPrefix(arg[i:], v.Value) {
				out.WriteString("${" + v.Name + "}")
				i += len(v.Value)
				matched = true
				break
			}
		}
		if !matched {
			out.WriteByte(arg[i])
			i++
		}
	}
	return out.String()
}

// Encode writes vars as the env file.
func Encode(vars []Var) []byte {
	var buf bytes.Buffer
	for _, v := range vars {
		buf.WriteString(v.Name + "=" + v.Value)
		buf.WriteByte(0)
	}
	return buf.Bytes()
}
