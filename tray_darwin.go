package main

/*
#cgo darwin CFLAGS: -x objective-c -fblocks
#cgo darwin LDFLAGS: -framework AppKit -framework Foundation
#include <AppKit/AppKit.h>
#include <dispatch/dispatch.h>

static NSStatusItem *cashpilotStatusItem;

static void installCashPilotTrayIcon(void *bytes, int length) {
	if (bytes == NULL || length <= 0) {
		return;
	}

	NSData *iconData = [[NSData alloc] initWithBytes:bytes length:(NSUInteger)length];
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

		NSMenuItem *show = [[NSMenuItem alloc] initWithTitle:@"Show CashPilot Desktop" action:@selector(unhide:) keyEquivalent:@""];
		[show setTarget:NSApp];
		[menu addItem:show];

		NSMenuItem *quit = [[NSMenuItem alloc] initWithTitle:@"Quit CashPilot Desktop" action:@selector(terminate:) keyEquivalent:@"q"];
		[quit setTarget:NSApp];
		[menu addItem:quit];

		cashpilotStatusItem.menu = menu;
		[iconData release];
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

// InstallTrayIcon creates a monochrome template status-bar icon on macOS.
func InstallTrayIcon(icon []byte) {
	if len(icon) == 0 {
		return
	}
	C.installCashPilotTrayIcon(unsafe.Pointer(&icon[0]), C.int(len(icon)))
}

// PositionMainWindowOnPrimaryScreen prevents stale multi-monitor coordinates from hiding the app.
func PositionMainWindowOnPrimaryScreen() {
	// SECURITY-REVIEW: Native AppKit call only moves this app's own window on macOS.
	C.positionCashPilotMainWindow()
}
