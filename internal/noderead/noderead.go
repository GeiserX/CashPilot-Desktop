// Package noderead reads a node's own earnings from inside its running container,
// for a provider whose account API cannot give a balance or a lifetime. It ports the
// web worker's app/node_read.py.
//
// Mysterium is the case it exists for: the MystNodes cloud's earningsTotal is a
// trailing 30-day sum that rises and falls as days leave the window, and its per-node
// lifetime comes back as 0. The node itself knows both, through TequilAPI, which
// listens on loopback inside the container. The catalog says how to ask it, in
// collector.node_read: a read-only command to run in the container and the regexes
// that pick the node id, the unsettled balance and the lifetime total out of what it
// prints.
//
// The command comes from the vendored catalog, never from a request, and only the
// matched groups leave this package.
package noderead

import (
	"context"
	"errors"
	"math"
	"regexp"
	"strconv"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// ReadTimeout bounds one container's read, as the web worker's READ_TIMEOUT does.
const ReadTimeout = 10 * time.Second

// ErrNotDeclared means the service's catalog entry declares no usable node_read, so
// the caller keeps whatever it did before.
var ErrNotDeclared = errors.New("the catalog declares no node_read for this service")

// Reader is a compiled collector.node_read.
type Reader struct {
	Command                 []string
	node, balance, lifetime *regexp.Regexp
	settledNet, settledFees *regexp.Regexp
}

// Compile returns the reader a catalog entry declares, or false when it declares
// none or a malformed one: no command, or a node, balance or lifetime pattern that
// does not compile or has no group. The two settled sums are used only when both
// compile with exactly one group, as the web does.
func Compile(spec *catalog.NodeRead) (*Reader, bool) {
	if spec == nil || len(spec.Command) == 0 {
		return nil, false
	}
	r := &Reader{Command: append([]string(nil), spec.Command...)}
	for _, f := range []struct {
		pattern string
		into    **regexp.Regexp
	}{{spec.Node, &r.node}, {spec.Balance, &r.balance}, {spec.Lifetime, &r.lifetime}} {
		re, err := regexp.Compile(f.pattern)
		if f.pattern == "" || err != nil || re.NumSubexp() < 1 {
			return nil, false
		}
		*f.into = re
	}
	net, errNet := regexp.Compile(spec.SettledNet)
	fees, errFees := regexp.Compile(spec.SettledFees)
	if spec.SettledNet != "" && spec.SettledFees != "" && errNet == nil && errFees == nil &&
		net.NumSubexp() == 1 && fees.NumSubexp() == 1 {
		r.settledNet, r.settledFees = net, fees
	}
	return r, true
}

// Reading is one node's figures, or why there are none.
type Reading struct {
	// Instance is which copy of the service the container is (1 for the first).
	Instance int     `json:"instance"`
	Node     string  `json:"node,omitempty"`
	Balance  float64 `json:"balance"`
	Lifetime float64 `json:"lifetime"`
	// SettledNet and SettledFees are what the node's settlements paid to the wallet
	// and what the provider kept, in any one unit; nil when not known.
	SettledNet  *float64 `json:"settledNet,omitempty"`
	SettledFees *float64 `json:"settledFees,omitempty"`
	// Error is set, and the figures are not, when the read did not work.
	Error string `json:"error,omitempty"`
}

// OK reports whether the reading carries figures.
func (r Reading) OK() bool { return r.Error == "" && r.Node != "" }

func amount(text string) (float64, bool) {
	v, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0, false
	}
	return v, true
}

// Extract picks the node's id, unsettled balance and lifetime out of the command's
// output, or returns false when any is missing. A lifetime below the balance is not
// a reading: everything waiting to settle is part of what the node ever earned.
func (r *Reader) Extract(output string) (Reading, bool) {
	var found [3]string
	for i, re := range []*regexp.Regexp{r.node, r.balance, r.lifetime} {
		m := re.FindStringSubmatch(output)
		if m == nil {
			return Reading{}, false
		}
		found[i] = m[1]
	}
	balance, okB := amount(found[1])
	lifetime, okL := amount(found[2])
	if found[0] == "" || !okB || !okL || lifetime < balance {
		return Reading{}, false
	}
	out := Reading{Node: found[0], Balance: balance, Lifetime: lifetime}
	if r.settledNet != nil && r.settledFees != nil {
		// No match at all is "not known" (the history could not be read), not "no
		// fee", so a sum is kept only when both found something and all of it parsed.
		net, okN := sumAll(r.settledNet, output)
		fees, okF := sumAll(r.settledFees, output)
		if okN && okF {
			out.SettledNet, out.SettledFees = &net, &fees
		}
	}
	return out, true
}

func sumAll(re *regexp.Regexp, text string) (float64, bool) {
	matches := re.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return 0, false
	}
	var sum float64
	for _, m := range matches {
		v, ok := amount(m[1])
		if !ok {
			return 0, false
		}
		sum += v
	}
	return sum, true
}

// Output is what one command printed in one container.
type Output struct {
	Instance int
	Text     string
	ExitCode int
	// Err is set when the command could not be run at all.
	Err error
}

// Runner runs a command in every running container CashPilot manages for a service.
// The Docker runtime implements it; a runtime with no containers answers with an
// error, so nothing is read.
type Runner interface {
	ExecRunning(ctx context.Context, slug string, cmd []string, timeout time.Duration) ([]Output, error)
}

// ReadAll returns one reading per running copy of the service: its figures when the
// read worked, its Error when it did not.
func ReadAll(ctx context.Context, run Runner, slug string, r *Reader) ([]Reading, error) {
	outputs, err := run.ExecRunning(ctx, slug, r.Command, ReadTimeout)
	if err != nil {
		return nil, err
	}
	readings := make([]Reading, 0, len(outputs))
	for _, o := range outputs {
		readings = append(readings, r.reading(o))
	}
	return readings, nil
}

// reading turns one output into a reading, by the one parser both paths share.
func (r *Reader) reading(o Output) Reading {
	reading := Reading{Instance: o.Instance}
	switch {
	case o.Err != nil:
		reading.Error = "read failed: " + o.Err.Error()
	case o.ExitCode != 0:
		reading.Error = "the read command exited with " + strconv.Itoa(o.ExitCode)
	default:
		figures, ok := r.Extract(o.Text)
		if !ok {
			reading.Error = "no figures in the node's answer"
		} else {
			figures.Instance = o.Instance
			reading = figures
		}
	}
	return reading
}

// NativeOutput produces, without a container, the text a service's node_read
// command prints inside one. It exists for a node Desktop runs as a native process,
// where the catalog's sh and wget command cannot run (no sh on Windows, no wget on a
// Mac). The text then goes through the same regexes as a container's.
type NativeOutput func(ctx context.Context) (string, error)

// NativeReaders are the services whose node can be read natively, by slug. A service
// that declares node_read but has no entry here is not read when it runs natively.
var NativeReaders = map[string]NativeOutput{
	"mysterium": func(ctx context.Context) (string, error) {
		return TequilAPIOutput(ctx, localClient, MysteriumTequilAPI)
	},
}

// ReadNative reads a natively run node through its NativeOutput: one reading, for
// the one process.
func ReadNative(ctx context.Context, read NativeOutput, r *Reader) []Reading {
	out := Output{Instance: 1}
	out.Text, out.Err = read(ctx)
	return []Reading{r.reading(out)}
}
