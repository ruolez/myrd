package keymap

// hidToEvdev is the Linux kernel's hid_keyboard[] table (drivers/hid/hid-input.c)
// for usages 0x00–0x94; 0 = unmapped.
var hidToEvdev = [...]uint8{
	0, 0, 0, 0, 30, 48, 46, 32, 18, 33, 34, 35, 23, 36, 37, 38, // 0x00
	50, 49, 24, 25, 16, 19, 31, 20, 22, 47, 17, 45, 21, 44, 2, 3, // 0x10
	4, 5, 6, 7, 8, 9, 10, 11, 28, 1, 14, 15, 57, 12, 13, 26, // 0x20
	27, 43, 43, 39, 40, 41, 51, 52, 53, 58, 59, 60, 61, 62, 63, 64, // 0x30
	65, 66, 67, 68, 87, 88, 99, 70, 119, 110, 102, 104, 111, 107, 109, 106, // 0x40
	105, 108, 103, 69, 98, 55, 74, 78, 96, 79, 80, 81, 75, 76, 77, 71, // 0x50
	72, 73, 82, 83, 86, 127, 116, 117, 183, 184, 185, 186, 187, 188, 189, 190, // 0x60
	191, 192, 193, 194, 134, 138, 130, 132, 128, 129, 131, 137, 133, 135, 136, 113, // 0x70
	115, 114, 0, 0, 0, 121, 0, 89, 93, 124, 92, 94, 95, 0, 0, 0, // 0x80
	122, 123, 90, 91, 85, // 0x90
}

// Modifiers 0xE0–0xE7: LCtrl LShift LAlt LMeta RCtrl RShift RAlt RMeta.
var hidModToEvdev = [8]uint8{29, 42, 56, 125, 97, 54, 100, 126}

// X11 maps HID usage → X11 keycode (Linux evdev code + 8).
var X11 = buildX11()

func buildX11() Table {
	t := Table{}
	for usage, code := range hidToEvdev {
		if code != 0 {
			t[uint16(usage)] = Entry{Code: uint32(code) + 8}
		}
	}
	for i, code := range hidModToEvdev {
		t[uint16(0xE0+i)] = Entry{Code: uint32(code) + 8}
	}
	return t
}
