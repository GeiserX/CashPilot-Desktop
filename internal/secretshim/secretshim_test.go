package secretshim

import (
	"strings"
	"testing"
)

func TestParseReadsEveryRecord(t *testing.T) {
	vars, err := Parse([]byte("A=1\x00B_2=x=y\x00EMPTY=\x00"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Var{{"A", "1"}, {"B_2", "x=y"}, {"EMPTY", ""}}
	if len(vars) != len(want) {
		t.Fatalf("got %v", vars)
	}
	for i := range want {
		if vars[i] != want[i] {
			t.Fatalf("record %d: got %v, want %v", i, vars[i], want[i])
		}
	}
}

func TestParseRefusesABrokenFile(t *testing.T) {
	for name, data := range map[string]string{
		"no NUL at the end":       "A=1",
		"no equals sign":          "A\x00",
		"empty name":              "=1\x00",
		"name with a space":       "A B=1\x00",
		"name with a digit first": "1A=1\x00",
		"too many":                strings.Repeat("A=1\x00", MaxVars+1),
		"too large":               "A=" + strings.Repeat("x", MaxFile) + "\x00",
	} {
		if _, err := Parse([]byte(data)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
	if vars, err := Parse(nil); err != nil || len(vars) != 0 {
		t.Fatalf("an empty file: %v %v", vars, err)
	}
}

func TestExpandFillsOnlyTheNamesInTheFile(t *testing.T) {
	vars := []Var{{"PW", "s3cr${OTHER}et"}, {"OTHER", "o"}}
	cases := map[string]string{
		"-pass":            "-pass",
		"${PW}":            "s3cr${OTHER}et", // a value put in is never read again
		"--p=${PW}!":       "--p=s3cr${OTHER}et!",
		"${NOPE} ${OTHER}": "${NOPE} o",
		"${PW":             "${PW",
		"$${PW}":           "$s3cr${OTHER}et",
		"":                 "",
	}
	for in, want := range cases {
		if got := Expand(in, vars); got != want {
			t.Errorf("Expand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHideThenExpandGivesBackTheArgument(t *testing.T) {
	vars := []Var{{"PW", "pass"}, {"LONG", "password"}, {"ID", "a"}, {"PASS", "${PW}"}}
	for _, arg := range []string{
		"-pass", "password", "passpassword", "--x=pass,a", "plain", "", "$$pass{}", "${NOT_IN_FILE}",
	} {
		hidden := Hide(arg, vars)
		if got := Expand(hidden, vars); got != arg {
			t.Errorf("Expand(Hide(%q)) = %q (hidden %q)", arg, got, hidden)
		}
		for _, v := range vars {
			if v.Value != "" && strings.Contains(arg, v.Value) && hidden == arg {
				t.Errorf("Hide(%q) left %s in place", arg, v.Name)
			}
		}
	}
	if got := Hide("password", vars); got != "${LONG}" {
		t.Errorf("the longest value goes first: %q", got)
	}
}

func TestEncodeIsWhatParseReads(t *testing.T) {
	vars := []Var{{"A", "x y"}, {"B", "=\n"}}
	back, err := Parse(Encode(vars))
	if err != nil || len(back) != 2 || back[0] != vars[0] || back[1] != vars[1] {
		t.Fatalf("%v %v", back, err)
	}
}

// An argument that already holds ${NAME} for a name in the file does not come back:
// the program fills it in. planSecrets (internal/runtime) refuses such a command and
// keeps the earner on its environment, so Hide is never asked to.
func TestALiteralPlaceholderIsWhatThePlanRefuses(t *testing.T) {
	vars := []Var{{"PW", "pass"}}
	if got := Expand(Hide("${PW}", vars), vars); got == "${PW}" {
		t.Fatal("a literal placeholder survived; the plan's refusal would be needless")
	}
}
