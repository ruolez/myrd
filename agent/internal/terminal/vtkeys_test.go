package terminal

import (
	"math/rand"
	"reflect"
	"testing"
)

func TestParseKeys(t *testing.T) {
	for in, want := range map[string][]Key{
		"aZ1 ":                 {{VK: 'A', Char: 'a'}, {VK: 'Z', Char: 'Z', Shift: true}, {VK: '1', Char: '1'}, {VK: VKSpace, Char: ' '}},
		"é€":                   {{Char: 'é'}, {Char: '€'}},
		"\r\t\x7f":             {{VK: VKReturn, Char: '\r'}, {VK: VKTab, Char: '\t'}, {VK: VKBack, Char: 0x08}},
		"\x03\x1a":             {{VK: 'C', Char: 3, Ctrl: true}, {VK: 'Z', Char: 0x1a, Ctrl: true}},
		"\x1b":                 {{VK: VKEscape, Char: 0x1b}},
		"\x1b\x1b":             {{VK: VKEscape, Char: 0x1b}, {VK: VKEscape, Char: 0x1b}},
		"\x1bb":                {{VK: 'B', Char: 'b', Alt: true}},
		"\x1b[A\x1bOB":         {{VK: VKUp}, {VK: VKDown}},
		"\x1b[1;5C":            {{VK: VKRight, Ctrl: true}},
		"\x1b[1;2D":            {{VK: VKLeft, Shift: true}},
		"\x1b[3~":              {{VK: VKDelete}},
		"\x1b[5;3~":            {{VK: VKPrior, Alt: true}},
		"\x1b[24~":             {{VK: VKF1 + 11}},
		"\x1bOP":               {{VK: VKF1}},
		"\x1b[Z":               {{VK: VKTab, Char: '\t', Shift: true}},
		"\x1b[200~hi\x1b[201~": {{VK: 'H', Char: 'h'}, {VK: 'I', Char: 'i'}},
		"\x1b[99x":             nil,
	} {
		got, rest := ParseKeys([]byte(in))
		if !reflect.DeepEqual(got, want) || rest != nil {
			t.Errorf("ParseKeys(%q) = %+v rest %q, want %+v", in, got, rest, want)
		}
	}
}

func TestParseKeysKeepsIncompleteSequences(t *testing.T) {
	for _, in := range []string{"\x1b[", "\x1b[1;5", "\x1bO", "\xe2\x82"} {
		got, rest := ParseKeys([]byte("x" + in))
		if !reflect.DeepEqual(got, []Key{{VK: 'X', Char: 'x'}}) || string(rest) != in {
			t.Errorf("ParseKeys(x%q) = %+v rest %q", in, got, rest)
		}
	}
}

// Input split anywhere gives the same keys when the rest is carried over.
func TestParseKeysSplitAnywhere(t *testing.T) {
	in := []byte("ls -la\r\x1b[A\x1b[1;5D\x1bOQ\x1bx€\x03\x1b[3~")
	whole, _ := ParseKeys(in)
	rng := rand.New(rand.NewSource(3))
	for i := 0; i < 500; i++ {
		var got []Key
		var rest []byte
		for p := in; len(p) > 0; {
			n := 1 + rng.Intn(len(p))
			if p[n-1] == 0x1b && n < len(p) {
				n++ // a lone ESC at the end of input is the Escape key
			}
			k, r := ParseKeys(append(rest, p[:n]...))
			got, rest, p = append(got, k...), r, p[n:]
		}
		if !reflect.DeepEqual(got, whole) || rest != nil {
			t.Fatalf("split parse %+v rest %q, want %+v", got, rest, whole)
		}
	}
}
