package runtime

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// Error paths and decisions of the wallet steps, driven against fakeSeedDaemon
// (seedfile_test.go). Every refusal must come before the file changes, say which
// step failed, carry no word of a phrase, and leave no helper behind.

const generatorOutput = "Recovery Phrase: " + testPhraseB + "\nAddress a2a3773f76136bdb05a0ff79a0f4fcc2826436794f8db36db6408355c5ca32345002db43c2eb\n"

func walletCode(err error) string {
	var werr *WalletError
	if errors.As(err, &werr) {
		return werr.Code
	}
	return ""
}

// assertNoPhraseIn fails when err's text holds a word of either test phrase.
func assertNoPhraseIn(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	for _, word := range []string{"legal", "winner", "abandon", "about"} {
		if strings.Contains(err.Error(), word) {
			t.Fatalf("the error holds a phrase word (%q): %v", word, err)
		}
	}
}

// assertNoHelperLeft fails when a wallet helper is still in the fake.
func assertNoHelperLeft(t *testing.T, f *fakeSeedDaemon) {
	t.Helper()
	for name, o := range f.containers {
		if o.Config != nil && o.Config.Labels[LabelWalletHelper] != "" {
			t.Fatalf("helper %s was left behind", name)
		}
	}
}

// Each failure while writing a pasted wallet into the file stops the deploy with
// the file as it was, a write that read back wrong included.
func TestEveryWalletFileFailureLeavesTheFileAsItWas(t *testing.T) {
	const before = "http:\n  address: :9980\n"
	cases := []struct {
		name, code, says string
		setup            func(f *fakeSeedDaemon)
	}{
		{"the helper cannot be created", "", "could not create the helper", func(f *fakeSeedDaemon) {
			f.fail = map[string]error{"create n-wallet": errors.New("no space")}
		}},
		{"the helper's name cannot be checked", "", "could not check n-wallet", func(f *fakeSeedDaemon) {
			f.fail = map[string]error{"inspect n-wallet": errors.New("daemon hiccup")}
		}},
		{"the file cannot be read", "", "could not read Sia (hostd)'s wallet file", func(f *fakeSeedDaemon) {
			f.fail = map[string]error{"copyfrom 1": errors.New("i/o error")}
		}},
		{"Docker refuses the write", "wallet_write", "did not write the wallet file", func(f *fakeSeedDaemon) {
			f.fail = map[string]error{"copyto": errors.New("read-only volume")}
		}},
		{"the write cannot be read back", "wallet_write", "could not be read back", func(f *fakeSeedDaemon) {
			f.fail = map[string]error{"copyfrom 2": errors.New("i/o error")}
		}},
		{"the write reads back as something else", "wallet_write", "did not read back as written", func(f *fakeSeedDaemon) {
			f.afterWrite = func() { f.files["sia-hostd-data/hostd.yml"] = []byte("recoveryPhrase: " + testPhraseB + "\n") }
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSeedDaemon()
			f.files["sia-hostd-data/hostd.yml"] = []byte(before)
			tc.setup(f)
			result, err := prepareWallet(context.Background(), f, seedService(), "img", "n", seedMounts, &WalletRequest{Mode: WalletImport, Phrase: testPhraseA}, "", nil)
			if err == nil || result != nil {
				t.Fatalf("the failure was not refused: %+v", result)
			}
			if walletCode(err) != tc.code || !strings.Contains(err.Error(), tc.says) || !strings.Contains(err.Error(), "Nothing was deployed") {
				t.Fatalf("refusal = %q (code %q), want code %q saying %q", err, walletCode(err), tc.code, tc.says)
			}
			assertNoPhraseIn(t, err)
			assertNoHelperLeft(t, f)
			// A write that could not be confirmed is taken back too.
			if got := string(f.files["sia-hostd-data/hostd.yml"]); got != before {
				t.Fatalf("the file changed although the step failed:\n%s", got)
			}
		})
	}
}

