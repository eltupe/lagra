//go:build !darwin

package menubar

// handleQuitEvent is a no-op off macOS.
func handleQuitEvent(f func()) {}
