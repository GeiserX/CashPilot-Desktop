//go:build !darwin && !windows

package i18n

// Detect lists the user's preferred languages, best first, from the
// environment: LANGUAGE, LC_ALL, LC_MESSAGES, then LANG.
func Detect() []string { return envLanguages() }
