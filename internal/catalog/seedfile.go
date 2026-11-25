package catalog

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// SeedFile says that a secret env var holds a wallet seed phrase, and where the
// service reads that phrase from instead of the variable (Sia's hostd reads
// recoveryPhrase from /data/hostd.yml before HOSTD_WALLET_SEED). The phrase IS the
// wallet: whoever reads it can spend the node's funds.
//
// A client that reads this block never stores the value and never passes it as a
// variable. It writes the phrase into the file, inside the volume the service
// already keeps its wallet in, and that file is the only copy (internal/runtime
// seedfile.go). The env entry stays in the catalog for clients that predate the
// block. The web catalog's services/_schema.yml documents the same block.
type SeedFile struct {
	// Path is the file inside the container, directly in a folder a volume mounts.
	Path string `json:"path" yaml:"path"`
	// Key is the YAML key in that file that holds the phrase.
	Key string `json:"key" yaml:"key"`
	// Words is how many words the phrase has (12).
	Words int `json:"words" yaml:"words"`
	// Generate is the command the service's own image runs, offline, to make a new
	// wallet. It prints the phrase and the address.
	Generate []string `json:"generate" yaml:"generate"`
	// PhrasePattern and AddressPattern each pull one group out of what Generate prints.
	PhrasePattern  string `json:"phrasePattern" yaml:"phrase_pattern"`
	AddressPattern string `json:"addressPattern" yaml:"address_pattern"`
}

// SeedVar is the Docker env entry of s whose value lives in a file (seed_file), and
// whether there is one.
func (s Service) SeedVar() (EnvVar, bool) {
	for _, item := range s.Docker.Env {
		if item.SeedFile != nil && item.Key != "" {
			return item, true
		}
	}
	return EnvVar{}, false
}

// SeedFileProblem says what is wrong with s's seed_file block, or "" when nothing
// is (or s has none). The same rules as the web catalog (app/wallet_seed.py
// validate_spec). A deploy refuses a service whose block is wrong rather than fall
// back to passing the phrase as a variable.
func (s Service) SeedFileProblem() string {
	item, ok := s.SeedVar()
	if !ok {
		return ""
	}
	spec := item.SeedFile
	var problems []string
	if !item.Secret {
		problems = append(problems, "seed_file is only for a secret variable")
	}
	targets := map[string]bool{}
	for _, mapping := range s.Docker.Volumes {
		if parts := strings.Split(mapping, ":"); len(parts) >= 2 {
			targets[strings.TrimRight(parts[1], "/")] = true
		}
	}
	switch {
	case !strings.HasPrefix(spec.Path, "/") || strings.HasSuffix(spec.Path, "/") || path.Clean(spec.Path) != spec.Path:
		problems = append(problems, "seed_file.path must be an absolute file path")
	case !targets[path.Dir(spec.Path)]:
		problems = append(problems, "seed_file.path must sit directly in a folder docker.volumes mounts")
	}
	if strings.TrimSpace(spec.Key) == "" {
		problems = append(problems, "seed_file.key must name the YAML key that holds the phrase")
	}
	if spec.Words != 12 {
		problems = append(problems, "seed_file.words must be 12 (the only length checked here)")
	}
	if len(spec.Generate) == 0 {
		problems = append(problems, "seed_file.generate must be a command")
	}
	for name, pattern := range map[string]string{"phrase_pattern": spec.PhrasePattern, "address_pattern": spec.AddressPattern} {
		re, err := regexp.Compile(pattern)
		if err != nil || pattern == "" {
			problems = append(problems, fmt.Sprintf("seed_file.%s must be a valid regular expression", name))
		} else if re.NumSubexp() != 1 {
			problems = append(problems, fmt.Sprintf("seed_file.%s must have exactly one group", name))
		}
	}
	if len(problems) == 0 {
		return ""
	}
	// Map iteration order above is random; the message is not.
	sort.Strings(problems)
	return s.Name + ": " + strings.Join(problems, "; ")
}
