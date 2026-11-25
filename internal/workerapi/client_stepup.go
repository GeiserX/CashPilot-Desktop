package workerapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// The approval a worker checks before it runs a service that keeps a wallet or node
// identity other than as its catalog says (web app/step_up.py). The web UI signs it
// after the owner re-entered their password; this Desktop signs it after the owner
// confirmed the same question. Only a holder of the worker's own key can sign one,
// and it is bound to the exact spec sent.

// ActionCustomImage is step_up.ACTION_CUSTOM_IMAGE.
const ActionCustomImage = "deploy-custom-image"

// approvalTTL is step_up.APPROVAL_TTL_SECONDS.
const approvalTTL = 300 * time.Second

var stepUpLabel = []byte("cashpilot-step-up-v1")

// Sign is step_up.sign: base64url(payload) + "." + hex(HMAC-SHA256(derived key, body)),
// where the payload holds the action, the target, an expiry and a random nonce.
func Sign(workerKey, action, target string, now time.Time) (string, error) {
	if workerKey == "" {
		return "", errors.New("this worker has not finished enrolling, so it cannot check a confirmation; wait for its next check-in and try again")
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return signWith(workerKey, action, target, now, hex.EncodeToString(nonce)), nil
}

func signWith(workerKey, action, target string, now time.Time, nonce string) string {
	// json.dumps(payload, separators=(",", ":"), sort_keys=True): keys a, exp, n, t.
	payload := `{"a":` + pyString(action) + `,"exp":` + strconv.FormatInt(now.Unix()+int64(approvalTTL/time.Second), 10) +
		`,"n":` + pyString(nonce) + `,"t":` + pyString(target) + `}`
	body := base64.RawURLEncoding.EncodeToString([]byte(payload))
	derive := hmac.New(sha256.New, []byte(workerKey))
	derive.Write(stepUpLabel)
	mac := hmac.New(sha256.New, derive.Sum(nil))
	mac.Write([]byte(body))
	return body + "." + hex.EncodeToString(mac.Sum(nil))
}

// DeployTarget is step_up.deploy_target: "slug|image|sha256" of the command, env and
// mounts, serialised as Python's json.dumps(sort_keys=True, separators=(",", ":"))
// does, so the worker computes the same digest from the spec it receives.
func DeployTarget(slug, image string, command *string, env map[string]string, volumes map[string]Mount) string {
	var b strings.Builder
	b.WriteString(`{"command":`)
	if command == nil || *command == "" {
		b.WriteString("null")
	} else {
		b.WriteString(pyString(*command))
	}
	b.WriteString(`,"env":`)
	writeStringMap(&b, env)
	b.WriteString(`,"volumes":{`)
	sources := make([]string, 0, len(volumes))
	for source := range volumes {
		sources = append(sources, source)
	}
	sort.Strings(sources)
	for i, source := range sources {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(pyString(source))
		b.WriteByte(':')
		writeStringMap(&b, map[string]string{"bind": volumes[source].Bind, "mode": volumes[source].Mode})
	}
	b.WriteString("}}")
	sum := sha256.Sum256([]byte(b.String()))
	return fmt.Sprintf("%s|%s|%s", slug, image, hex.EncodeToString(sum[:]))
}

func writeStringMap(b *strings.Builder, m map[string]string) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Python sorts str keys by code point; Go's string order is by UTF-8 bytes,
	// which is the same order.
	sort.Strings(keys)
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(pyString(k))
		b.WriteByte(':')
		b.WriteString(pyString(m[k]))
	}
	b.WriteByte('}')
}

// pyString is a JSON string as Python's json.dumps writes it with ensure_ascii (its
// default): \" \\ \b \f \n \r \t, and every other character outside space..~ as
// \uXXXX, with a surrogate pair above U+FFFF. Go's encoding/json differs (it keeps
// non-ASCII and escapes <, > and &), so it cannot be used here.
func pyString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r >= ' ' && r <= '~' {
				b.WriteRune(r)
				continue
			}
			if r > 0xFFFF {
				hi, lo := utf16.EncodeRune(r)
				fmt.Fprintf(&b, `\u%04x\u%04x`, hi, lo)
				continue
			}
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
