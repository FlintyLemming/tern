//go:build ignore

// Command genicons draws the QPKG icons as GIFs: tern.gif (64 px),
// tern_80.gif (80 px) and tern_gray.gif (64 px, shown while the app is
// stopped). A rounded square in the interface's primary blue with the white
// pulse line the sidebar shows next to the name.
//
//	go run deploy/qnap/genicons.go <outdir>
package main

import (
	"image"
	"image/color"
	"image/gif"
	"log"
	"math"
	"os"
	"path/filepath"
)

const ss = 4 // supersampling per axis

// The pulse line, in the 24-unit grid of the interface's icon.
var pulse = [][2]float64{{2, 12}, {6, 12}, {9, 3}, {15, 21}, {18, 12}, {22, 12}}

const (
	margin = 0.17  // of the side, around the line's 24-unit grid
	stroke = 0.085 // line width, of the side
	corner = 0.22  // corner radius, of the side
)

// distSeg is the distance from p to the segment ab.
func distSeg(px, py, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	t := ((px-ax)*dx + (py-ay)*dy) / (dx*dx + dy*dy)
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(px-(ax+t*dx), py-(ay+t*dy))
}

// coverage is, per pixel, how much of it the square (bg) and the line (fg)
// cover, in [0,1].
func coverage(size int) (bg, fg [][]float64) {
	n := float64(size * ss)
	r := n * corner
	inSquare := func(x, y float64) bool {
		cx := math.Max(r, math.Min(n-r, x))
		cy := math.Max(r, math.Min(n-r, y))
		return (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r
	}
	unit := func(v float64) float64 { return margin + v/24*(1-2*margin) }
	onLine := func(ux, uy float64) bool {
		for i := 1; i < len(pulse); i++ {
			a, b := pulse[i-1], pulse[i]
			if distSeg(ux, uy, unit(a[0]), unit(a[1]), unit(b[0]), unit(b[1])) <= stroke/2 {
				return true
			}
		}
		return false
	}
	bg = make([][]float64, size)
	fg = make([][]float64, size)
	for py := range size {
		bg[py] = make([]float64, size)
		fg[py] = make([]float64, size)
		for px := range size {
			var b, f float64
			for sy := range ss {
				for sx := range ss {
					x := float64(px*ss+sx) + 0.5
					y := float64(py*ss+sy) + 0.5
					if inSquare(x, y) {
						b++
						if onLine(x/n, y/n) {
							f++
						}
					}
				}
			}
			bg[py][px] = b / (ss * ss)
			fg[py][px] = f / (ss * ss)
		}
	}
	return bg, fg
}

func lerp(a, b color.RGBA, t float64) color.RGBA {
	m := func(x, y uint8) uint8 { return uint8(math.Round(float64(x) + (float64(y)-float64(x))*t)) }
	return color.RGBA{m(a.R, b.R), m(a.G, b.G), m(a.B, b.B), 255}
}

// draw renders one icon. GIF has no partial transparency: pixels less than
// half inside the square are transparent, the rest blend base with white.
func draw(size int, base color.RGBA) *image.Paletted {
	white := color.RGBA{255, 255, 255, 255}
	pal := color.Palette{color.RGBA{0, 0, 0, 0}}
	for i := range 255 {
		pal = append(pal, lerp(base, white, float64(i)/254))
	}
	img := image.NewPaletted(image.Rect(0, 0, size, size), pal)
	bg, fg := coverage(size)
	for y := range size {
		for x := range size {
			if bg[y][x] < 0.5 {
				img.SetColorIndex(x, y, 0)
				continue
			}
			t := fg[y][x] / bg[y][x]
			img.SetColorIndex(x, y, uint8(1+math.Round(t*254)))
		}
	}
	return img
}

func write(path string, img *image.Paletted) {
	f, err := os.Create(path)
	if err != nil {
		log.Fatal(err)
	}
	if err := gif.Encode(f, img, nil); err != nil {
		log.Fatal(err)
	}
	if err := f.Close(); err != nil {
		log.Fatal(err)
	}
}

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: go run deploy/qnap/genicons.go <outdir>")
	}
	out := os.Args[1]
	if err := os.MkdirAll(out, 0o755); err != nil {
		log.Fatal(err)
	}
	blue := color.RGBA{0x00, 0x75, 0xde, 255} // --color-primary
	gray := color.RGBA{0x9a, 0xa5, 0xb1, 255}
	write(filepath.Join(out, "tern.gif"), draw(64, blue))
	write(filepath.Join(out, "tern_80.gif"), draw(80, blue))
	write(filepath.Join(out, "tern_gray.gif"), draw(64, gray))
}
