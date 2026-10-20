package keymap

import "testing"

func TestWindowsTableKnownKeys(t *testing.T) {
	// Independent expectations from the Microsoft keyboard scan code spec.
	cases := []struct {
		name  string
		usage uint16
		want  Entry
	}{
		{"KeyA", 0x04, Entry{0x1E, 0}},
		{"KeyZ", 0x1D, Entry{0x2C, 0}},
		{"Digit1", 0x1E, Entry{0x02, 0}},
		{"Digit0", 0x27, Entry{0x0B, 0}},
		{"Enter", 0x28, Entry{0x1C, 0}},
		{"NumpadEnter", 0x58, Entry{0x1C, winExt}},
		{"ArrowUp", 0x52, Entry{0x48, winExt}},
		{"Numpad8", 0x60, Entry{0x48, 0}},
		{"ControlRight", 0xE4, Entry{0x1D, winExt}},
		{"AltRight", 0xE6, Entry{0x38, winExt}},
		{"MetaLeft", 0xE3, Entry{0x5B, winExt}},
		{"F11", 0x44, Entry{0x57, 0}},
		{"F24", 0x73, Entry{0x76, 0}},
		{"Pause", 0x48, Entry{WinPause, 0}},
		{"PrintScreen", 0x46, Entry{0x37, winExt}},
		{"IntlBackslash", 0x64, Entry{0x56, 0}},
	}
	for _, c := range cases {
		if got, ok := Windows[c.usage]; !ok || got != c.want {
			t.Errorf("%s (0x%02x): got %+v ok=%v, want %+v", c.name, c.usage, got, ok, c.want)
		}
	}
}

func TestWindowsTableCoversContiguousRanges(t *testing.T) {
	// Letters, digits, control keys, F1..F12, nav block and numpad are contiguous in HID.
	for u := uint16(0x04); u <= 0x67; u++ {
		if _, ok := Windows[u]; !ok {
			t.Errorf("usage 0x%02x missing", u)
		}
	}
	for u := uint16(0xE0); u <= 0xE7; u++ {
		if _, ok := Windows[u]; !ok {
			t.Errorf("modifier 0x%02x missing", u)
		}
	}
}

func TestWindowsTableCodesAreSendable(t *testing.T) {
	for u, e := range Windows {
		if e.Code == WinPause {
			continue
		}
		if e.Code == 0 || e.Code > 0x7F {
			t.Errorf("usage 0x%02x: scancode 0x%x outside set-1 make codes", u, e.Code)
		}
		if e.Flags&^winExt != 0 {
			t.Errorf("usage 0x%02x: unknown flags 0x%x", u, e.Flags)
		}
	}
}

func TestWindowsTableNoAccidentalDuplicates(t *testing.T) {
	// Only Backslash and Non-US # legitimately share a key.
	seen := map[Entry]uint16{}
	for u, e := range Windows {
		if prev, dup := seen[e]; dup {
			pair := [2]uint16{min(prev, u), max(prev, u)}
			if pair != [2]uint16{0x31, 0x32} {
				t.Errorf("usages 0x%02x and 0x%02x both map to %+v", pair[0], pair[1], e)
			}
		}
		seen[e] = u
	}
}

func TestForWindowsReturnsWindowsTable(t *testing.T) {
	if got := For("windows")[0x04]; got != (Entry{0x1E, 0}) {
		t.Fatalf("For(windows)[KeyA] = %+v", got)
	}
}
