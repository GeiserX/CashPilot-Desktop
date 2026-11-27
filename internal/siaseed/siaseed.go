// Package siaseed checks a Sia wallet seed phrase and derives the wallet's address,
// without a copy of the phrase ever leaving the call.
//
// A Sia phrase is 12 words of the BIP-39 English list: 128 bits of entropy and a
// 4-bit SHA-256 checksum, the same rule hostd applies at start
// (go.sia.tech/coreutils/wallet.SeedFromPhrase). The address is the one hostd's
// wallet uses, key index 0 (wallet.KeyFromSeed, types.StandardUnlockHash). Both are
// re-implemented here on blake2b and ed25519 rather than importing coreutils, whose
// module brings a database, a mux and a logger with it; siaseed_test.go pins the
// result against addresses coreutils itself derived.
//
// Nothing here logs a phrase or puts one in an error.
package siaseed

import (
	"crypto/ed25519"
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/blake2b"

	"github.com/GeiserX/CashPilot-Desktop/internal/i18n"
)

// Words is how many words a Sia phrase has.
const Words = 12

//go:embed english.txt
var englishList string

var (
	wordsOnce sync.Once
	wordIndex map[string]int
)

func wordlist() map[string]int {
	wordsOnce.Do(func() {
		list := strings.Fields(englishList)
		wordIndex = make(map[string]int, len(list))
		for i, word := range list {
			wordIndex[word] = i
		}
	})
	return wordIndex
}

// Normalize is the phrase as hostd reads it: lowercase words, one space apart.
func Normalize(phrase string) string {
	return strings.Join(strings.Fields(strings.ToLower(phrase)), " ")
}

// Problem says, in words a user can act on, why phrase is not a valid phrase of
// `words` words, or "" when it is. It never repeats a word of the phrase.
func Problem(phrase string, words int) string {
	_, err := entropy(phrase, words)
	if err != nil {
		return err.Error()
	}
	return ""
}

// entropy is the 16 bytes the phrase encodes, after checking its words and checksum.
func entropy(phrase string, words int) ([16]byte, error) {
	var out [16]byte
	if words != Words {
		return out, errors.New(i18n.T("go.siaseed.onlyWords", "words", Words))
	}
	found := strings.Fields(Normalize(phrase))
	if len(found) != words {
		return out, errors.New(i18n.T("go.siaseed.wordCount", "want", words, "got", len(found)))
	}
	list := wordlist()
	var unknown []string
	for n, word := range found {
		if _, ok := list[word]; !ok {
			unknown = append(unknown, strconv.Itoa(n+1))
		}
	}
	if len(unknown) > 0 {
		return out, errors.New(i18n.T("go.siaseed.unknownWords", "count", len(unknown), "words", strings.Join(unknown, i18n.T("go.siaseed.wordSeparator"))))
	}
	// 12 words x 11 bits = 132 bits: 128 of entropy, then 4 of checksum.
	var bits [17]byte
	pos := 0
	for _, word := range found {
		index := list[word]
		for b := 10; b >= 0; b-- {
			if index>>b&1 == 1 {
				bits[pos/8] |= 0x80 >> (pos % 8)
			}
			pos++
		}
	}
	copy(out[:], bits[:16])
	sum := sha256.Sum256(out[:])
	if sum[0]>>4 != bits[16]>>4 {
		return [16]byte{}, errors.New(i18n.T("go.siaseed.checksum"))
	}
	return out, nil
}

// Address is the Sia address of the wallet phrase opens (key index 0): 76 hex
// characters, the 32-byte unlock hash and its 6-byte checksum.
func Address(phrase string) (string, error) {
	ent, err := entropy(phrase, Words)
	if err != nil {
		return "", err
	}
	seed := blake2b.Sum256(ent[:])
	// KeyFromSeed: blake2b(seed || uint64le(index)) is the ed25519 seed.
	var buf [40]byte
	copy(buf[:32], seed[:])
	binary.LittleEndian.PutUint64(buf[32:], 0)
	keySeed := blake2b.Sum256(buf[:])
	pub := ed25519.NewKeyFromSeed(keySeed[:]).Public().(ed25519.PublicKey)
	clear(seed[:])
	clear(buf[:])
	clear(keySeed[:])
	clear(ent[:])

	// StandardUnlockHash: the Merkle root over the unlock conditions' three leaves,
	// timelock 0, the public key and 1 required signature.
	leaf := func(data []byte) [32]byte {
		return blake2b.Sum256(append([]byte{0x00}, data...))
	}
	node := func(a, b [32]byte) [32]byte {
		joined := make([]byte, 0, 65)
		joined = append(joined, 0x01)
		joined = append(joined, a[:]...)
		return blake2b.Sum256(append(joined, b[:]...))
	}
	u64 := func(v uint64) []byte {
		var b [8]byte
		binary.LittleEndian.PutUint64(b[:], v)
		return b[:]
	}
	var specifier [16]byte
	copy(specifier[:], "ed25519")
	key := make([]byte, 0, 56)
	key = append(key, specifier[:]...)
	key = append(key, u64(uint64(len(pub)))...)
	key = append(key, pub...)
	root := node(node(leaf(u64(0)), leaf(key)), leaf(u64(1)))
	checksum := blake2b.Sum256(root[:])
	return hex.EncodeToString(root[:]) + hex.EncodeToString(checksum[:6]), nil
}
