//go:build !darwin

package window

// Open is a no-op off macOS.
func Open(url, title string, width, height int) {}

// PickDirectory is unavailable off macOS.
func PickDirectory(start string) (string, bool) { return "", false }
