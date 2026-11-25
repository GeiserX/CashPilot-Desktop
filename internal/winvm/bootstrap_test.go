package winvm

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// TestEmbeddedScriptPins pins the SHA-256 of every script setup embeds, so a
// change to what runs as admin or goes into the guest's answer file is always
// a deliberate one: change the script, then the pin, in the same commit.
func TestEmbeddedScriptPins(t *testing.T) {
	for name, c := range map[string]struct {
		b    []byte
		want string
	}{
		"autounattend.tpl.xml": {[]byte(answerTemplate), "7313f82b46a37e34169b0cab8fec2f39f5e564c42de024e9822024e235de5475"},
		"setup.ps1":            {setupPS1, "67fcb789cb8be453673a26321bebc3b844b0bf978e875fe657c157fbba516775"},
		"remove.ps1":           {removePS1, "3abda4fe37ba2a6e48c3e7da6ad5efc2398a3c0b627e06019215f3be54a0b33d"},
	} {
		s := sha256.Sum256(c.b)
		if got := hex.EncodeToString(s[:]); got != c.want {
			t.Errorf("%s changed: sha256 %s, pinned %s", name, got, c.want)
		}
	}
	if b := SetupBootstrap(); b.SHA256 != "67FCB789CB8BE453673A26321BEBC3B844B0BF978E875FE657C157FBBA516775" || b.Name != "setup.ps1" {
		t.Errorf("SetupBootstrap = %s %s", b.Name, b.SHA256)
	}
}
