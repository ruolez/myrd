package tray

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"testing"
)

func TestIconPNGDecodes(t *testing.T) {
	for _, k := range []iconKind{iconIdle, iconInSession, iconOffline} {
		img, err := png.Decode(bytes.NewReader(iconPNG(k)))
		if err != nil {
			t.Fatalf("kind %d: %v", k, err)
		}
		if b := img.Bounds(); b.Dx() != iconPx || b.Dy() != iconPx {
			t.Fatalf("kind %d: size %v", k, b)
		}
	}
}

func TestIconGlyphHasInkAndTransparency(t *testing.T) {
	img := renderIcon(iconIdle)
	var opaque, clear int
	for i := 3; i < len(img.Pix); i += 4 {
		switch img.Pix[i] {
		case 255:
			opaque++
		case 0:
			clear++
		}
	}
	if opaque < 50 || clear < 300 {
		t.Fatalf("glyph looks wrong: opaque=%d clear=%d", opaque, clear)
	}
}

func TestSessionIconIsRed(t *testing.T) {
	c := renderIcon(iconInSession).NRGBAAt(5, 12) // inside the filled screen, away from the dot
	if c != colSession {
		t.Fatalf("got %v, want %v", c, colSession)
	}
}

func TestICOHeaderPointsAtPNG(t *testing.T) {
	ico := iconICO(iconIdle)
	if binary.LittleEndian.Uint16(ico[2:]) != 1 || binary.LittleEndian.Uint16(ico[4:]) != 1 {
		t.Fatal("not a single-image .ico")
	}
	size := binary.LittleEndian.Uint32(ico[14:])
	off := binary.LittleEndian.Uint32(ico[18:])
	if int(off+size) != len(ico) || !bytes.HasPrefix(ico[off:], []byte("\x89PNG")) {
		t.Fatalf("entry size=%d off=%d len=%d", size, off, len(ico))
	}
}
