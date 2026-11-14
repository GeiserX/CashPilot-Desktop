package catalog

import (
	"bytes"
	"testing"

	"gopkg.in/yaml.v3"
)

// The web catalog marks device names and ids with `identifier: true` (issue #740 in
// the web repository). The vendored catalog is strict-decoded on every sync, so the
// field must exist here before the sync that brings it, or that sync fails.
func TestAnIdentifierEnvVarDecodesStrictly(t *testing.T) {
	raw := []byte("key: HONEYGAIN_DEVICE_NAME\nidentifier: true\nsecret: false\n")
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var env EnvVar
	if err := decoder.Decode(&env); err != nil {
		t.Fatalf("an env var with identifier: true must decode strictly: %v", err)
	}
	if !env.Identifier {
		t.Fatal("identifier: true was dropped at decode")
	}
}
