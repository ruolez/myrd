package terminal

import (
	"math/rand"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// vt is a small terminal emulator: enough of xterm to check what
// EncodeScreen writes.
type vt struct {
	s          *Screen
	attr       Attr
	scrolled   int // lines that went into the scrollback
	pending    bool
	titleCount int
}

func newVT(cols, rows int) *vt {
	s := NewScreen(cols, rows)
	return &vt{s: s, attr: DefaultAttr}
}

func wide(r rune) bool { return r >= 0x4e00 && r <= 0x9fff }

func (t *vt) lineFeed() {
	s := t.s
	if s.CurY < s.Rows-1 {
		s.CurY++
		return
	}
	copy(s.Cells, s.Cells[s.Cols:])
	for x := 0; x < s.Cols; x++ {
		s.Cells[(s.Rows-1)*s.Cols+x] = blankCell
	}
	t.scrolled++
}

func (t *vt) put(r rune) {
	s := t.s
	w := 1
	if wide(r) {
		w = 2
	}
	if t.pending {
		s.CurX, t.pending = 0, false
		t.lineFeed()
	}
	i := s.CurY*s.Cols + s.CurX
	if s.Cells[i].Ch != 0 && wide(s.Cells[i].Ch) && s.CurX+1 < s.Cols {
		s.Cells[i+1] = blankCell
	}
	if s.Cells[i].Ch == 0 && s.CurX > 0 && wide(s.Cells[i-1].Ch) {
		s.Cells[i-1] = blankCell
	}
	if w == 2 && s.CurX+2 < s.Cols && wide(s.Cells[i+1].Ch) {
		s.Cells[i+2] = blankCell
	}
	s.Cells[i] = Cell{Ch: r, A: t.attr}
	if w == 2 && s.CurX+1 < s.Cols {
		s.Cells[i+1] = Cell{Ch: 0, A: t.attr}
	}
	s.CurX += w
	if s.CurX >= s.Cols {
		s.CurX, t.pending = s.Cols-1, true
	}
}

func (t *vt) csi(params string, final byte) {
	s := t.s
	nums := []int{}
	priv := strings.HasPrefix(params, "?")
	for _, p := range strings.Split(strings.TrimPrefix(params, "?"), ";") {
		n, _ := strconv.Atoi(p)
		nums = append(nums, n)
	}
	arg := func(i, def int) int {
		if i < len(nums) && nums[i] != 0 {
			return nums[i]
		}
		return def
	}
	t.pending = false
	switch {
	case final == 'H':
		s.CurY, s.CurX = min(arg(0, 1), s.Rows)-1, min(arg(1, 1), s.Cols)-1
	case final == 'J' && nums[0] == 2:
		for i := range s.Cells {
			s.Cells[i] = Cell{Ch: ' ', A: Attr{Fg: -1, Bg: t.attr.Bg}}
		}
	case final == 'K':
		for x := s.CurX; x < s.Cols; x++ {
			s.Cells[s.CurY*s.Cols+x] = Cell{Ch: ' ', A: Attr{Fg: -1, Bg: t.attr.Bg}}
		}
	case final == 'm':
		for _, n := range nums {
			switch {
			case n == 0:
				t.attr = DefaultAttr
			case n == 1:
				t.attr.Bold = true
			case n == 4:
				t.attr.Underline = true
			case n == 7:
				t.attr.Reverse = true
			case n >= 30 && n <= 37:
				t.attr.Fg = int8(n - 30)
			case n >= 40 && n <= 47:
				t.attr.Bg = int8(n - 40)
			case n >= 90 && n <= 97:
				t.attr.Fg = int8(n - 90 + 8)
			case n >= 100 && n <= 107:
				t.attr.Bg = int8(n - 100 + 8)
			}
		}
	case priv && nums[0] == 25:
		s.Cursor = final == 'h'
	}
}

func (t *vt) write(t2 *testing.T, b []byte) {
	for len(b) > 0 {
		switch b[0] {
		case 0x1b:
			if len(b) > 1 && b[1] == '[' {
				i := 2
				for i < len(b) && (b[i] < 0x40 || b[i] > 0x7e) {
					i++
				}
				t.csi(string(b[2:i]), b[i])
				b = b[i+1:]
				continue
			}
			if len(b) > 1 && b[1] == ']' {
				end := strings.IndexByte(string(b), '\a')
				t.s.Title = strings.TrimPrefix(string(b[2:end]), "0;")
				t.titleCount++
				b = b[end+1:]
				continue
			}
			t2.Fatalf("unexpected escape %q", b)
		case '\n':
			t.pending = false
			t.lineFeed()
			b = b[1:]
		default:
			if b[0] < 0x20 {
				t2.Fatalf("unexpected control %#x", b[0])
			}
			r, n := utf8.DecodeRune(b)
			t.put(r)
			b = b[n:]
		}
	}
}

var (
	testRunes = []rune{' ', ' ', ' ', 'a', 'b', 'Z', '-', 'é', '中'}
	testAttrs = []Attr{DefaultAttr, DefaultAttr, {Fg: 1, Bg: -1}, {Fg: 12, Bg: 4, Bold: true}, {Fg: -1, Bg: 2, Reverse: true},
		{Fg: -1, Bg: -1, Underline: true}}
)

func randScreen(rng *rand.Rand, cols, rows int) *Screen {
	s := NewScreen(cols, rows)
	for y := 0; y < rows; y++ {
		if rng.Intn(4) == 0 {
			continue // a blank row
		}
		n := rng.Intn(cols + 1)
		for x := 0; x < n; x++ {
			r := testRunes[rng.Intn(len(testRunes))]
			a := testAttrs[rng.Intn(len(testAttrs))]
			if wide(r) {
				if x+1 >= cols {
					r = 'w'
				} else {
					s.Cells[y*cols+x] = Cell{Ch: r, A: a}
					x++
					s.Cells[y*cols+x] = Cell{Ch: 0, A: a}
					continue
				}
			}
			s.Cells[y*cols+x] = Cell{Ch: r, A: a}
		}
	}
	s.CurX, s.CurY = rng.Intn(cols), rng.Intn(rows)
	s.Cursor = rng.Intn(3) > 0
	s.Title = []string{"", "cmd", "vim — notes"}[rng.Intn(3)]
	return s
}

// scrollScreen is prev scrolled up by k with fresh rows at the bottom.
func scrollScreen(rng *rand.Rand, prev *Screen, k int) *Screen {
	next := randScreen(rng, prev.Cols, prev.Rows)
	copy(next.Cells, prev.Cells[k*prev.Cols:])
	return next
}

func sameScreen(t *testing.T, label string, got, want *Screen) {
	t.Helper()
	if !reflect.DeepEqual(got.Cells, want.Cells) {
		t.Fatalf("%s: cells differ\n got %q\nwant %q", label, screenText(got), screenText(want))
	}
	if got.CurX != want.CurX || got.CurY != want.CurY || got.Cursor != want.Cursor || got.Title != want.Title {
		t.Fatalf("%s: cursor/title got (%d,%d,%v,%q) want (%d,%d,%v,%q)", label, got.CurX, got.CurY, got.Cursor,
			got.Title, want.CurX, want.CurY, want.Cursor, want.Title)
	}
}

func screenText(s *Screen) []string {
	var out []string
	for y := 0; y < s.Rows; y++ {
		var b strings.Builder
		for _, c := range s.row(y) {
			if c.Ch != 0 {
				b.WriteRune(c.Ch)
			}
		}
		out = append(out, b.String())
	}
	return out
}

func TestEncodeScreenReproducesTheScreen(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 3000; i++ {
		cols, rows := 1+rng.Intn(12), 1+rng.Intn(8)
		prev := randScreen(rng, cols, rows)
		term := newVT(cols, rows)
		term.write(t, EncodeScreen(nil, prev))
		sameScreen(t, "full paint", term.s, prev)

		var next *Screen
		k := 0
		if rows > 1 && rng.Intn(3) == 0 {
			k = 1 + rng.Intn(rows-1)
			next = scrollScreen(rng, prev, k)
		} else {
			next = randScreen(rng, cols, rows)
		}
		before := term.scrolled
		term.write(t, EncodeScreen(prev, next))
		sameScreen(t, "update", term.s, next)
		if k > 0 && scrolledBy(prev, next) > 0 && term.scrolled == before {
			t.Fatalf("scroll of %d drawn without scrolling", k)
		}
	}
}

func TestEncodeScreenUnchangedIsCheap(t *testing.T) {
	s := randScreen(rand.New(rand.NewSource(2)), 80, 24)
	if got, want := string(EncodeScreen(s, s)), "\x1b[0m"+"\x1b["+strconv.Itoa(s.CurY+1)+";"+strconv.Itoa(s.CurX+1)+"H"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestEncodeScreenScrollKeepsLinesForScrollback(t *testing.T) {
	prev := NewScreen(10, 3)
	prev.SetText([]string{"one", "two", "three"})
	next := NewScreen(10, 3)
	next.SetText([]string{"three", "four", "five"})
	term := newVT(10, 3)
	term.write(t, EncodeScreen(nil, prev))
	term.write(t, EncodeScreen(prev, next))
	sameScreen(t, "scrolled", term.s, next)
	if term.scrolled != 2 {
		t.Fatalf("scrolled %d lines, want 2", term.scrolled)
	}
}

func TestClip(t *testing.T) {
	s := NewScreen(6, 5)
	s.SetText([]string{"aaaaaa", "bbbbbb", "cccccc", "dddddd", "eeeeee"})
	s.Cells[2*6+3] = Cell{Ch: '中', A: DefaultAttr}
	s.Cells[2*6+4] = Cell{Ch: 0, A: DefaultAttr}
	s.CurX, s.CurY = 5, 2
	c := s.Clip(4, 2)
	if got := screenText(c); !reflect.DeepEqual(got, []string{"bbbb", "ccc "}) {
		t.Fatalf("clip rows %q", got)
	}
	if c.CurX != 3 || c.CurY != 1 {
		t.Fatalf("clip cursor %d,%d", c.CurX, c.CurY)
	}
	if s.Clip(10, 10) != s {
		t.Fatal("a larger viewer should see the whole screen")
	}
}

func TestConsoleAttr(t *testing.T) {
	for a, want := range map[uint16]Attr{
		0x07:          DefaultAttr,                     // light grey on black
		0x0f:          {Fg: 15, Bg: -1},                // bright white
		0x1e:          {Fg: 11, Bg: 4},                 // bright yellow on blue
		0x4c | 0x8000: {Fg: 9, Bg: 1, Underline: true}, // bright red on red, underlined
		0x70 | 0x4000: {Fg: 0, Bg: 7, Reverse: true},   // black on grey, reversed
		0x02:          {Fg: 2, Bg: -1},                 // green
		0x05:          {Fg: 5, Bg: -1},                 // magenta
		0x56:          {Fg: 3, Bg: 5},                  // PowerShell's yellow on magenta
	} {
		if got := ConsoleAttr(a); got != want {
			t.Errorf("ConsoleAttr(%#x) = %+v, want %+v", a, got, want)
		}
	}
}
