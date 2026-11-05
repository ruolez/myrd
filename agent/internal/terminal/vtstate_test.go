package terminal

import (
	"bytes"
	"testing"
)

func TestVTStateTracksModesAcrossSplitFeeds(t *testing.T) {
	stream := []byte("plain\x1b[?1h\x1b[?2004;1006h\x1b[?25l text \x1b[?1006l")
	for split := 0; split <= len(stream); split++ {
		v := newVTState()
		v.feed(stream[:split])
		v.feed(stream[split:])
		got := string(v.restore())
		want := "\x1b[?1h\x1b[?2004h\x1b[?25l"
		if got != want {
			t.Fatalf("split %d: restore = %q, want %q", split, got, want)
		}
	}
}

func TestVTStateAltScreenOffset(t *testing.T) {
	v := newVTState()
	prefix := []byte("$ vim\r\n")
	v.feed(prefix)
	v.feed([]byte("\x1b[?1049h\x1b[Hbuffer"))
	if !v.alt || v.altAt != int64(len(prefix)) {
		t.Fatalf("alt=%v altAt=%d, want true %d", v.alt, v.altAt, len(prefix))
	}
	v.feed([]byte("\x1b[?1049l"))
	if v.alt {
		t.Fatal("alt screen still on after 1049l")
	}
}

func TestVTStateTitle(t *testing.T) {
	cases := []struct {
		name, in, want string
		changed        bool
	}{
		{"bel", "\x1b]0;user@host: ~\x07", "user@host: ~", true},
		{"st", "\x1b]2;make\x1b\\", "make", true},
		{"icon only ignored", "\x1b]1;icon\x07", "", false},
		{"control chars dropped", "\x1b]2;a\x01b\x07", "ab", true},
		{"hyperlink ignored", "\x1b]8;;http://x\x07", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := newVTState()
			changed := v.feed([]byte(c.in))
			if v.title != c.want || changed != c.changed {
				t.Fatalf("title %q changed %v, want %q %v", v.title, changed, c.want, c.changed)
			}
		})
	}
}

func TestVTStateTitleSplitEverywhere(t *testing.T) {
	in := []byte("x\x1b]0;hello world\x07y")
	for split := 0; split <= len(in); split++ {
		v := newVTState()
		v.feed(in[:split])
		v.feed(in[split:])
		if v.title != "hello world" {
			t.Fatalf("split %d: title %q", split, v.title)
		}
	}
}

func TestReplayCutsToLineStartOnceWrapped(t *testing.T) {
	r := newRing(16)
	v := newVTState()
	out := []byte("0123456789\nabcdef\nghij")
	r.write(out)
	v.feed(out)
	got := replay(r, v)
	// Held: last 16 bytes "789\nabcdef\nghij" -> cut after the first newline.
	if !bytes.Equal(got, []byte("abcdef\nghij")) {
		t.Fatalf("replay = %q", got)
	}
}

func TestReplayWholeRingBeforeWrap(t *testing.T) {
	r := newRing(64)
	v := newVTState()
	out := []byte("partial line, no newline")
	r.write(out)
	v.feed(out)
	if got := replay(r, v); !bytes.Equal(got, out) {
		t.Fatalf("replay = %q, want %q", got, out)
	}
}

func TestReplayAltScreenStartsAtSwitch(t *testing.T) {
	r := newRing(1024)
	v := newVTState()
	out := []byte("$ htop\r\n\x1b[?1049h\x1b[?1hTOP")
	r.write(out)
	v.feed(out)
	want := "\x1b[?1049h\x1b[?1hTOP\x1b[?1h"
	if got := string(replay(r, v)); got != want {
		t.Fatalf("replay = %q, want %q", got, want)
	}
}

func TestReplayReentersLostAltScreen(t *testing.T) {
	r := newRing(8)
	v := newVTState()
	out := []byte("\x1b[?1049h0123456789\nscreen")
	r.write(out)
	v.feed(out)
	got := string(replay(r, v))
	want := "\x1b[?1049h\x1b[H\x1b[2Jscreen"
	if got != want {
		t.Fatalf("replay = %q, want %q", got, want)
	}
}
