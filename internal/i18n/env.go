package i18n

import (
	"os"
	"strings"
)

// envLanguages reads the POSIX locale variables in the order gettext does.
// LANGUAGE is a colon list and counts only when a locale is set at all.
func envLanguages() []string {
	var out []string
	locale := ""
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(name); v != "" {
			locale = v
			break
		}
	}
	if locale != "" && locale != "C" && locale != "POSIX" {
		for _, v := range strings.Split(os.Getenv("LANGUAGE"), ":") {
			if v != "" {
				out = append(out, v)
			}
		}
	}
	if locale != "" {
		out = append(out, locale)
	}
	return out
}
