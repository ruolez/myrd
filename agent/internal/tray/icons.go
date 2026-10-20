package tray

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

// Icon variants. Idle is a macOS template image (black + alpha, recoloured by
// the menu bar); InSession is solid red so the session state cannot be missed.
type iconKind int

const (
	iconIdle iconKind = iota
	iconInSession
	iconOffline
)

const iconPx = 32 // 16 pt @2x on macOS; Windows scales the ICO entry

var (
	colIdle    = color.NRGBA{0, 0, 0, 255}
	colSession = color.NRGBA{0xD9, 0x30, 0x25, 255}
	colOffline = color.NRGBA{0x80, 0x86, 0x8B, 200}
)

// monitorGlyph reports how much of pixel (px,py) a small monitor glyph covers,
// using 4x4 supersampling for antialiasing. filled paints the screen area.
func monitorGlyph(px, py int, filled bool) float64 {
	const n = 4
	hits := 0
	for sy := 0; sy < n; sy++ {
		for sx := 0; sx < n; sx++ {
			x := float64(px) + (float64(sx)+0.5)/n
			y := float64(py) + (float64(sy)+0.5)/n
			if inGlyph(x, y, filled) {
				hits++
			}
		}
	}
	return float64(hits) / (n * n)
}

func inGlyph(x, y float64, filled bool) bool {
	// Screen: rounded rect (2,4)-(30,23), radius 3, stroke 2.6.
	const x0, y0, x1, y1, r, stroke = 2.0, 4.0, 30.0, 23.0, 3.0, 2.6
	d := roundedRectDist(x, y, x0, y0, x1, y1, r) // <0 inside
	if filled && d <= 0 {
		return true
	}
	if d <= 0 && d > -stroke {
		return true
	}
	// Stand neck and base.
	if x >= 14 && x <= 18 && y >= 23 && y <= 26.5 {
		return true
	}
	return x >= 9 && x <= 23 && y >= 26.5 && y <= 28.5
}

func roundedRectDist(x, y, x0, y0, x1, y1, r float64) float64 {
	cx, cy := (x0+x1)/2, (y0+y1)/2
	hx, hy := (x1-x0)/2-r, (y1-y0)/2-r
	qx, qy := math.Abs(x-cx)-hx, math.Abs(y-cy)-hy
	outside := math.Hypot(math.Max(qx, 0), math.Max(qy, 0))
	inside := math.Min(math.Max(qx, qy), 0)
	return outside + inside - r
}

func renderIcon(k iconKind) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, iconPx, iconPx))
	c := colIdle
	switch k {
	case iconInSession:
		c = colSession
	case iconOffline:
		c = colOffline
	}
	for y := 0; y < iconPx; y++ {
		for x := 0; x < iconPx; x++ {
			cov := monitorGlyph(x, y, k == iconInSession)
			if cov == 0 {
				continue
			}
			img.SetNRGBA(x, y, color.NRGBA{c.R, c.G, c.B, uint8(math.Round(float64(c.A) * cov))})
		}
	}
	if k == iconInSession {
		// White "live" dot in the middle of the red screen.
		for y := 0; y < iconPx; y++ {
			for x := 0; x < iconPx; x++ {
				if math.Hypot(float64(x)+0.5-16, float64(y)+0.5-13.5) <= 3.5 {
					img.SetNRGBA(x, y, color.NRGBA{255, 255, 255, 255})
				}
			}
		}
	}
	return img
}

func iconPNG(k iconKind) []byte {
	var buf bytes.Buffer
	_ = png.Encode(&buf, renderIcon(k))
	return buf.Bytes()
}

// iconICO wraps the PNG in a single-entry .ico (PNG-compressed entries are
// valid since Windows Vista), which is what systray expects on Windows.
func iconICO(k iconKind) []byte {
	p := iconPNG(k)
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, struct {
		Reserved, Type, Count uint16
		W, H, Colors, Res     uint8
		Planes, BPP           uint16
		Size, Offset          uint32
	}{0, 1, 1, iconPx, iconPx, 0, 0, 1, 32, uint32(len(p)), 22})
	buf.Write(p)
	return buf.Bytes()
}
