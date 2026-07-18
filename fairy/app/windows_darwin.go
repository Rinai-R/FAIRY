//go:build darwin

package app

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit
#import <AppKit/AppKit.h>

// fairySetNSWindowFrame sets position+size in one non-animated step using the
// same top-left DIP coordinate space as Wails windowSetPosition.
void fairySetNSWindowFrame(void* nsWindow, int x, int y, int width, int height) {
	if (nsWindow == NULL || width <= 0 || height <= 0) {
		return;
	}
	NSWindow* window = (NSWindow*)nsWindow;
	NSScreen* primaryScreen = [[NSScreen screens] firstObject];
	if (primaryScreen == nil) {
		primaryScreen = [NSScreen mainScreen];
	}
	CGFloat primaryHeight = [primaryScreen frame].size.height;
	NSRect frame = NSMakeRect(
		(CGFloat)x,
		primaryHeight - (CGFloat)height - (CGFloat)y,
		(CGFloat)width,
		(CGFloat)height
	);
	[window setFrame:frame display:YES animate:NO];
}
*/
import "C"

import (
	"unsafe"

	"fairy/desktop"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func setWindowBoundsAtomic(window application.Window, bounds desktop.WindowBounds) {
	if window == nil {
		return
	}
	application.InvokeSync(func() {
		native := window.NativeWindow()
		if native == nil {
			window.SetPosition(bounds.X, bounds.Y)
			window.SetSize(bounds.Width, bounds.Height)
			return
		}
		C.fairySetNSWindowFrame(
			unsafe.Pointer(native),
			C.int(bounds.X),
			C.int(bounds.Y),
			C.int(bounds.Width),
			C.int(bounds.Height),
		)
	})
}