// What the file already holds decides: a wallet there is kept whatever was asked,
// the same phrase pasted again is no change, and another one is refused.
func TestTheWalletInTheFileDecides(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  *WalletRequest
		code string
	}{
		{"keep", &WalletRequest{Mode: WalletKeep}, ""},
		{"no request", nil, ""},
		{"a new wallet", &WalletRequest{Mode: WalletGenerate}, ""},
		{"the same one pasted", &WalletRequest{Mode: WalletImport, Phrase: "  " + strings.ToUpper(testPhraseA)}, ""},
		{"another one pasted", &WalletRequest{Mode: WalletImport, Phrase: testPhraseB}, "wallet_exists"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSeedDaemon()
			f.files["sia-hostd-data/hostd.yml"] = []byte("recoveryPhrase: " + testPhraseA + "\n")
			f.output = generatorOutput
			result, err := prepareWallet(context.Background(), f, seedService(), "img", "n", seedMounts, tc.req, testPhraseB, nil)
			if walletCode(err) != tc.code {
				t.Fatalf("err = %v, want code %q", err, tc.code)
			}
			if tc.code == "" && (result == nil || result.Created || result.Phrase != "" || result.Address != "5a7da9320b1d8cb37801eafa7065175df235be95096e401a187b5a98fda50428ef9e7566c29f") {
				t.Fatalf("the file's wallet was not kept as it is: %+v", result)
			}
			assertNoPhraseIn(t, err)
			if f.writes != 0 || strings.Contains(strings.Join(f.calls, " "), "wallet-new") {
				t.Fatalf("a node with a wallet got a write (%d) or a generator: %v", f.writes, f.calls)
			}
		})
	}
}

// A new wallet comes from the generator's output, is written and shown once, and
// the owner is told what happens.
func TestANewWalletIsGeneratedWrittenAndShownOnce(t *testing.T) {
	f := newFakeSeedDaemon()
	f.output = generatorOutput
	var progress []string
	result, err := prepareWallet(context.Background(), f, seedService(), "img", "n", seedMounts, &WalletRequest{Mode: WalletGenerate}, "", func(m string) { progress = append(progress, m) })
	if err != nil {
		t.Fatalf("prepareWallet: %v", err)
	}
	if !result.Created || result.Migrated || result.Phrase != testPhraseB || !strings.HasPrefix(result.Address, "a2a3773f") {
		t.Fatalf("result = %+v, want a new wallet with its phrase and address", result)
	}
	if got := string(f.files["sia-hostd-data/hostd.yml"]); !strings.Contains(got, "recoveryPhrase: "+testPhraseB) {
		t.Fatalf("the new wallet was not written: %q", got)
	}
	if strings.Join(progress, "|") != "Creating a new wallet, offline|Writing the wallet into Sia (hostd)'s data volume" {
		t.Fatalf("progress = %q", progress)
	}
	gen := f.removed
	if len(gen) != 2 || gen[0] != "n-wallet-new" || gen[1] != "n-wallet" {
		t.Fatalf("removed %v, want the generator, then the file helper", gen)
	}

	// Output with a phrase but no address still makes the wallet: the address is
	// derived from the phrase.
	f = newFakeSeedDaemon()
	f.output = "Recovery Phrase: " + testPhraseB + "\n"
	result, err = prepareWallet(context.Background(), f, seedService(), "img", "n", seedMounts, &WalletRequest{Mode: WalletGenerate}, "", nil)
	if err != nil || !strings.HasPrefix(result.Address, "a2a3773f") {
		t.Fatalf("a wallet with no printed address: %+v, %v", result, err)
	}
}

