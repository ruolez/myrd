package terminal

import (
	"path"
	"strconv"
	"strings"

	"github.com/ruolez/remotectl/proto"
)

// Mac terminal apps whose tabs can be mirrored.
const (
	appTerminal = "terminal" // Terminal.app
	appITerm    = "iterm"    // iTerm2
)

// parseAppList reads the tab list the listing script prints: one tab per
// line, "terminal\t<tty>\t<custom title>\t<foreground process>" or
// "iterm\t<session id>\t<name>\t<tty>".
func parseAppList(out string) []Source {
	var ss []Source
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 4 {
			continue
		}
		switch f[0] {
		case appTerminal:
			tty := path.Base(f[1])
			if !strings.HasPrefix(f[1], "/dev/") || !validSessionName(tty) {
				continue
			}
			title := f[2]
			if title == "" {
				title = strings.TrimPrefix(f[3], "-")
			}
			if title == "" {
				title = "Terminal"
			}
			ss = append(ss, Source{ID: "app:" + appTerminal + ":" + tty, Kind: proto.TermSourceApp, Title: title,
				Detail: "Terminal · " + tty, Fidelity: "mirror"})
		case appITerm:
			if !validSessionName(f[1]) || strings.ContainsAny(f[1], "\"\\") {
				continue
			}
			title := f[2]
			if title == "" {
				title = "iTerm2"
			}
			detail := "iTerm2"
			if f[3] != "" {
				detail += " · " + path.Base(f[3])
			}
			ss = append(ss, Source{ID: "app:" + appITerm + ":" + f[1], Kind: proto.TermSourceApp, Title: title,
				Detail: detail, Fidelity: "mirror"})
		}
	}
	return ss
}

// mirrorScreen turns a tab's text into a screen. The apps do not say where
// the cursor is: it goes after the last line's text, past a prompt sign.
func mirrorScreen(text string, cols, rows int, title string) *Screen {
	cols, rows = max(1, min(cols, 1000)), max(1, min(rows, 500))
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > rows {
		lines = lines[len(lines)-rows:]
	}
	s := NewScreen(cols, rows)
	s.SetText(lines)
	s.Title = title
	y := len(lines) - 1
	for y > 0 && strings.TrimSpace(lines[y]) == "" {
		y--
	}
	last := []rune(strings.TrimRight(lines[max(y, 0)], " "))
	x := len(last)
	if x > 0 && strings.ContainsRune("$%#>❯:", last[x-1]) {
		x++
	}
	s.CurX, s.CurY = min(x, cols-1), max(y, 0)
	return s
}

// Mac virtual key codes (kVK_*) and event flags.
var macVK = map[uint16]uint16{VKReturn: 36, VKTab: 48, VKSpace: 49, VKBack: 51, VKEscape: 53, VKLeft: 123,
	VKRight: 124, VKDown: 125, VKUp: 126, VKHome: 115, VKEnd: 119, VKPrior: 116, VKNext: 121, VKDelete: 117,
	VKF1: 122, VKF1 + 1: 120, VKF1 + 2: 99, VKF1 + 3: 118, VKF1 + 4: 96, VKF1 + 5: 97, VKF1 + 6: 98, VKF1 + 7: 100,
	VKF1 + 8: 101, VKF1 + 9: 109, VKF1 + 10: 103, VKF1 + 11: 111}

// macLetter is the ANSI layout key code of each letter.
var macLetter = [26]uint16{0x00, 0x0b, 0x08, 0x02, 0x0e, 0x03, 0x05, 0x04, 0x22, 0x26, 0x28, 0x25, 0x2e, 0x2d, 0x1f,
	0x23, 0x0c, 0x0f, 0x01, 0x11, 0x20, 0x09, 0x0d, 0x07, 0x10, 0x06}

const (
	macShift   = 0x20000
	macControl = 0x40000
)

// macKeyEvent is a key press to post to a Mac app.
type macKeyEvent struct {
	Code  uint16
	Char  rune // typed as text when not 0
	Flags uint64
}

// macKeys is how a Mac app is sent k: Alt becomes an Escape first, the
// way a terminal's meta key works.
func macKeys(k Key) []macKeyEvent {
	var out []macKeyEvent
	if k.Alt {
		out = append(out, macKeyEvent{Code: 53})
	}
	var flags uint64
	if k.Shift {
		flags |= macShift
	}
	switch {
	case k.Ctrl && k.VK >= 'A' && k.VK <= 'Z':
		return append(out, macKeyEvent{Code: macLetter[k.VK-'A'], Flags: flags | macControl})
	case k.VK == VKSpace && k.Ctrl:
		return append(out, macKeyEvent{Code: 49, Flags: macControl})
	}
	if code, ok := macVK[k.VK]; ok && (k.Char == 0 || k.Char < 0x20 || k.Char == 0x7f || k.VK == VKSpace) {
		if k.Ctrl {
			flags |= macControl
		}
		return append(out, macKeyEvent{Code: code, Flags: flags})
	}
	if k.Char >= 0x20 && k.Char != 0x7f {
		return append(out, macKeyEvent{Char: k.Char})
	}
	return nil
}

// findPID reads `ps -axo pid=,comm=` output for an app's executable name.
func findPID(ps, name string) int {
	for _, line := range strings.Split(ps, "\n") {
		pid, comm, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok && path.Base(strings.TrimSpace(comm)) == name {
			n, _ := strconv.Atoi(pid)
			return n
		}
	}
	return 0
}
