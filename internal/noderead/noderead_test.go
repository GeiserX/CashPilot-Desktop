package noderead

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// What the catalog's command prints inside a Mysterium node: the identity, a blank
// line, then the settlement history. Shaped on TequilAPI's real answers.
const identityAndHistory = `{"id":"0x1111111111111111111111111111111111111111","registration_status":"Registered",` +
	`"earnings_tokens":{"wei":"1250000000000000000","ether":"1.25","human":"1.250"},` +
	`"earnings_total_tokens":{"wei":"9500000000000000000","ether":"9.5","human":"9.500"}}
{"items":[{"amount":4000,"fees":1000},{"amount":2000,"fees":500}],"page":1}`

// mysteriumReader compiles the node_read the vendored catalog really declares, so a
// change there that Go's regexp cannot run fails here.
func mysteriumReader(t *testing.T) *Reader {
	t.Helper()
	cat, err := catalog.LoadEmbedded(os.DirFS(filepath.Join("..", "..")))
	if err != nil {
		t.Fatal(err)
	}
	svc, ok := cat.Get("mysterium")
	if !ok {
		t.Fatal("mysterium is missing from the catalog")
	}
	r, ok := Compile(svc.Collector.NodeRead)
	if !ok {
		t.Fatalf("the catalog's mysterium node_read does not compile in Go: %+v", svc.Collector.NodeRead)
	}
	return r
}

func TestExtractReadsTheCatalogsMysteriumAnswer(t *testing.T) {
	got, ok := mysteriumReader(t).Extract(identityAndHistory)
	if !ok {
		t.Fatal("no reading from a well-formed TequilAPI answer")
	}
	if got.Node != "0x1111111111111111111111111111111111111111" || got.Balance != 1.25 || got.Lifetime != 9.5 {
		t.Errorf("got %+v, want node 0x1111…, balance 1.25, lifetime 9.5", got)
	}
	if got.SettledNet == nil || got.SettledFees == nil || *got.SettledNet != 6000 || *got.SettledFees != 1500 {
		t.Errorf("settled sums = %v/%v, want 6000/1500", got.SettledNet, got.SettledFees)
	}
}

func TestExtractRefusesWhatIsNotAReading(t *testing.T) {
	r := mysteriumReader(t)
	cases := map[string]string{
		"empty":                  "",
		"no lifetime":            `{"id":"0x1111111111111111111111111111111111111111","earnings_tokens":{"ether":"1"}}`,
		"lifetime below balance": `{"id":"0x1111111111111111111111111111111111111111","earnings_tokens":{"ether":"5"},"earnings_total_tokens":{"ether":"4"}}`,
	}
	for name, out := range cases {
		if got, ok := r.Extract(out); ok {
			t.Errorf("%s: got a reading %+v, want none", name, got)
		}
	}
	// A failed history read leaves the figures and drops only the settled sums.
	got, ok := r.Extract(`{"id":"0x1111111111111111111111111111111111111111","earnings_tokens":{"ether":"1"},"earnings_total_tokens":{"ether":"2"}}`)
	if !ok || got.SettledNet != nil || got.SettledFees != nil {
		t.Errorf("no history: got %+v ok=%v, want figures without settled sums", got, ok)
	}
}

func TestCompileRefusesAMalformedSpec(t *testing.T) {
	good := catalog.NodeRead{Command: []string{"true"}, Node: `id=(\w+)`, Balance: `b=([0-9.]+)`, Lifetime: `l=([0-9.]+)`}
	if _, ok := Compile(&good); !ok {
		t.Fatal("control: a well-formed spec must compile")
	}
	bad := []catalog.NodeRead{
		{Node: good.Node, Balance: good.Balance, Lifetime: good.Lifetime},
		{Command: good.Command, Node: `id=\w+`, Balance: good.Balance, Lifetime: good.Lifetime},
		{Command: good.Command, Node: good.Node, Balance: `(`, Lifetime: good.Lifetime},
		{Command: good.Command, Node: good.Node, Balance: good.Balance},
	}
	for i, spec := range bad {
		if _, ok := Compile(&spec); ok {
			t.Errorf("case %d compiled, want refused: %+v", i, spec)
		}
	}
	if _, ok := Compile(nil); ok {
		t.Error("nil spec compiled")
	}
	// Only one of the two sums: they are both-or-neither, so neither is used.
	half := good
	half.SettledNet = `n=(\d+)`
	r, _ := Compile(&half)
	if got, _ := r.Extract("id=a b=1 l=2 n=5"); got.SettledNet != nil {
		t.Errorf("a lone settled_net must be ignored, got %v", *got.SettledNet)
	}
}

type fakeRunner struct {
	outputs []Output
	err     error
	gotCmd  []string
}

func (f *fakeRunner) ExecRunning(_ context.Context, _ string, cmd []string, _ time.Duration) ([]Output, error) {
	f.gotCmd = cmd
	return f.outputs, f.err
}

func TestReadAllGivesOneReadingPerContainer(t *testing.T) {
	r := mysteriumReader(t)
	run := &fakeRunner{outputs: []Output{
		{Instance: 1, Text: identityAndHistory},
		{Instance: 2, Text: "wget: can't connect", ExitCode: 1},
		{Instance: 3, Err: errors.New("container is gone")},
		{Instance: 4, Text: "{}"},
	}}
	got, err := ReadAll(context.Background(), run, "mysterium", r)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.gotCmd) == 0 || run.gotCmd[0] != "sh" {
		t.Errorf("the catalog's command was not the one run: %v", run.gotCmd)
	}
	if len(got) != 4 || !got[0].OK() || got[0].Instance != 1 || got[0].Balance != 1.25 {
		t.Fatalf("got %+v", got)
	}
	for _, reading := range got[1:] {
		if reading.OK() || reading.Error == "" {
			t.Errorf("instance %d: want an error and no figures, got %+v", reading.Instance, reading)
		}
	}
	if _, err := ReadAll(context.Background(), &fakeRunner{err: errors.New("no docker")}, "mysterium", r); err == nil {
		t.Error("a runtime that cannot exec must say so")
	}
}
