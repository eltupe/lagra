//go:build darwin

package menubar

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -fmodules
#cgo LDFLAGS: -framework Cocoa
void lagra_handle_quit_event(void);
*/
import "C"

var quitEventFunc func()

// handleQuitEvent makes the quit Apple event call f instead of ending the
// process. macOS sends that event at logout, and `brew upgrade`, `brew
// uninstall` and `osascript -e 'quit app "Lagra"'` send it too. AppKit's own
// handler exits right after applicationWillTerminate, so the daemon never
// unmounted and the volumes stayed mounted against a stopped NFS server.
// Must be called after the app has finished launching: AppKit installs its
// handler then and would replace this one.
func handleQuitEvent(f func()) {
	quitEventFunc = f
	C.lagra_handle_quit_event()
}

//export lagraQuitEvent
func lagraQuitEvent() {
	if f := quitEventFunc; f != nil {
		go f() // off the main thread: shutting down updates the menu
	}
}
