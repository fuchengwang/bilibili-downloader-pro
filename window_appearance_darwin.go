//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

static void setAppAppearance(int mode) {
    dispatch_async(dispatch_get_main_queue(), ^{
        if (NSApp == nil) {
            return;
        }
        NSAppearance *appearance = nil;
        if (mode == 1) {
            appearance = [NSAppearance appearanceNamed:NSAppearanceNameAqua];
        } else if (mode == 2) {
            appearance = [NSAppearance appearanceNamed:NSAppearanceNameDarkAqua];
        }
        [NSApp setAppearance:appearance];
        for (NSWindow *win in [NSApp windows]) {
            [win setAppearance:appearance];
        }
    });
}
*/
import "C"

func applyNativeWindowAppearance(theme string) {
	mode := 0
	switch theme {
	case "light":
		mode = 1
	case "dark":
		mode = 2
	}
	C.setAppAppearance(C.int(mode))
}
