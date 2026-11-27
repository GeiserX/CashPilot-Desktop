package main

/*
#cgo darwin CFLAGS: -x objective-c -fblocks
#cgo darwin LDFLAGS: -framework AppKit -framework Foundation
#include <AppKit/AppKit.h>
#include <dispatch/dispatch.h>
#include <stdlib.h>

static NSStatusItem *cashpilotStatusItem;
static NSMenuItem *cashpilotShowItem;
static NSMenuItem *cashpilotQuitItem;

static void installCashPilotTrayIcon(void *bytes, int length, const char *showTitle, const char *quitTitle) {
	if (bytes == NULL || length <= 0) {
		return;
	}

	NSData *iconData = [[NSData alloc] initWithBytes:bytes length:(NSUInteger)length];
	NSString *showText = [[NSString alloc] initWithUTF8String:showTitle];
	NSString *quitText = [[NSString alloc] initWithUTF8String:quitTitle];
	dispatch_async(dispatch_get_main_queue(), ^{
		NSImage *image = [[NSImage alloc] initWithData:iconData];
		[image setSize:NSMakeSize(18, 18)];
		[image setTemplate:YES];

		if (cashpilotStatusItem == nil) {
			cashpilotStatusItem = [[NSStatusBar systemStatusBar] statusItemWithLength:NSSquareStatusItemLength];
		}
		cashpilotStatusItem.button.image = image;
		cashpilotStatusItem.button.title = @"☀";
		cashpilotStatusItem.button.imagePosition = NSImageLeft;
		cashpilotStatusItem.button.toolTip = @"CashPilot Desktop";

		NSMenu *menu = [[NSMenu alloc] initWithTitle:@"CashPilot Desktop"];
		NSMenuItem *title = [[NSMenuItem alloc] initWithTitle:@"CashPilot Desktop" action:nil keyEquivalent:@""];
		[title setEnabled:NO];
		[menu addItem:title];
		[menu addItem:[NSMenuItem separatorItem]];

		NSMenuItem *show = [[NSMenuItem alloc] initWithTitle:showText action:@selector(unhide:) keyEquivalent:@""];
		[show setTarget:NSApp];
		[menu addItem:show];

		NSMenuItem *quit = [[NSMenuItem alloc] initWithTitle:quitText action:@selector(terminate:) keyEquivalent:@"q"];
		[quit setTarget:NSApp];
		[menu addItem:quit];

		cashpilotShowItem = show;
		cashpilotQuitItem = quit;
		cashpilotStatusItem.menu = menu;
		[iconData release];
		[showText release];
		[quitText release];
	});
}

// setCashPilotTrayTitles renames the menu's two items after a language change.
static void setCashPilotTrayTitles(const char *showTitle, const char *quitTitle) {
	NSString *showText = [[NSString alloc] initWithUTF8String:showTitle];
	NSString *quitText = [[NSString alloc] initWithUTF8String:quitTitle];
	dispatch_async(dispatch_get_main_queue(), ^{
		if (cashpilotShowItem != nil) [cashpilotShowItem setTitle:showText];
		if (cashpilotQuitItem != nil) [cashpilotQuitItem setTitle:quitText];
		[showText release];
		[quitText release];
	});
}

static void positionCashPilotMainWindow(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		NSWindow *window = [NSApp mainWindow];
		if (window == nil && [[NSApp windows] count] > 0) {
			window = [[NSApp windows] objectAtIndex:0];
		}
		if (window == nil) {
			return;
		}
		NSRect screenFrame = [[NSScreen mainScreen] visibleFrame];
		for (NSScreen *screen in [NSScreen screens]) {
			NSRect candidate = [screen visibleFrame];
			if (candidate.origin.x <= 0 && candidate.origin.y <= 0 &&
				candidate.origin.x + candidate.size.width > 0 &&
				candidate.origin.y + candidate.size.height > 0) {
				screenFrame = candidate;
				break;
			}
		}
		NSPoint topLeft = NSMakePoint(screenFrame.origin.x + 80, screenFrame.origin.y + screenFrame.size.height - 80);
		[window setFrameTopLeftPoint:topLeft];
		[window makeKeyAndOrderFront:nil];
		[NSApp activateIgnoringOtherApps:YES];
	});
}
*/
import "C"
import "unsafe"

// InstallTrayIcon creates a monochrome template status-bar icon on macOS, with
// its Show and Quit items in the current language.
func InstallTrayIcon(icon []byte, show, quit string) {
	if len(icon) == 0 {
		return
	}
	cShow, cQuit := C.CString(show), C.CString(quit)
	defer C.free(unsafe.Pointer(cShow))
	defer C.free(unsafe.Pointer(cQuit))
	C.installCashPilotTrayIcon(unsafe.Pointer(&icon[0]), C.int(len(icon)), cShow, cQuit)
}

// SetTrayLabels renames the tray menu's items after a language change. Before
// the tray exists it does nothing; InstallTrayIcon reads the current language.
func SetTrayLabels(show, quit string) {
	cShow, cQuit := C.CString(show), C.CString(quit)
	defer C.free(unsafe.Pointer(cShow))
	defer C.free(unsafe.Pointer(cQuit))
	C.setCashPilotTrayTitles(cShow, cQuit)
}

// PositionMainWindowOnPrimaryScreen prevents stale multi-monitor coordinates from hiding the app.
func PositionMainWindowOnPrimaryScreen() {
	// SECURITY-REVIEW: Native AppKit call only moves this app's own window on macOS.
	C.positionCashPilotMainWindow()
}
