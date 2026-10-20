//go:build !darwin

package tray

// Notify is a no-op off macOS: Windows shows transfers in the tray tooltip
// and menu, the Linux demo device on its console.
func Notify(title, body string) {}
