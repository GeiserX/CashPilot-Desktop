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
		[image setTemplate:YES];

		cashpilotStatusItem = [[NSStatusBar systemStatusBar] statusItemWithLength:NSSquareStatusItemLength];
		cashpilotStatusItem.button.image = image;
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
