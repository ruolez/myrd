// Package keymap maps USB HID keyboard usages (page 0x07, from the browser's
// KeyboardEvent.code) to platform key codes handed to rc_input_inject.
package keymap

// Entry is one platform key code plus rc_native flags (RC_KEYFLAG_EXTENDED).
type Entry struct {
	Code  uint32
	Flags uint8
}

// Table maps HID usage → platform key.
type Table map[uint16]Entry

// For returns the table for a GOOS value. Tables live in table_mac.go
// (CGKeyCode), table_win.go (set-1 scancodes) and table_x11.go (evdev+8);
// they are plain files without build tags so every table is unit-tested on
// every host.
func For(goos string) Table {
	switch goos {
	case "darwin":
		return Mac
	case "windows":
		return Windows
	default:
		return X11
	}
}
