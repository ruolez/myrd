package terminal

import (
	"bytes"
	"strconv"
	"strings"
)

// Attr is how a cell looks. Colors are 0-15 in ANSI order (8-15 bright)
// or -1 for the terminal's default.
type Attr struct {
	Fg, Bg    int8
	Bold      bool
	Underline bool
	Reverse   bool
}

// DefaultAttr is the terminal's default look.
var DefaultAttr = Attr{Fg: -1, Bg: -1}

// Cell is one character cell. Ch 0 is the right half of a wide character.
type Cell struct {
	Ch rune
	A  Attr
}

var blankCell = Cell{Ch: ' ', A: DefaultAttr}

// Screen is a snapshot of a terminal that has no byte stream to relay (a
// Windows console window, a Terminal.app tab), drawn for the browser by
// EncodeScreen.
type Screen struct {
	Cols, Rows int
	Cells      []Cell // row-major
	CurX, CurY int
	Cursor     bool // visible
	Title      string
}

// NewScreen is a blank cols×rows screen with a visible cursor at the top left.
func NewScreen(cols, rows int) *Screen {
	s := &Screen{Cols: cols, Rows: rows, Cells: make([]Cell, cols*rows), Cursor: true}
	for i := range s.Cells {
		s.Cells[i] = blankCell
	}
	return s
}

func (s *Screen) row(y int) []Cell { return s.Cells[y*s.Cols : (y+1)*s.Cols] }

// SetText writes text lines from the top left in the default look; lines
// and characters past the screen's edges are dropped.
func (s *Screen) SetText(lines []string) {
	for y, l := range lines {
		if y >= s.Rows {
			break
		}
		x := 0
		for _, r := range l {
			if x >= s.Cols {
				break
			}
			if r < 0x20 || r == 0x7f {
				r = ' '
			}
			s.Cells[y*s.Cols+x] = Cell{Ch: r, A: DefaultAttr}
			x++
		}
	}
}

// Clip is the part of s a cols×rows viewer sees: the left columns, and the
// rows ending with the cursor's when s is taller.
func (s *Screen) Clip(cols, rows int) *Screen {
	if cols >= s.Cols && rows >= s.Rows {
		return s
	}
	c, r := min(cols, s.Cols), min(rows, s.Rows)
	top := max(0, min(s.CurY-r+1, s.Rows-r))
	out := &Screen{Cols: c, Rows: r, Cells: make([]Cell, c*r), CurX: min(s.CurX, c-1), CurY: s.CurY - top,
		Cursor: s.Cursor, Title: s.Title}
	for y := 0; y < r; y++ {
		row := out.row(y)
		copy(row, s.row(top + y)[:c])
		if c < s.Cols && s.row(top + y)[c].Ch == 0 { // a wide character cut in half
			row[c-1] = Cell{Ch: ' ', A: row[c-1].A}
		}
	}
	if out.CurY < 0 {
		out.CurY, out.Cursor = 0, false
	}
	return out
}

// ConsoleAttr converts a Windows console character attribute. The
// console's usual light grey on black becomes the terminal's default look.
func ConsoleAttr(a uint16) Attr {
	at := Attr{Fg: consoleColor(a & 0x0f), Bg: consoleColor(a >> 4 & 0x0f),
		Reverse: a&0x4000 != 0, Underline: a&0x8000 != 0}
	if at.Fg == 7 {
		at.Fg = -1
	}
	if at.Bg == 0 {
		at.Bg = -1
	}
	return at
}

// consoleColor maps the console's BGR+intensity bits to ANSI RGB+bright.
func consoleColor(c uint16) int8 {
	var i int8
	if c&4 != 0 {
		i |= 1
	}
	if c&2 != 0 {
		i |= 2
	}
	if c&1 != 0 {
		i |= 4
	}
	if c&8 != 0 {
		i += 8
	}
	return i
}

func sgr(b *bytes.Buffer, a Attr) {
	b.WriteString("\x1b[0")
	if a.Bold {
		b.WriteString(";1")
	}
	if a.Underline {
		b.WriteString(";4")
	}
	if a.Reverse {
		b.WriteString(";7")
	}
	color := func(c int8, base, bright int) {
		if c < 0 {
			return
		}
		b.WriteByte(';')
		if c < 8 {
			b.WriteString(strconv.Itoa(base + int(c)))
		} else {
			b.WriteString(strconv.Itoa(bright + int(c) - 8))
		}
	}
	color(a.Fg, 30, 90)
	color(a.Bg, 40, 100)
	b.WriteByte('m')
}

