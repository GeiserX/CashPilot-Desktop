// Package statebackup exports a service's irreplaceable state (a node identity, a
// keystore, a wallet) as an encrypted file, and decides whether the user still has
// a backup of it.
//
// The bundle format is the web app's (GeiserX/CashPilot app/state_backup.py), byte
// for byte, so one decrypt tool opens a backup from either app:
//
//	4-byte big-endian header length | header JSON | ChaCha20-Poly1305 ciphertext
//
// The header carries magic "CASHPILOT-BACKUP", version 1, salt, nonce, mode,
// metadata and the payload's sha256. It is not encrypted but it is the AEAD's
// associated data, so changing which service a bundle says it belongs to breaks
// it. The key comes from scrypt (N=2^15, r=8, p=1) over a passphrase, or from
// HKDF-SHA256 (info "cashpilot-backup-v1") over an X25519 exchange with the
// recipient's public key.
//
// CashPilot keeps nothing that could open what it exported: the passphrase is used
// once and dropped, and in recipient mode no secret enters the app at all. There is
// no upload and no schedule; a timer would need a stored key.
package statebackup

import (
	"bytes"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/scrypt"
)

const (
	Magic   = "CASHPILOT-BACKUP"
	Version = 1

	ModePassphrase = "passphrase"
	ModeRecipient  = "recipient"

	// MinPassphraseLength: shorter than this can be brute-forced offline, and
	// nothing rate-limits a guess at a stolen file.
	MinPassphraseLength = 12

	// MaxStateBytes is the most state an export reads. Identities and wallets are
	// kilobytes; anything near this is a storage folder (Storj, Sia), which belongs
	// in a disk-level backup, not in one file held in memory with the node paused.
	MaxStateBytes = 256 << 20

	scryptN    = 1 << 15
	scryptR    = 8
	scryptP    = 1
	saltBytes  = 16
	nonceBytes = 12
	keyBytes   = 32
	hkdfInfo   = "cashpilot-backup-v1"
)

// ErrBackup marks every error this package returns about a bundle or its inputs.
// The messages never contain a passphrase or key.
var ErrBackup = errors.New("backup")

func fail(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrBackup, fmt.Sprintf(format, args...))
}

// GenerateKeypair returns a fresh X25519 pair as (private hex, public hex). The
// private half is for the user to keep; nothing in the app stores it.
func GenerateKeypair() (string, string, error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return hex.EncodeToString(priv.Bytes()), hex.EncodeToString(priv.PublicKey().Bytes()), nil
}

func deriveFromPassphrase(passphrase string, salt []byte) ([]byte, error) {
	return scrypt.Key([]byte(passphrase), salt, scryptN, scryptR, scryptP, keyBytes)
}

func deriveShared(shared, salt []byte) ([]byte, error) {
	return hkdf.Key(sha256.New, shared, salt, hkdfInfo, keyBytes)
}

// Seal encrypts payload into a bundle. Exactly one of passphrase or recipientHex
// (an X25519 public key, 64 hex characters) must be given.
func Seal(payload []byte, passphrase, recipientHex string, metadata map[string]any) ([]byte, error) {
	if (passphrase == "") == (recipientHex == "") {
		return nil, fail("give exactly one of a passphrase or a recipient public key")
	}
	salt := make([]byte, saltBytes)
	nonce := make([]byte, nonceBytes)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	sum := sha256.Sum256(payload)
	header := map[string]any{
		"magic":    Magic,
		"version":  Version,
		"salt":     hex.EncodeToString(salt),
		"nonce":    hex.EncodeToString(nonce),
		"metadata": metadata,
		"sha256":   hex.EncodeToString(sum[:]),
	}

	var key []byte
	if passphrase != "" {
		if len([]rune(passphrase)) < MinPassphraseLength {
			return nil, fail("the passphrase must be at least %d characters; it is the only thing between a stolen file and a node identity", MinPassphraseLength)
		}
		header["mode"] = ModePassphrase
		k, err := deriveFromPassphrase(passphrase, salt)
		if err != nil {
			return nil, err
		}
		key = k
	} else {
		raw, err := hex.DecodeString(recipientHex)
		if err != nil || len(raw) != 32 {
			return nil, fail("the recipient public key must be 32 bytes of hex (an X25519 public key)")
		}
		recipient, err := ecdh.X25519().NewPublicKey(raw)
		if err != nil {
			return nil, fail("the recipient public key must be 32 bytes of hex (an X25519 public key)")
		}
		ephemeral, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		header["mode"] = ModeRecipient
		header["ephemeral_public_key"] = hex.EncodeToString(ephemeral.PublicKey().Bytes())
		shared, err := ephemeral.ECDH(recipient)
		if err != nil {
			// A low-order point gives an all-zero secret, which ECDH refuses.
			return nil, fail("the recipient public key is not a usable X25519 point")
		}
		if key, err = deriveShared(shared, salt); err != nil {
			return nil, err
		}
	}

	headerBytes, err := encodeHeader(header)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 4, 4+len(headerBytes)+len(payload)+aead.Overhead())
	binary.BigEndian.PutUint32(out, uint32(len(headerBytes)))
	out = append(out, headerBytes...)
	return aead.Seal(out, nonce, payload, headerBytes), nil
}

