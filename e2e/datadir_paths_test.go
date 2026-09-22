package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTheAppWorksFromAwkwardDataDirectories runs the whole engine — config, the
// database, a deploy, saved credentials — out of data directories shaped the way
// real installs are.
//
// Every one of these is somebody's actual home directory. "C:\Users\José
// Pérez\AppData\Roaming\CashPilot Desktop" has both a space and an accent in it
// before the app has done anything; a synced folder nests deep enough to pass the
// old 260-character Windows limit; and the directory can arrive spelled with
// forward slashes because that is what an environment variable or a shortcut
// carried. A path handled by string concatenation somewhere breaks on exactly
// these and on nothing else, which is why they are worth a test rather than a
// look at the code.
func TestTheAppWorksFromAwkwardDataDirectories(t *testing.T) {
	cases := []struct {
		name string
		dir  func(base string) string
	}{
		{
			name: "a plain path",
			dir:  func(base string) string { return filepath.Join(base, "cashpilot") },
		},
		{
			name: "spaces and brackets, the way an installer writes them",
			dir:  func(base string) string { return filepath.Join(base, "CashPilot Desktop (user data)") },
		},
		{
			name: "accents and non-ascii, the way a user's name arrives",
			dir:  func(base string) string { return filepath.Join(base, "José Pérez", "Datos de Ñandú") },
		},
		{
			name: "deeper than the old windows path limit",
			dir: func(base string) string {
				parts := []string{base}
				for i := 0; i < 12; i++ {
					parts = append(parts, strings.Repeat("nested-folder", 2)+string(rune('a'+i)))
				}
				return filepath.Join(parts...)
			},
		},
		{
			name: "spelled with forward slashes",
			// Windows accepts these, and this is how a path set by hand in an
			// environment variable or copied from a config file usually looks.
			dir: func(base string) string { return base + "/forward/slash/data" },
		},
		{
			name: "with a trailing separator",
			dir:  func(base string) string { return filepath.Join(base, "trailing") + string(os.PathSeparator) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.dir(t.TempDir())
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatalf("creating %q: %v", dir, err)
			}

			e := newEnvIn(t, dir)
			ctx := e.ctx()

			// The config manager put its data directory inside the one it was given.
			// Both sides are cleaned first: the directory may have been handed over
			// with forward slashes or a trailing separator, and on Windows the app
			// hands back the native spelling, so a raw string compare would fail on
			// the spelling rather than on the behaviour.
			if !strings.HasPrefix(filepath.Clean(e.DataDir), filepath.Clean(dir)) {
				t.Errorf("the data directory %q is not inside %q", e.DataDir, dir)
			}

			// A credential round trip proves the database opened and the master key
			// was reachable from this path.
			if err := e.Store.SaveCredentials("honeygain", honeygainCreds); err != nil {
				t.Fatalf("SaveCredentials: %v", err)
			}
			creds, err := e.Store.GetCredentials("honeygain")
			if err != nil || creds["HONEYGAIN_EMAIL"] != honeygainCreds["HONEYGAIN_EMAIL"] {
				t.Fatalf("credentials did not round trip: %v (err=%v)", creds, err)
			}

			// And a deploy proves the rest of the engine works from here too.
			if _, err := e.Manager.Deploy(ctx, "honeygain", honeygainCreds); err != nil {
				t.Fatalf("Deploy from this data directory: %v", err)
			}
			if row, ok := e.deployment("honeygain"); !ok || row.Status != "running" {
				t.Errorf("the deployment row is wrong: %+v (ok=%v)", row, ok)
			}

			// The database really is on disk under the directory that was asked for.
			if _, err := os.Stat(filepath.Join(e.DataDir, dbFile)); err != nil {
				t.Errorf("no database at %s: %v", e.DataDir, err)
			}
		})
	}
}
