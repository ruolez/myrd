package terminal

import (
	"reflect"
	"testing"
)

func TestParseAppList(t *testing.T) {
	out := "terminal\t/dev/ttys003\t\t-zsh\n" +
		"terminal\t/dev/ttys004\tbuild\tmake\n" +
		"iterm\tA1B2-C3\tvim notes\t/dev/ttys009\n" +
		"terminal\tttys1\t\tzsh\n" + // not a device path
		"iterm\tbad\"id\tx\t\n" +
		"junk\n"
	want := []Source{
		{ID: "app:terminal:ttys003", Kind: "app", Title: "zsh", Detail: "Terminal · ttys003", Fidelity: "mirror"},
		{ID: "app:terminal:ttys004", Kind: "app", Title: "build", Detail: "Terminal · ttys004", Fidelity: "mirror"},
		{ID: "app:iterm:A1B2-C3", Kind: "app", Title: "vim notes", Detail: "iTerm2 · ttys009", Fidelity: "mirror"},
	}
	if got := parseAppList(out); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestMirrorScreen(t *testing.T) {
	s := mirrorScreen("Last login: Tue\nuser@mac ~ %\n\n\n", 20, 3, "zsh")
	if got := screenText(s); !reflect.DeepEqual(got, []string{"Last login: Tue     ", "user@mac ~ %        ", "                    "}) {
		t.Fatalf("text %q", got)
	}
	if s.CurX != 13 || s.CurY != 1 || s.Title != "zsh" {
		t.Fatalf("cursor %d,%d title %q", s.CurX, s.CurY, s.Title)
	}
	// More lines than rows: the bottom ones show.
	s = mirrorScreen("a\nb\nc\nd", 5, 2, "")
	if got := screenText(s); !reflect.DeepEqual(got, []string{"c    ", "d    "}) {
		t.Fatalf("tall text %q", got)
	}
}

func TestMacKeys(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []macKeyEvent
	}{
		{"a", []macKeyEvent{{Char: 'a'}}},
		{"\r", []macKeyEvent{{Code: 36}}},
		{"\x03", []macKeyEvent{{Code: 0x08, Flags: macControl}}},
		{"\x1b[A", []macKeyEvent{{Code: 126}}},
		{"\x1bb", []macKeyEvent{{Code: 53}, {Char: 'b'}}},
		{"\x7f", []macKeyEvent{{Code: 51}}},
		{" ", []macKeyEvent{{Code: 49}}},
		{"É", []macKeyEvent{{Char: 'É'}}},
		{"\x1b[1;2C", []macKeyEvent{{Code: 124, Flags: macShift}}},
	} {
		keys, _ := ParseKeys([]byte(c.in))
		var got []macKeyEvent
		for _, k := range keys {
			got = append(got, macKeys(k)...)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("macKeys(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestFindPID(t *testing.T) {
	ps := "  1 /sbin/launchd\n24686 /System/Applications/Utilities/Terminal.app/Contents/MacOS/Terminal\n" +
		"  901 /Applications/iTerm.app/Contents/MacOS/iTerm2\n 77 /usr/bin/TerminalHelper\n"
	for name, want := range map[string]int{"Terminal": 24686, "iTerm2": 901, "Ghostty": 0} {
		if got := findPID(ps, name); got != want {
			t.Errorf("findPID(%q) = %d, want %d", name, got, want)
		}
	}
}