// Each failure of the generator stops the deploy with nothing written and no
// generator left behind.
func TestEveryGeneratorFailureWritesNothing(t *testing.T) {
	broken := strings.Replace(testPhraseB, "about", "abandon", 1) // a wrong checksum
	cases := []struct {
		name, says string
		setup      func(f *fakeSeedDaemon)
	}{
		{"it cannot be created", "could not create the container", func(f *fakeSeedDaemon) {
			f.fail = map[string]error{"create n-wallet-new": errors.New("no such image")}
		}},
		{"it cannot start", "could not start the container", func(f *fakeSeedDaemon) {
			f.fail = map[string]error{"start n-wallet-new": errors.New("exec format error")}
		}},
		{"its wait fails", "did not finish", func(f *fakeSeedDaemon) {
			f.fail = map[string]error{"wait": errors.New("context deadline exceeded")}
		}},
		{"it exits with an error", "exit 3", func(f *fakeSeedDaemon) { f.exitCode = 3 }},
		{"its log cannot be read", "could not be read from its container (", func(f *fakeSeedDaemon) {
			f.fail = map[string]error{"logs": errors.New("log driver none")}
		}},
		{"its log is not a log stream", "could not be read from its container.", func(f *fakeSeedDaemon) {
			f.logsRaw = []byte{9, 0, 0, 0, 0, 0, 0, 5, 'x'}
		}},
		{"it prints a phrase that does not check out", "did not print a wallet", func(f *fakeSeedDaemon) {
			f.output = "Recovery Phrase: " + broken + "\n"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSeedDaemon()
			f.output = generatorOutput
			tc.setup(f)
			result, err := prepareWallet(context.Background(), f, seedService(), "img", "n", seedMounts, &WalletRequest{Mode: WalletGenerate}, "", nil)
			if result != nil || walletCode(err) != "wallet_generate" || !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("got %+v, %v; want wallet_generate saying %q", result, err, tc.says)
			}
			assertNoPhraseIn(t, err)
			assertNoHelperLeft(t, f)
			if f.writes != 0 {
				t.Fatal("a failed generator still wrote the file")
			}
		})
	}

	// Patterns that do not compile are refused before anything reads the output.
	spec := *seedService().Docker.Env[0].SeedFile
	spec.PhrasePattern = "("
	f := newFakeSeedDaemon()
	f.output = generatorOutput
	if _, _, err := generateWallet(context.Background(), f, "img", "n-wallet-new", spec); err == nil || !strings.Contains(err.Error(), "do not compile") {
		t.Fatalf("a broken pattern was not refused: %v", err)
	}
}

// A request that cannot go ahead is refused before Docker is touched.
func TestAWalletRequestIsCheckedBeforeDockerIsTouched(t *testing.T) {
	svc := seedService()
	for _, tc := range []struct {
		name string
		req  *WalletRequest
		code string
	}{
		{"an empty paste", &WalletRequest{Mode: WalletImport, Phrase: "   "}, "wallet_phrase"},
		{"a short paste", &WalletRequest{Mode: WalletImport, Phrase: "legal winner thank"}, "wallet_phrase"},
		{"an unknown choice", &WalletRequest{Mode: "borrow"}, "wallet_mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSeedDaemon()
			_, err := prepareWallet(context.Background(), f, svc, "img", "n", seedMounts, tc.req, "", nil)
			if walletCode(err) != tc.code || !strings.Contains(err.Error(), "Nothing was deployed") {
				t.Fatalf("err = %v, want %s", err, tc.code)
			}
			assertNoPhraseIn(t, err)
			if len(f.calls) != 0 || f.reads != 0 {
				t.Fatalf("Docker was touched: %v", f.calls)
			}
		})
	}

	// A seed_file the runtime cannot keep safely is refused, asked or not.
	bad := seedService()
	spec := *bad.Docker.Env[0].SeedFile
	spec.Words = 24
	bad.Docker.Env[0].SeedFile = &spec
	for _, req := range []*WalletRequest{nil, {Mode: WalletKeep}} {
		f := newFakeSeedDaemon()
		if _, err := prepareWallet(context.Background(), f, bad, "img", "n", seedMounts, req, "", nil); err == nil || !strings.Contains(err.Error(), "seed_file is not valid") || len(f.calls) != 0 {
			t.Fatalf("an invalid seed_file was not refused before Docker (req %+v): %v", req, err)
		}
	}

	// A deploy that mounts nothing where the file lives cannot keep a wallet.
	f := newFakeSeedDaemon()
	other := []mount.Mount{{Type: mount.TypeVolume, Source: "x", Target: "/elsewhere"}}
	if _, err := prepareWallet(context.Background(), f, svc, "img", "n", other, nil, "", nil); walletCode(err) != "wallet_volume" || len(f.calls) != 0 {
		t.Fatalf("no mount at /data was not refused before Docker: %v", err)
	}

	// A service with no seed file has no wallet step at all.
	plain := catalog.Service{Name: "Plain", Slug: "plain"}
	if result, err := prepareWallet(context.Background(), f, plain, "img", "n", nil, &WalletRequest{Mode: "borrow"}, "", nil); result != nil || err != nil {
		t.Fatalf("a service with no seed file got a wallet step: %+v, %v", result, err)
	}
	if err := CheckWalletRequest(plain, &WalletRequest{Mode: "borrow"}); err != nil {
		t.Fatalf("a service with no seed file had its request checked: %v", err)
	}
}

