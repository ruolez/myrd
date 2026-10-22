package preview

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"

	"github.com/ruolez/remotectl/agent/internal/platform"
)

// MaxSide is the longest edge a preview is captured at.
const MaxSide = 480

// steps are tried in order until the JPEG fits the budget: lower quality
// first, then a smaller picture.
var steps = []struct{ side, quality int }{{480, 70}, {480, 55}, {480, 40}, {360, 50}, {360, 35}}

var errTooLarge = errors.New("preview does not fit")

// encodeJPEG compresses a BGRA picture to at most budget bytes.
func encodeJPEG(img platform.Image, budget int) (jpg []byte, w, h int, err error) {
	for _, st := range steps {
		ycc := toYCbCr(img, st.side)
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, ycc, &jpeg.Options{Quality: st.quality}); err != nil {
			return nil, 0, 0, err
		}
		if buf.Len() <= budget {
			b := ycc.Bounds()
			return buf.Bytes(), b.Dx(), b.Dy(), nil
		}
	}
	return nil, 0, 0, errTooLarge
}

// toYCbCr converts BGRA to 4:2:0 YCbCr, box-averaging down so the longest
// edge is at most side.
func toYCbCr(img platform.Image, side int) *image.YCbCr {
	w, h := img.Width, img.Height
	if m := max(w, h); m > side {
		w, h = max(1, w*side/m), max(1, h*side/m)
	}
	out := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio420)
	for y := 0; y < h; y++ {
		y0, y1 := y*img.Height/h, max((y+1)*img.Height/h, y*img.Height/h+1)
		for x := 0; x < w; x++ {
			x0, x1 := x*img.Width/w, max((x+1)*img.Width/w, x*img.Width/w+1)
			var r, g, b, n int
			for sy := y0; sy < y1; sy++ {
				row := img.Pix[sy*img.Stride:]
				for sx := x0; sx < x1; sx++ {
					p := row[sx*4 : sx*4+4]
					b += int(p[0])
					g += int(p[1])
					r += int(p[2])
					n++
				}
			}
			yy, cb, cr := color.RGBToYCbCr(uint8(r/n), uint8(g/n), uint8(b/n))
			out.Y[out.YOffset(x, y)] = yy
			ci := out.COffset(x, y)
			if x%2 == 0 && y%2 == 0 {
				out.Cb[ci], out.Cr[ci] = cb, cr
			}
		}
	}
	return out
}
