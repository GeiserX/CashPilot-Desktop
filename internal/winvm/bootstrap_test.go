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
		"setup.ps1":            {setupPS1, "7e89ab76c5a093e2769b6c6eac7075fbd3e74d1b061cf37b8105c9bb8f21aa35"},
		"remove.ps1":           {removePS1, "185141da5531bfb8429e85b608e838b2689f8031eb27c3120005f7a3baa948a4"},
	} {
		s := sha256.Sum256(c.b)
		if got := hex.EncodeToString(s[:]); got != c.want {
			t.Errorf("%s changed: sha256 %s, pinned %s", name, got, c.want)
		}
	}
	if b := SetupBootstrap(); b.SHA256 != "7E89AB76C5A093E2769B6C6EAC7075FBD3E74D1B061CF37B8105C9BB8F21AA35" || b.Name != "setup.ps1" {
		t.Errorf("SetupBootstrap = %s %s", b.Name, b.SHA256)
	}
}
