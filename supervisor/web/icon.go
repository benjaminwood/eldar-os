package web

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/lucasb-eyer/go-colorful"

	"github.com/benjaminwood/eldar-os/supervisor/view"
)

var (
	iconMu    sync.Mutex
	iconCache = map[int][]byte{}
)

// icon serves the app icon: a four-point Eldar star on a Tokyo Night gradient.
// Drawn in code so there's no binary asset to keep in sync with the theme.
func (s *server) icon(w http.ResponseWriter, r *http.Request) {
	size, err := strconv.Atoi(strings.TrimSuffix(r.PathValue("file"), ".png"))
	if err != nil || (size != 180 && size != 192 && size != 512) {
		http.NotFound(w, r)
		return
	}
	iconMu.Lock()
	b, ok := iconCache[size]
	if !ok {
		b = drawIcon(size)
		iconCache[size] = b
	}
	iconMu.Unlock()
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(b)
}

func drawIcon(n int) []byte {
	deep, _ := colorful.Hex(view.Theme.Deep)
	accent, _ := colorful.Hex(view.Theme.Accent)
	cyan, _ := colorful.Hex(view.Theme.Cyan)
	bg, _ := colorful.Hex(view.Theme.Bg)
	star, _ := colorful.Hex(view.Theme.Bright)

	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	const ss = 4 // supersampling per axis
	fn := float64(n)
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					px := (float64(x) + (float64(sx)+0.5)/ss) / fn
					py := (float64(y) + (float64(sy)+0.5)/ss) / fn
					// Diagonal gradient deep → accent → cyan, darkened toward the bottom.
					t := (px + py) / 2
					var c colorful.Color
					if t < 0.5 {
						c = deep.BlendLab(accent, t*2)
					} else {
						c = accent.BlendLab(cyan, (t-0.5)*2)
					}
					c = c.BlendLab(bg, 0.35+0.25*py)
					// Four-point star: an astroid, |x|^½ + |y|^½ ≤ r^½.
					cx, cy := (px-0.5)*2, (py-0.5)*2
					d := math.Sqrt(math.Abs(cx)) + math.Sqrt(math.Abs(cy))
					if d <= math.Sqrt(0.62) {
						c = star
					} else if glow := math.Max(0, 1-math.Hypot(cx, cy)/0.55); glow > 0 {
						c = c.BlendLab(accent, glow*0.35)
					}
					r += c.R
					g += c.G
					b += c.B
					a++
				}
			}
			k := a
			img.Set(x, y, color.NRGBA{uint8(r / k * 255), uint8(g / k * 255), uint8(b / k * 255), 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
