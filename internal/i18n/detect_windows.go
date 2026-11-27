//go:build windows

package i18n

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var procGetUserDefaultLocaleName = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetUserDefaultLocaleName")

// Detect lists the user's preferred languages, best first: the Windows display
// languages, then the user's locale (GetUserDefaultLocaleName). A user with an
// English display and a Spanish region gets English, which is the language
// Windows itself shows them.
func Detect() []string {
	var out []string
	if langs, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME); err == nil {
		out = append(out, langs...)
	}
	if name := userDefaultLocaleName(); name != "" {
		out = append(out, name)
	}
	return append(out, envLanguages()...)
}

func userDefaultLocaleName() string {
	if procGetUserDefaultLocaleName.Find() != nil {
		return ""
	}
	buf := make([]uint16, 85) // LOCALE_NAME_MAX_LENGTH
	n, _, _ := procGetUserDefaultLocaleName.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return ""
	}
	return windows.UTF16ToString(buf)
}
