package runtime

import (
	"strconv"
	"strings"
)

// parseMemBytes converts a Docker-style memory size string to bytes. It accepts a plain
// byte count ("1073741824") or a number with a binary unit suffix — b, k/kb, m/mb, g/gb,
// t/tb (case-insensitive, IEC 1024-based, matching how the catalog's MemLimit/"768m" style
// values map to Docker's HostConfig.Memory). It returns (0, false) for an empty or
// unparseable string so callers can skip applying a limit rather than guess.
func parseMemBytes(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	lower := strings.ToLower(s)

	mult := int64(1)
	// Order matters: check the two-letter suffixes before the single-letter ones.
	switch {
	case strings.HasSuffix(lower, "kb"):
		mult, lower = 1<<10, strings.TrimSuffix(lower, "kb")
	case strings.HasSuffix(lower, "mb"):
		mult, lower = 1<<20, strings.TrimSuffix(lower, "mb")
	case strings.HasSuffix(lower, "gb"):
		mult, lower = 1<<30, strings.TrimSuffix(lower, "gb")
	case strings.HasSuffix(lower, "tb"):
		mult, lower = 1<<40, strings.TrimSuffix(lower, "tb")
	case strings.HasSuffix(lower, "b"):
		mult, lower = 1, strings.TrimSuffix(lower, "b")
	case strings.HasSuffix(lower, "k"):
		mult, lower = 1<<10, strings.TrimSuffix(lower, "k")
	case strings.HasSuffix(lower, "m"):
		mult, lower = 1<<20, strings.TrimSuffix(lower, "m")
	case strings.HasSuffix(lower, "g"):
		mult, lower = 1<<30, strings.TrimSuffix(lower, "g")
	case strings.HasSuffix(lower, "t"):
		mult, lower = 1<<40, strings.TrimSuffix(lower, "t")
	}

	n, err := strconv.ParseInt(strings.TrimSpace(lower), 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	// Guard against overflow when scaling by the unit multiplier.
	if mult > 1 && n > (1<<62)/mult {
		return 0, false
	}
	return n * mult, true
}
