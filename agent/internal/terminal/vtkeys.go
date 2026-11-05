package terminal

import "unicode/utf8"

// Windows virtual-key codes of the keys terminal input names.
const (
	VKBack   = 0x08
	VKTab    = 0x09
	VKReturn = 0x0d
	VKEscape = 0x1b
	VKSpace  = 0x20
	VKPrior  = 0x21
	VKNext   = 0x22
	VKEnd    = 0x23
	VKHome   = 0x24
	VKLeft   = 0x25
	VKUp     = 0x26
	VKRight  = 0x27
	VKDown   = 0x28
	VKInsert = 0x2d
	VKDelete = 0x2e
	VKF1     = 0x70
)

// Key is one key press, for programs that read keys rather than bytes (a
// Windows console's input queue, a Mac app's key events).
type Key struct {
	VK    uint16 // Windows virtual-key code; 0: derive it from Char
	Char  rune   // what the key types; 0 for keys that type nothing
	Ctrl  bool
	Alt   bool
	Shift bool
}

var csiTilde = map[int]uint16{1: VKHome, 2: VKInsert, 3: VKDelete, 4: VKEnd, 5: VKPrior, 6: VKNext, 7: VKHome, 8: VKEnd,
	11: VKF1, 12: VKF1 + 1, 13: VKF1 + 2, 14: VKF1 + 3, 15: VKF1 + 4, 17: VKF1 + 5, 18: VKF1 + 6, 19: VKF1 + 7,
	20: VKF1 + 8, 21: VKF1 + 9, 23: VKF1 + 10, 24: VKF1 + 11}

var csiLetter = map[byte]uint16{'A': VKUp, 'B': VKDown, 'C': VKRight, 'D': VKLeft, 'H': VKHome, 'F': VKEnd,
	'P': VKF1, 'Q': VKF1 + 1, 'R': VKF1 + 2, 'S': VKF1 + 3}

// ParseKeys turns terminal input (what xterm.js sends) into key presses.
// rest is an incomplete sequence at the end of p; pass it again in front
// of the next input.
func ParseKeys(p []byte) (keys []Key, rest []byte) {
	for len(p) > 0 {
		k, n, ok := parseKey(p)
		if n == 0 {
			return keys, p
		}
		if ok {
			keys = append(keys, k...)
		}
		p = p[n:]
	}
	return keys, nil
}

// parseKey reads the key at the start of p: its presses, the bytes used
// (0: incomplete) and whether it is a key at all (paste markers are not).
func parseKey(p []byte) ([]Key, int, bool) {
	c := p[0]
	switch {
	case c == 0x1b:
		if len(p) == 1 {
			return []Key{{VK: VKEscape, Char: 0x1b}}, 1, true
		}
		switch p[1] {
		case '[':
			return parseCSI(p)
		case 'O':
			if len(p) < 3 {
				return nil, 0, false
			}
			if vk, ok := csiLetter[p[2]]; ok {
				return []Key{{VK: vk}}, 3, true
			}
			return nil, 3, false
		case 0x1b:
			return []Key{{VK: VKEscape, Char: 0x1b}}, 1, true
		}
		k, n, ok := parseKey(p[1:])
		if n == 0 {
			return nil, 0, false
		}
		for i := range k {
			k[i].Alt = true
		}
		return k, n + 1, ok
	case c == 0x7f:
		return []Key{{VK: VKBack, Char: 0x08}}, 1, true
	case c == 0x08:
		return []Key{{VK: VKBack, Char: 0x7f, Ctrl: true}}, 1, true
	case c == '\r':
		return []Key{{VK: VKReturn, Char: '\r'}}, 1, true
	case c == '\n':
		return []Key{{VK: VKReturn, Char: '\n', Ctrl: true}}, 1, true
	case c == '\t':
		return []Key{{VK: VKTab, Char: '\t'}}, 1, true
	case c == 0:
		return []Key{{VK: VKSpace, Ctrl: true}}, 1, true
	case c < 0x1b:
		return []Key{{VK: uint16('A' + c - 1), Char: rune(c), Ctrl: true}}, 1, true
	case c < 0x20:
		vk := map[byte]uint16{0x1c: 0xdc, 0x1d: 0xdd, 0x1e: '6', 0x1f: 0xbd}[c]
		return []Key{{VK: vk, Char: rune(c), Ctrl: true, Shift: c == 0x1e || c == 0x1f}}, 1, true
	}
	if !utf8.FullRune(p) {
		return nil, 0, false
	}
	r, n := utf8.DecodeRune(p)
	k := Key{Char: r}
	switch {
	case r >= 'a' && r <= 'z':
		k.VK = uint16(r - 'a' + 'A')
	case r >= 'A' && r <= 'Z':
		k.VK, k.Shift = uint16(r), true
	case r >= '0' && r <= '9':
		k.VK = uint16(r)
	case r == ' ':
		k.VK = VKSpace
	}
	return []Key{k}, n, true
}

func parseCSI(p []byte) ([]Key, int, bool) {
	i := 2
	for i < len(p) && (p[i] < 0x40 || p[i] > 0x7e) {
		if i-2 > 16 {
			return nil, i, false // not a key sequence
		}
		i++
	}
	if i >= len(p) {
		return nil, 0, false
	}
	final := p[i]
	params := []int{0}
	for _, b := range p[2:i] {
		switch {
		case b >= '0' && b <= '9':
			params[len(params)-1] = params[len(params)-1]*10 + int(b-'0')
		case b == ';':
			params = append(params, 0)
		}
	}
	n := i + 1
	k := Key{}
	switch final {
	case '~':
		if params[0] == 200 || params[0] == 201 { // bracketed paste markers
			return nil, n, false
		}
		vk, ok := csiTilde[params[0]]
		if !ok {
			return nil, n, false
		}
		k.VK = vk
	case 'Z':
		return []Key{{VK: VKTab, Char: '\t', Shift: true}}, n, true
	default:
		vk, ok := csiLetter[final]
		if !ok {
			return nil, n, false
		}
		k.VK = vk
	}
	if len(params) > 1 && params[1] > 1 {
		m := params[1] - 1
		k.Shift, k.Alt, k.Ctrl = m&1 != 0, m&2 != 0, m&4 != 0
	}
	return []Key{k}, n, true
}