// Keep, on a node with no wallet anywhere, is refused: there is nothing to keep.
func TestKeepingNoWalletIsRefusedAndNothingIsWritten(t *testing.T) {
	f := newFakeSeedDaemon()
	_, err := prepareWallet(context.Background(), f, seedService(), "img", "n", seedMounts, &WalletRequest{Mode: WalletKeep, Stored: "not a phrase at all"}, "also not one", nil)
	if walletCode(err) != "wallet_missing" || f.writes != 0 {
		t.Fatalf("keep with no valid wallet: %v (writes %d)", err, f.writes)
	}
	assertNoHelperLeft(t, f)
}

// A wallet helper a deploy that died midway left under the name is ours: it is
// removed and the step goes on.
func TestALeftoverWalletHelperIsRemovedFirst(t *testing.T) {
	f := newFakeSeedDaemon()
	f.containers["n-wallet"] = client.ContainerCreateOptions{Name: "n-wallet", Config: &container.Config{Labels: map[string]string{LabelWalletHelper: "file"}}}
	result, err := prepareWallet(context.Background(), f, seedService(), "img", "n", seedMounts, &WalletRequest{Mode: WalletImport, Phrase: testPhraseA}, "", nil)
	if err != nil || !result.Created {
		t.Fatalf("prepareWallet with a leftover helper: %+v, %v", result, err)
	}
	if len(f.removed) < 2 || f.removed[0] != "n-wallet" {
		t.Fatalf("the leftover helper was not removed first: %v", f.removed)
	}
}

// An undo that cannot run says the new wallet stays, and still answers the
// deploy's own error.
func TestAnUndoThatFailsSaysTheWalletStays(t *testing.T) {
	cause := errors.New("port is taken")
	for _, tc := range []struct {
		name  string
		setup func(f *fakeSeedDaemon)
	}{
		{"its helper cannot be created", func(f *fakeSeedDaemon) { f.fail = map[string]error{"create n-wallet": errors.New("no space")} }},
		{"Docker refuses its write", func(f *fakeSeedDaemon) { f.fail = map[string]error{"copyto": errors.New("read-only")} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSeedDaemon()
			f.output = generatorOutput
			result, err := prepareWallet(context.Background(), f, seedService(), "img", "n", seedMounts, &WalletRequest{Mode: WalletGenerate}, "", nil)
			if err != nil {
				t.Fatalf("prepareWallet: %v", err)
			}
			tc.setup(f)
			got := result.Undo(cause)
			if !errors.Is(got, cause) || !strings.Contains(got.Error(), "The new wallet stays in /data/hostd.yml") {
				t.Fatalf("Undo = %v, want the cause and that the wallet stays", got)
			}
			assertNoPhraseIn(t, got)
			if !strings.Contains(string(f.files["sia-hostd-data/hostd.yml"]), testPhraseB) {
				t.Fatal("the test expected the failed undo to leave the wallet in place")
			}
		})
	}
}

// The reveal reads only a deployed service's own file, and says plainly why it
// cannot.
func TestTheRevealRefusesWhatItCannotRead(t *testing.T) {
	own := client.ContainerCreateOptions{
		Name:       "cashpilot-sia-hostd",
		Config:     &container.Config{Labels: map[string]string{LabelManaged: "true", LabelRuntime: desktopRuntime, LabelService: "sia-hostd"}},
		HostConfig: &container.HostConfig{Mounts: seedMounts},
	}
	cases := []struct {
		name, code, says string
		setup            func(f *fakeSeedDaemon)
	}{
		{"not deployed", "wallet_missing", "is not deployed", func(f *fakeSeedDaemon) { delete(f.containers, own.Name) }},
		{"the container cannot be checked", "", "could not check cashpilot-sia-hostd", func(f *fakeSeedDaemon) {
			f.fail = map[string]error{"inspect cashpilot-sia-hostd": errors.New("daemon hiccup")}
		}},
		{"the file cannot be read", "", "could not read Sia (hostd)'s wallet file", func(f *fakeSeedDaemon) {
			f.fail = map[string]error{"copyfrom 1": errors.New("i/o error")}
		}},
		{"there is no file yet", "wallet_missing", "has no wallet file yet", func(f *fakeSeedDaemon) { delete(f.files, "sia-hostd-data/hostd.yml") }},
		{"the file is not a mapping", "wallet_file_invalid", "not a YAML mapping", func(f *fakeSeedDaemon) {
			f.files["sia-hostd-data/hostd.yml"] = []byte("- a\n- list\n")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSeedDaemon()
			f.containers[own.Name] = own
			f.files["sia-hostd-data/hostd.yml"] = []byte("recoveryPhrase: " + testPhraseA + "\n")
			tc.setup(f)
			got, err := readWalletSeed(context.Background(), f, "sia-hostd", seedService())
			if got != "" || err == nil || walletCode(err) != tc.code || !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("readWalletSeed = %q, %v; want code %q saying %q", got, err, tc.code, tc.says)
			}
			assertNoPhraseIn(t, err)
		})
	}
	if _, err := readWalletSeed(context.Background(), newFakeSeedDaemon(), "plain", catalog.Service{Name: "Plain"}); walletCode(err) != "wallet_none" {
		t.Fatalf("a service with no seed file: %v", err)
	}
}

