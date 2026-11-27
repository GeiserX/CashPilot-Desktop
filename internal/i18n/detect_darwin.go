//go:build darwin

package i18n

/*
#cgo darwin CFLAGS: -x objective-c
#cgo darwin LDFLAGS: -framework Foundation
#include <Foundation/Foundation.h>
#include <stdlib.h>
#include <string.h>

// preferredLanguages is the app's AppleLanguages list joined with newlines. It
// honours a per-app choice (System Settings > Language & Region > Applications,
// or `defaults write <bundle id> AppleLanguages`) and the -AppleLanguages launch
// argument, before the system list.
static char *cashpilotPreferredLanguages(void) {
	@autoreleasepool {
		NSArray<NSString *> *langs = [NSLocale preferredLanguages];
		NSString *joined = [langs componentsJoinedByString:@"\n"];
		const char *utf8 = [joined UTF8String];
		return utf8 ? strdup(utf8) : NULL;
	}
}
*/
import "C"

import (
	"strings"
	"unsafe"
)

// Detect lists the user's preferred languages, best first.
func Detect() []string {
	out := envLanguages()
	cs := C.cashpilotPreferredLanguages()
	if cs == nil {
		return out
	}
	defer C.free(unsafe.Pointer(cs))
	langs := strings.Split(C.GoString(cs), "\n")
	// The OS list goes first; LANG in a terminal launch is only a fallback.
	return append(langs, out...)
}
