package keymap

import "testing"

// Reference kVK_* values from Carbon HIToolbox/Events.h, spelled out
// independently of the table under test.
const (
	kVKANSIA         = 0x00
	kVKANSIZ         = 0x06
	kVKANSI0         = 0x1D
	kVKANSI5         = 0x17
	kVKReturn        = 0x24
	kVKDelete        = 0x33
	kVKForwardDelete = 0x75
	kVKEscape        = 0x35
	kVKCommand       = 0x37
	kVKRightCommand  = 0x36
	kVKShift         = 0x38
	kVKOption        = 0x3A
	kVKControl       = 0x3B
	kVKF1            = 0x7A
	kVKF12           = 0x6F
	kVKLeftArrow     = 0x7B
	kVKUpArrow       = 0x7E
	kVKKeypadEnter   = 0x4C
	kVKISOSection    = 0x0A
	kVKGrave         = 0x32
)

func TestMacSpotChecks(t *testing.T) {
	cases := []struct {
		name  string
		usage uint16
		want  uint32
	}{
		{"KeyA", 0x04, kVKANSIA},
		{"KeyZ", 0x1D, kVKANSIZ},
		{"Digit5", 0x22, kVKANSI5},
		{"Digit0", 0x27, kVKANSI0},
		{"Enter", 0x28, kVKReturn},
		{"Backspace", 0x2A, kVKDelete},
		{"Delete", 0x4C, kVKForwardDelete},
		{"Escape", 0x29, kVKEscape},
		{"Backquote", 0x35, kVKGrave},
		{"IntlBackslash", 0x64, kVKISOSection},
		{"F1", 0x3A, kVKF1},
		{"F12", 0x45, kVKF12},
		{"ArrowLeft", 0x50, kVKLeftArrow},
		{"ArrowUp", 0x52, kVKUpArrow},
		{"NumpadEnter", 0x58, kVKKeypadEnter},
		{"ControlLeft", 0xE0, kVKControl},
		{"ShiftLeft", 0xE1, kVKShift},
		{"AltLeft", 0xE2, kVKOption},
		{"MetaLeft", 0xE3, kVKCommand},
		{"MetaRight", 0xE7, kVKRightCommand},
	}
	for _, c := range cases {
		got, ok := Mac[c.usage]
		if !ok || got != (Entry{Code: c.want}) {
			t.Errorf("%s (0x%02X): got %+v ok=%v, want code 0x%02X", c.name, c.usage, got, ok, c.want)
		}
	}
}

// Every key a browser on a standard ANSI/ISO keyboard can produce must map.
func TestMacCoversStandardKeyboard(t *testing.T) {
	var required []uint16
	for u := uint16(0x04); u <= 0x65; u++ {
		required = append(required, u)
	}
	for u := uint16(0xE0); u <= 0xE7; u++ {
		required = append(required, u)
	}
	for _, u := range required {
		if _, ok := Mac[u]; !ok {
			t.Errorf("usage 0x%02X has no mapping", u)
		}
	}
}

// Distinct physical keys must not collapse onto one code, except the
// documented aliases (ISO IntlHash shares Backslash; PC PrintScreen/
// ScrollLock/Pause share F13/F14/F15).
func TestMacCodesUniqueExceptAliases(t *testing.T) {
	aliases := map[uint16]uint16{0x32: 0x31, 0x46: 0x68, 0x47: 0x69, 0x48: 0x6A}
	seen := map[uint32]uint16{}
	for u, e := range Mac {
		if _, alias := aliases[u]; alias {
			continue
		}
		if prev, dup := seen[e.Code]; dup {
			t.Errorf("code 0x%02X used by usages 0x%02X and 0x%02X", e.Code, prev, u)
		}
		seen[e.Code] = u
	}
	for alias, canonical := range aliases {
		if Mac[alias] != Mac[canonical] {
			t.Errorf("alias 0x%02X should equal 0x%02X", alias, canonical)
		}
	}
}

func TestMacCodesInRangeAndNoFlags(t *testing.T) {
	for u, e := range Mac {
		if e.Code > 0x7F || e.Flags != 0 {
			t.Errorf("usage 0x%02X → %+v: CGKeyCodes are 0..0x7F with no flags", u, e)
		}
	}
	if For("darwin")[0x04] != Mac[0x04] {
		t.Error(`For("darwin") must return the Mac table`)
	}
}