// The file reader takes only a regular file of a wallet file's size.
func TestTheWalletFileReaderTakesOnlyARegularFileOfWalletSize(t *testing.T) {
	tarOf := func(entries ...tar.Header) []byte {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		for _, h := range entries {
			h := h
			_ = tw.WriteHeader(&h)
			if h.Typeflag == tar.TypeReg {
				_, _ = tw.Write(bytes.Repeat([]byte("a"), int(h.Size)))
			}
		}
		_ = tw.Close()
		return buf.Bytes()
	}
	f := newFakeSeedDaemon()
	f.rawTar = map[string][]byte{
		"/dir-then-file": tarOf(tar.Header{Name: "d", Typeflag: tar.TypeDir, Mode: 0o755}, tar.Header{Name: "f", Typeflag: tar.TypeReg, Mode: 0o600, Size: 3}),
		"/empty":         tarOf(),
		"/huge":          tarOf(tar.Header{Name: "f", Typeflag: tar.TypeReg, Mode: 0o600, Size: maxSeedFileBytes + 1}),
		"/garbage":       []byte("this is not a tar archive at all, and it is long enough to have a header block" + strings.Repeat("!", 600)),
	}
	if got, err := readContainerFile(context.Background(), f, "id", "/dir-then-file"); err != nil || string(got) != "aaa" {
		t.Fatalf("a directory before the file: %q, %v", got, err)
	}
	if got, err := readContainerFile(context.Background(), f, "id", "/empty"); err != nil || got != nil {
		t.Fatalf("an empty archive: %q, %v", got, err)
	}
	if _, err := readContainerFile(context.Background(), f, "id", "/huge"); err == nil || !strings.Contains(err.Error(), "larger than a wallet file") {
		t.Fatalf("an oversize file was read: %v", err)
	}
	if _, err := readContainerFile(context.Background(), f, "id", "/garbage"); err == nil {
		t.Fatal("a broken archive was read as a file")
	}
}

// The env helpers: a seed-file variable never survives StripSeedVars, the rest
// do, and the replaced container's env yields its value.
func TestTheSeedVariableIsStrippedAndReadByKey(t *testing.T) {
	env := map[string]string{"HOSTD_WALLET_SEED": testPhraseA, "HOSTD_API_PASSWORD": "pw"}
	got := StripSeedVars(seedService(), env)
	if _, ok := got["HOSTD_WALLET_SEED"]; ok || got["HOSTD_API_PASSWORD"] != "pw" || env["HOSTD_WALLET_SEED"] == "" {
		t.Fatalf("StripSeedVars = %v (input now %v)", got, env)
	}
	plain := map[string]string{"A": "1"}
	if got := StripSeedVars(catalog.Service{}, plain); len(got) != 1 || got["A"] != "1" {
		t.Fatalf("a service with no seed file lost settings: %v", got)
	}
	live := []string{"HOSTD_API_PASSWORD=pw", "HOSTD_WALLET_SEED=" + testPhraseA, "NOEQUALS"}
	if seedEnvValue(live, "HOSTD_WALLET_SEED") != testPhraseA || seedEnvValue(live, "MISSING") != "" || seedEnvValue(nil, "X") != "" {
		t.Fatal("seedEnvValue did not read the env by key")
	}
	if walletAddress(catalog.Service{Payout: catalog.Payout{Chain: "eth"}}, testPhraseA) != "" {
		t.Fatal("an address was derived for a chain siaseed does not know")
	}
	if walletAddress(seedService(), "not a phrase") != "" {
		t.Fatal("an address was derived from something that is not a phrase")
	}
}
