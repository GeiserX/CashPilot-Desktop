package catalog

import (
	"strings"
	"testing"
)

func seedFileService(edit func(item *EnvVar, spec *SeedFile)) Service {
	spec := SeedFile{
		Path: "/data/hostd.yml", Key: "recoveryPhrase", Words: 12, Generate: []string{"seed"},
		PhrasePattern:  `(?m)^Recovery Phrase: ([a-z ]+)$`,
		AddressPattern: `(?m)^Address ([0-9a-f]{76})$`,
	}
	item := EnvVar{Key: "HOSTD_WALLET_SEED", Secret: true}
	if edit != nil {
		edit(&item, &spec)
	}
	item.SeedFile = &spec
	return Service{Name: "Sia (hostd)", Docker: DockerConfig{Volumes: []string{"sia-hostd-data:/data/", "${DIR}:/storage"}, Env: []EnvVar{{Key: "OTHER"}, item}}}
}

// Each rule of the web's validate_spec refuses on its own, by name, so a deploy
// never falls back to passing the phrase as a variable.
func TestEachSeedFileRuleRefusesByName(t *testing.T) {
	if problem := seedFileService(nil).SeedFileProblem(); problem != "" {
		t.Fatalf("a valid block was refused: %s", problem)
	}
	for _, tc := range []struct {
		name, says string
		edit       func(item *EnvVar, spec *SeedFile)
	}{
		{"not secret", "only for a secret variable", func(item *EnvVar, _ *SeedFile) { item.Secret = false }},
		{"relative path", "absolute file path", func(_ *EnvVar, s *SeedFile) { s.Path = "data/hostd.yml" }},
		{"a folder", "absolute file path", func(_ *EnvVar, s *SeedFile) { s.Path = "/data/" }},
		{"an unclean path", "absolute file path", func(_ *EnvVar, s *SeedFile) { s.Path = "/data/../data/hostd.yml" }},
		{"outside the volumes", "directly in a folder docker.volumes mounts", func(_ *EnvVar, s *SeedFile) { s.Path = "/etc/hostd.yml" }},
		{"nested in a volume", "directly in a folder docker.volumes mounts", func(_ *EnvVar, s *SeedFile) { s.Path = "/data/sub/hostd.yml" }},
		{"no key", "seed_file.key", func(_ *EnvVar, s *SeedFile) { s.Key = "  " }},
		{"24 words", "seed_file.words must be 12", func(_ *EnvVar, s *SeedFile) { s.Words = 24 }},
		{"no generator", "seed_file.generate", func(_ *EnvVar, s *SeedFile) { s.Generate = nil }},
		{"a broken phrase pattern", "phrase_pattern must be a valid regular expression", func(_ *EnvVar, s *SeedFile) { s.PhrasePattern = "(" }},
		{"an empty address pattern", "address_pattern must be a valid regular expression", func(_ *EnvVar, s *SeedFile) { s.AddressPattern = "" }},
		{"two groups", "phrase_pattern must have exactly one group", func(_ *EnvVar, s *SeedFile) { s.PhrasePattern = "(a)(b)" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problem := seedFileService(tc.edit).SeedFileProblem()
			if !strings.HasPrefix(problem, "Sia (hostd): ") || !strings.Contains(problem, tc.says) {
				t.Fatalf("problem = %q, want it to say %q", problem, tc.says)
			}
		})
	}
	many := seedFileService(func(_ *EnvVar, s *SeedFile) { s.Words, s.Key = 24, "" })
	if a, b := many.SeedFileProblem(), many.SeedFileProblem(); a != b || strings.Index(a, "seed_file.key") > strings.Index(a, "seed_file.words") {
		t.Fatalf("several problems are not listed in a fixed order: %q / %q", a, b)
	}
}

// A service with no seed_file has no seed variable and nothing to refuse.
func TestAServiceWithNoSeedFileHasNoSeedVar(t *testing.T) {
	plain := Service{Name: "Plain", Docker: DockerConfig{Env: []EnvVar{{Key: "A", Secret: true}}}}
	if _, ok := plain.SeedVar(); ok {
		t.Fatal("a plain secret was taken for a seed variable")
	}
	if problem := plain.SeedFileProblem(); problem != "" {
		t.Fatalf("a service with no seed_file has a problem: %s", problem)
	}
	item, ok := seedFileService(nil).SeedVar()
	if !ok || item.Key != "HOSTD_WALLET_SEED" {
		t.Fatalf("SeedVar = %+v, %v", item, ok)
	}
}
