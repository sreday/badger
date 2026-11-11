package pkg

import (
	"bytes"
	_ "embed"
	"image"
	"image/color"
	"image/png"
	"sync"

	"github.com/fogleman/gg"
	qrcode "github.com/skip2/go-qrcode"
	"golang.org/x/image/draw"
)

// Sniff's QR is the Badger logo taken apart: the badger's head in the
// middle of the code (on a white rounded square, a quarter of the code's
// width, at high error correction: the badge's design, drawQRTile) and the
// logo's orange scan-frame corners around the whole code, in its white
// margin. jsQR (the reader Snout's scanner uses) reads it everywhere a
// plain QR does (1024-160 px, scaled and blurred, and on the slide).
const (
	sniffQRLevel   = qrcode.High
	sniffLogoShare = 0.24 // the head's side, share of the code's width
)

// frameOrange is the logo's corners (#ec7a2c).
var frameOrange = color.RGBA{0xec, 0x7a, 0x2c, 0xff}

//go:embed images/badger-head.png
var badgerHeadPNG []byte

var (
	headOnce sync.Once
	headImg  image.Image
	headErr  error
)

// badgerHead is the logo's head without its orange corners.
func badgerHead() (image.Image, error) {
	headOnce.Do(func() {
		src, err := png.Decode(bytes.NewReader(badgerHeadPNG))
		if err != nil {
			headErr = err
			return
		}
		b := src.Bounds()
		out := image.NewNRGBA(b)
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				c := color.NRGBAModel.Convert(src.At(x, y)).(color.NRGBA)
				if c.R > 150 && c.R > c.G+40 && c.G > c.B { // the orange corners
					c.A = 0
				}
				out.SetNRGBA(x, y, c)
			}
		}
		headImg = out
	})
	return headImg, headErr
}

// SniffQR draws url as a size x size QR with the head in the middle and,
// unless noBorder, the usual quiet zone with the orange corners in it.
func SniffQR(url string, size int, noBorder bool) (image.Image, error) {
	return sniffQR(url, size, noBorder)
}

// sniffQR is SniffQR; the slide calls it while holding v2FaceMu (it draws
// no text, so it needs no lock of its own).
func sniffQR(url string, size int, noBorder bool) (image.Image, error) {
	head, err := badgerHead()
	if err != nil {
		return nil, err
	}
	qr, err := qrcode.New(url, sniffQRLevel)
	if err != nil {
		return nil, err
	}
	qr.DisableBorder = noBorder
	dc := gg.NewContextForImage(qr.Image(size))
	modules := float64(len(qr.Bitmap()))
	code, module := float64(size), float64(size)/modules
	if !noBorder {
		code = float64(size) * (modules - 8) / modules // without the 4-module quiet zone on each side
		// The corners sit in the outer half of the quiet zone, two modules
		// clear of the code.
		drawFrameCorners(dc, module*1.3, module*1.3, float64(size)-2*module*1.3, module*0.8)
	}
	s, c := code*sniffLogoShare, float64(size)/2
	ring := s * 0.06
	dc.SetColor(white)
	dc.DrawRoundedRectangle(c-s/2-ring, c-s/2-ring, s+2*ring, s+2*ring, s*0.18)
	dc.Fill()
	mark := image.NewRGBA(image.Rect(0, 0, int(s), int(s)))
	draw.CatmullRom.Scale(mark, mark.Bounds(), head, head.Bounds(), draw.Over, nil)
	dc.DrawImageAnchored(mark, int(c), int(c), 0.5, 0.5)
	return dc.Image(), nil
}

// drawFrameCorners draws the logo's four orange corners on a square of
// side w at (x, y), lines thick px wide, round-capped.
func drawFrameCorners(dc *gg.Context, x, y, w, thick float64) {
	arm := w * 0.16
	dc.SetColor(frameOrange)
	dc.SetLineWidth(thick)
	dc.SetLineCap(gg.LineCapRound)
	dc.SetLineJoin(gg.LineJoinRound)
	x0, y0, x1, y1 := x, y, x+w, y+w
	for _, c := range [][6]float64{
		{x0, y0 + arm, x0, y0, x0 + arm, y0},
		{x1 - arm, y0, x1, y0, x1, y0 + arm},
		{x1, y1 - arm, x1, y1, x1 - arm, y1},
		{x0 + arm, y1, x0, y1, x0, y1 - arm},
	} {
		dc.MoveTo(c[0], c[1])
		dc.LineTo(c[2], c[3])
		dc.LineTo(c[4], c[5])
		dc.Stroke()
	}
}