// encodeHeader writes the header the way the web does (sorted keys, no spaces).
// The bytes only have to be stable inside one bundle, since they are read back as
// they are, but matching the web keeps the two apps' files alike.
func encodeHeader(header map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(header); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// Header is the bundle's unencrypted, authenticated header. It never holds key
// material.
type Header struct {
	Magic              string         `json:"magic"`
	Version            int            `json:"version"`
	Mode               string         `json:"mode"`
	Salt               string         `json:"salt"`
	Nonce              string         `json:"nonce"`
	EphemeralPublicKey string         `json:"ephemeral_public_key"`
	SHA256             string         `json:"sha256"`
	Metadata           map[string]any `json:"metadata"`
}

func splitBundle(bundle []byte) (Header, []byte, []byte, error) {
	var h Header
	if len(bundle) < 4 {
		return h, nil, nil, fail("not a CashPilot backup file")
	}
	n := binary.BigEndian.Uint32(bundle[:4])
	if n == 0 || uint64(len(bundle)) < 4+uint64(n) {
		return h, nil, nil, fail("not a CashPilot backup file")
	}
	headerBytes := bundle[4 : 4+n]
	if err := json.Unmarshal(headerBytes, &h); err != nil || h.Magic != Magic {
		return h, nil, nil, fail("not a CashPilot backup file")
	}
	if h.Version != Version {
		return h, nil, nil, fail("unsupported backup version %d", h.Version)
	}
	return h, headerBytes, bundle[4+n:], nil
}

// ReadHeader returns the header without decrypting anything.
func ReadHeader(bundle []byte) (Header, error) {
	h, _, _, err := splitBundle(bundle)
	return h, err
}

// Open decrypts a bundle with the passphrase or the recipient's private key (hex),
// whichever its header asks for, and checks the payload against the recorded
// sha256. A wrong secret and an altered file fail the same way.
func Open(bundle []byte, passphrase, privateKeyHex string) ([]byte, error) {
	h, headerBytes, ciphertext, err := splitBundle(bundle)
	if err != nil {
		return nil, err
	}
	salt, err1 := hex.DecodeString(h.Salt)
	nonce, err2 := hex.DecodeString(h.Nonce)
	if err1 != nil || err2 != nil || len(nonce) != nonceBytes {
		return nil, fail("the backup's header is malformed")
	}

	var key []byte
	switch h.Mode {
	case ModePassphrase:
		if passphrase == "" {
			return nil, fail("this backup needs the passphrase it was made with")
		}
		if key, err = deriveFromPassphrase(passphrase, salt); err != nil {
			return nil, err
		}
	case ModeRecipient:
		if privateKeyHex == "" {
			return nil, fail("this backup needs the private key matching the public key it was made for")
		}
		rawPriv, errA := hex.DecodeString(privateKeyHex)
		rawEph, errB := hex.DecodeString(h.EphemeralPublicKey)
		if errA != nil || errB != nil {
			return nil, fail("the backup's header is malformed, or the private key is not valid X25519")
		}
		priv, errA := ecdh.X25519().NewPrivateKey(rawPriv)
		eph, errB := ecdh.X25519().NewPublicKey(rawEph)
		if errA != nil || errB != nil {
			return nil, fail("the backup's header is malformed, or the private key is not valid X25519")
		}
		shared, err := priv.ECDH(eph)
		if err != nil {
			return nil, fail("the backup's ephemeral key is not a usable X25519 point, so it cannot be opened")
		}
		if key, err = deriveShared(shared, salt); err != nil {
			return nil, err
		}
	default:
		return nil, fail("unknown backup mode %q", h.Mode)
	}

	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	payload, err := aead.Open(nil, nonce, ciphertext, headerBytes)
	if err != nil {
		return nil, fail("could not decrypt this backup: the passphrase or key is wrong, or the file was altered. There is no recovery path")
	}
	if h.SHA256 != "" {
		sum := sha256.Sum256(payload)
		if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(h.SHA256)) != 1 {
			return nil, fail("the backup decrypted but its contents do not match the recorded checksum")
		}
	}
	return payload, nil
}
