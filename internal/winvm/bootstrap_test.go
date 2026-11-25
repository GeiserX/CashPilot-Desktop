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
		"setup.ps1":            {setupPS1, "fa8f287edc26ec6c7bf04f95f35fc3deb8cbce5e0c49f99b691d53b2df2e4dfa"},
		"remove.ps1":           {removePS1, "3abda4fe37ba2a6e48c3e7da6ad5efc2398a3c0b627e06019215f3be54a0b33d"},
	} {
		s := sha256.Sum256(c.b)
		if got := hex.EncodeToString(s[:]); got != c.want {
			t.Errorf("%s changed: sha256 %s, pinned %s", name, got, c.want)
		}
	}
	if b := SetupBootstrap(); b.SHA256 != "FA8F287EDC26EC6C7BF04F95F35FC3DEB8CBCE5E0C49F99B691D53B2DF2E4DFA" || b.Name != "setup.ps1" {
		t.Errorf("SetupBootstrap = %s %s", b.Name, b.SHA256)
	}
}
