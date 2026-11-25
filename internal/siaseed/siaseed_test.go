package siaseed

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// The vectors are the public BIP-39 test phrases (all-zero, 0x7f.. and all-ones
// entropy), never a wallet anybody uses. The addresses were derived by
// go.sia.tech/coreutils v0.25.4 itself (wallet.SeedFromPhrase, wallet.KeyFromSeed(0),
// types.StandardUnlockHash), so this pins the re-implementation to hostd's own rule.
var vectors = []struct{ phrase, address string }{
	{"abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about",
		"a2a3773f76136bdb05a0ff79a0f4fcc2826436794f8db36db6408355c5ca32345002db43c2eb"},
	{"legal winner thank year wave sausage worth useful legal winner thank yellow",
		"5a7da9320b1d8cb37801eafa7065175df235be95096e401a187b5a98fda50428ef9e7566c29f"},
	{"zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo wrong",
		"d11e064b51f75444c2d0fc1083f9009cf87a8d21bb7a1c5d43cb87b6793544ea488908c3000d"},
}

func TestAddressIsTheOneCoreutilsDerives(t *testing.T) {
	for _, v := range vectors {
		got, err := Address(v.phrase)
		if err != nil {
			t.Fatalf("Address(%q): %v", v.phrase, err)
		}
		if got != v.address {
			t.Errorf("Address(%q) = %s, coreutils says %s", v.phrase, got, v.address)
		}
	}
}

func TestPhraseIsReadAsHostdReadsIt(t *testing.T) {
	messy := "  Legal WINNER thank year\twave sausage worth\nuseful legal winner thank yellow "
	if p := Problem(messy, Words); p != "" {
		t.Fatalf("a phrase with odd case and spacing was refused: %s", p)
	}
	got, _ := Address(messy)
	if got != vectors[1].address {
		t.Fatalf("odd case and spacing changed the wallet: %s", got)
	}
	if Normalize(messy) != vectors[1].phrase {
		t.Fatalf("Normalize = %q", Normalize(messy))
	}
}

func TestProblemNamesWhatIsWrongWithoutRepeatingAWord(t *testing.T) {
	cases := map[string]string{
		"abandon abandon abandon":      "has 3",
		strings.Repeat("abandon ", 12): "does not check out",
		"abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon qwertyzz about": "word 11 is not",
		"zzzz abandon abandon abandon abandon abandon abandon abandon abandon abandon qwertyzz about":    "words 1, 11 are not",
	}
	for phrase, want := range cases {
		got := Problem(phrase, Words)
		if !strings.Contains(got, want) {
			t.Errorf("Problem(%q) = %q, want it to say %q", phrase, got, want)
		}
		for _, word := range strings.Fields(phrase) {
			if len(word) > 3 && strings.Contains(got, word) {
				t.Errorf("Problem(%q) repeats the word %q: %q", phrase, word, got)
			}
		}
	}
	if _, err := Address(strings.Repeat("abandon ", 12)); err == nil {
		t.Fatal("Address accepted a phrase whose checksum is wrong")
	}
	if Problem(vectors[0].phrase, 24) == "" {
		t.Fatal("a 24-word length was accepted, but only 12 is checked")
	}
}

// The embedded list is the BIP-39 English list, byte for byte.
func TestWordlistIsBIP39English(t *testing.T) {
	sum := sha256.Sum256([]byte(englishList))
	if got := hex.EncodeToString(sum[:]); got != "2f5eed53a4727b4bf8880d8f3f199efc90e58503646d9ff8eff3a2ed3b24dbda" {
		t.Fatalf("english.txt sha256 = %s", got)
	}
	if n := len(wordlist()); n != 2048 {
		t.Fatalf("wordlist has %d words", n)
	}
}
