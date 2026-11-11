package pkg

import (
	"bytes"
	_ "embed"
	"image"
	"image/color"
	"image/png"
	"math"
	"sync"

	"github.com/fogleman/gg"
	qrcode "github.com/skip2/go-qrcode"
	"golang.org/x/image/draw"
)

// Sniff's QR is the Badger logo taken apart: the badger's head in the
// middle of the code (a quarter of the code's width, the modules under it
// left out, at high error correction: the badge's design, drawQRTile) and
// the logo's own orange corners, copied 1:1 from the logo, framing the
// whole code in its margin. Transparent, it has no white at all: only the
// code, the head and the corners. jsQR (the reader Snout's scanner uses)
// reads it everywhere a plain QR does (1024-160 px, scaled and blurred,
// and on the slide).
const (
	sniffQRLevel   = qrcode.High
	sniffLogoShare = 0.24 // the head's side, share of the code's width
	sniffBorder    = 5    // the margin round the code, in modules: room for the corners
)

// SniffQROpts picks a variant.
type SniffQROpts struct {
	Transparent bool // no background: only the code, the head and the corners (keyed out, to paste on a slide)
	Bare        bool // the code and the head only: no margin, no corners (the slide draws its own)
}

//go:embed images/badger-head.png
var badgerHeadPNG []byte

var (
	logoOnce              sync.Once
	logoHead, logoCorners image.Image
	logoErr               error
)

// logoParts splits the logo into its head and its orange corners.
func logoParts() error {
	logoOnce.Do(func() {
		src, err := png.Decode(bytes.NewReader(badgerHeadPNG))
		if err != nil {
			logoErr = err
			return
		}
		b := src.Bounds()
		head, corners := image.NewNRGBA(b), image.NewNRGBA(b)
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				c := color.NRGBAModel.Convert(src.At(x, y)).(color.NRGBA)
				if c.R > 150 && c.R > c.G+40 && c.G > c.B { // orange
					corners.SetNRGBA(x, y, c)
				} else {
					head.SetNRGBA(x, y, c)
				}
			}
		}
		logoHead, logoCorners = head, corners
	})
	return logoErr
}

// SniffQR draws url as a size x size QR with the head in the middle and
// the logo's corners round it.
func SniffQR(url string, size int, o SniffQROpts) (image.Image, error) {
	if err := logoParts(); err != nil {
		return nil, err
	}
	qr, err := qrcode.New(url, sniffQRLevel)
	if err != nil {
		return nil, err
	}
	qr.DisableBorder = true
	bits := qr.Bitmap()
	n := len(bits)
	border := sniffBorder
	if o.Bare {
		border = 0
	}
	module := float64(size) / float64(n+2*border)
	dc := gg.NewContext(size, size)
	if !o.Transparent {
		dc.SetColor(color.White)
		dc.Clear()
	}
	// The head's square, a little clear room round it: no modules there.
	s, c := float64(n)*module*sniffLogoShare, float64(size)/2
	clear0, clear1 := c-s/2-s*0.06, c+s/2+s*0.06
	dc.SetColor(color.Black)
	edge := func(i int) float64 { return math.Round(float64(i+border) * module) } // whole pixels: no seams
	for y, row := range bits {
		for x, on := range row {
			x0, y0, x1, y1 := edge(x), edge(y), edge(x+1), edge(y+1)
			if on && !(x1 > clear0 && x0 < clear1 && y1 > clear0 && y0 < clear1) {
				dc.DrawRectangle(x0, y0, x1-x0, y1-y0)
			}
		}
	}
	dc.Fill()
	drawScaled(dc, logoHead, c-s/2, c-s/2, s)
	if !o.Bare {
		drawScaled(dc, logoCorners, 0, 0, float64(size))
	}
	return dc.Image(), nil
}

// drawSniffCorners draws the logo's corners round a w-wide square at x, y
// (the slide's tile).
func drawSniffCorners(dc *gg.Context, x, y, w float64) error {
	if err := logoParts(); err != nil {
		return err
	}
	drawScaled(dc, logoCorners, x, y, w)
	return nil
}

// drawScaled draws img scaled to a w x w square at x, y.
func drawScaled(dc *gg.Context, img image.Image, x, y, w float64) {
	m := image.NewRGBA(image.Rect(0, 0, int(math.Round(w)), int(math.Round(w))))
	draw.CatmullRom.Scale(m, m.Bounds(), img, img.Bounds(), draw.Over, nil)
	dc.DrawImage(m, int(math.Round(x)), int(math.Round(y)))
}
