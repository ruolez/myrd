package keymap

import "testing"

// Expected X11 keycodes from the standard evdev xkb keycodes file.
func TestX11KnownKeys(t *testing.T) {
	cases := []struct {
		name  string
		usage uint16
		code  uint32
	}{
		{"KeyA", 0x04, 38}, {"KeyZ", 0x1D, 52}, {"Digit1", 0x1E, 10}, {"Digit0", 0x27, 19},
		{"Enter", 0x28, 36}, {"Escape", 0x29, 9}, {"Backspace", 0x2A, 22}, {"Tab", 0x2B, 23},
		{"Space", 0x2C, 65}, {"F1", 0x3A, 67}, {"F12", 0x45, 96}, {"Delete", 0x4C, 119},
		{"ArrowRight", 0x4F, 114}, {"ArrowLeft", 0x50, 113}, {"ArrowDown", 0x51, 116}, {"ArrowUp", 0x52, 111},
		{"NumpadEnter", 0x58, 104}, {"ControlLeft", 0xE0, 37}, {"ShiftLeft", 0xE1, 50}, {"AltLeft", 0xE2, 64},
		{"MetaLeft", 0xE3, 133}, {"ControlRight", 0xE4, 105}, {"ShiftRight", 0xE5, 62}, {"AltRight", 0xE6, 108},
		{"MetaRight", 0xE7, 134}, {"ContextMenu", 0x65, 135},
	}
	for _, c := range cases {
		if got := X11[c.usage]; got.Code != c.code {
			t.Errorf("%s (0x%02x): got %d want %d", c.name, c.usage, got.Code, c.code)
		}
	}
}

func TestX11CoversLettersDigitsFunctionKeys(t *testing.T) {
	for u := uint16(0x04); u <= 0x45; u++ { // letters, digits, punctuation, F1–F12
		if _, ok := X11[u]; !ok {
			t.Errorf("usage 0x%02x unmapped", u)
		}
	}
}

func TestX11CodesUnique(t *testing.T) {
	seen := map[uint32]uint16{}
	for u, e := range X11 {
		if prev, dup := seen[e.Code]; dup && !(u == 0x31 || u == 0x32 || prev == 0x31 || prev == 0x32) {
			t.Errorf("code %d used by 0x%02x and 0x%02x", e.Code, prev, u)
		}
		seen[e.Code] = u
	}
}

func TestForSelectsTable(t *testing.T) {
	if len(For("linux")) != len(X11) {
		t.Fatal("linux should use the X11 table")
	}
}