func cup(b *bytes.Buffer, y, x int) {
	b.WriteString("\x1b[")
	b.WriteString(strconv.Itoa(y + 1))
	b.WriteByte(';')
	b.WriteString(strconv.Itoa(x + 1))
	b.WriteByte('H')
}

func rowsEqual(a, b []Cell) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func rowBlank(r []Cell) bool {
	for _, c := range r {
		if c != blankCell {
			return false
		}
	}
	return true
}

// scrolledBy is how many lines next scrolled up from prev (0: none): the
// smallest k where next's rows [0, rows-k) are prev's [k, rows), not
// counting matches of blank rows only.
func scrolledBy(prev, next *Screen) int {
	for k := 1; k < next.Rows; k++ {
		content := false
		same := true
		for y := 0; y+k < next.Rows; y++ {
			nr := next.row(y)
			if !rowsEqual(nr, prev.row(y+k)) {
				same = false
				break
			}
			content = content || !rowBlank(nr)
		}
		if same && content {
			return k
		}
	}
	return 0
}

// EncodeScreen is the output that turns a terminal showing prev (nil:
// unknown) into next. Rows scrolled off the top go through a real scroll,
// so the viewer keeps them in its scrollback.
func EncodeScreen(prev, next *Screen) []byte {
	var b bytes.Buffer
	b.WriteString("\x1b[0m")
	cur := DefaultAttr
	full := prev == nil || prev.Cols != next.Cols || prev.Rows != next.Rows
	if full {
		b.WriteString("\x1b[H\x1b[2J")
		prev = NewScreen(next.Cols, next.Rows)
		prev.Cursor = !next.Cursor // always say
		if next.Title != "" {
			prev.Title = "\x00"
		}
	} else if k := scrolledBy(prev, next); k > 0 {
		cup(&b, next.Rows-1, 0)
		b.WriteString(strings.Repeat("\n", k))
		shifted := NewScreen(prev.Cols, prev.Rows)
		copy(shifted.Cells, prev.Cells[k*prev.Cols:])
		shifted.CurX, shifted.CurY, shifted.Cursor, shifted.Title = prev.CurX, prev.CurY, prev.Cursor, prev.Title
		prev = shifted
	}
	for y := 0; y < next.Rows; y++ {
		pr, nr := prev.row(y), next.row(y)
		first := -1
		last := -1
		for x := range nr {
			if nr[x] != pr[x] {
				if first < 0 {
					first = x
				}
				last = x
			}
		}
		if first < 0 {
			continue
		}
		if nr[first].Ch == 0 && first > 0 {
			first--
		}
		// Blank default cells from tail on are erased instead of written.
		tail := len(nr)
		for tail > 0 && nr[tail-1] == blankCell {
			tail--
		}
		end := last + 1
		erase := end > tail
		if erase {
			end = tail
		}
		cup(&b, y, first)
		for x := first; x < end; x++ {
			c := nr[x]
			if c.Ch == 0 {
				continue
			}
			if c.A != cur {
				sgr(&b, c.A)
				cur = c.A
			}
			r := c.Ch
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
				r = ' '
			}
			b.WriteRune(r)
		}
		if erase {
			if cur != DefaultAttr {
				b.WriteString("\x1b[0m")
				cur = DefaultAttr
			}
			if end > first {
				cup(&b, y, end) // a character in the last column leaves the cursor there
			}
			b.WriteString("\x1b[K")
		}
	}
	if cur != DefaultAttr {
		b.WriteString("\x1b[0m")
	}
	if next.Title != prev.Title {
		b.WriteString("\x1b]0;")
		b.WriteString(strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
				return -1
			}
			return r
		}, next.Title))
		b.WriteByte('\a')
	}
	cup(&b, next.CurY, next.CurX)
	if next.Cursor != prev.Cursor {
		if next.Cursor {
			b.WriteString("\x1b[?25h")
		} else {
			b.WriteString("\x1b[?25l")
		}
	}
	return b.Bytes()
}
