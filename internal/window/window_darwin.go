//go:build darwin

// Package window opens lagra's settings page in a native macOS window with a
// WKWebView. The page itself is served by the daemon on 127.0.0.1; this is
// only the shell. It runs on the Cocoa main thread that fyne.io/systray
// already drives, so calls are dispatched onto the main queue.
package window

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -fmodules
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#include <stdlib.h>
void lagra_window_open(const char *url, const char *title, int width, int height);
char *lagra_pick_directory(const char *start);
*/
import "C"

import "unsafe"

// Open shows the window (creating it on first use) and loads url.
func Open(url, title string, width, height int) {
	cu := C.CString(url)
	ct := C.CString(title)
	defer C.free(unsafe.Pointer(cu))
	defer C.free(unsafe.Pointer(ct))
	C.lagra_window_open(cu, ct, C.int(width), C.int(height))
}

// PickDirectory shows a native folder chooser and returns the chosen path.
// ok is false when the user cancelled.
func PickDirectory(start string) (path string, ok bool) {
	cs := C.CString(start)
	defer C.free(unsafe.Pointer(cs))
	res := C.lagra_pick_directory(cs)
	if res == nil {
		return "", false
	}
	defer C.free(unsafe.Pointer(res))
	return C.GoString(res), true
}
