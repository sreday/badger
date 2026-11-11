package pkg

import (
	"image"

	"github.com/fogleman/gg"
	qrcode "github.com/skip2/go-qrcode"
)

// Sniff's QR carries a "Q&A" mark in the middle, the badge's design
// (drawQRTile, drawLinkedInLogo): high error correction, and a black
// rounded square on a thin white ring with the letters in white, about a
// fifth of the code's width. The code still reads with the mark covering
// its middle (tested with jsQR, the reader Snout's scanner uses).
const (
	sniffQRLevel   = qrcode.High
	sniffLogoShare = 0.22 // the mark's side, share of the code's width
)

// SniffQR draws url as a size x size QR (with the usual quiet zone unless
// noBorder) with the "Q&A" mark in the middle.
func SniffQR(url string, size int, noBorder bool) (image.Image, error) {
	if err := loadV2Fonts(); err != nil {
		return nil, err
	}
	v2FaceMu.Lock()
	defer v2FaceMu.Unlock()
	return sniffQR(url, size, noBorder)
}

// sniffQR is SniffQR for a caller that already holds v2FaceMu (the slide).
func sniffQR(url string, size int, noBorder bool) (image.Image, error) {
	qr, err := qrcode.New(url, sniffQRLevel)
	if err != nil {
		return nil, err
	}
	qr.DisableBorder = noBorder
	dc := gg.NewContextForImage(qr.Image(size))
	modules := float64(len(qr.Bitmap()))
	code := float64(size)
	if !noBorder {
		code = float64(size) * (modules - 8) / modules // without the 4-module quiet zone on each side
	}
	drawQAMark(dc, float64(size)/2, float64(size)/2, code*sniffLogoShare)
	return dc.Image(), nil
}

// drawQAMark draws "Q&A" in white on a black rounded square centred on cx,
// cy, on a thin white ring: like the LinkedIn mark on a badge.
func drawQAMark(dc *gg.Context, cx, cy, size float64) {
	pad := size * badgeLogoPad
	dc.SetColor(white)
	dc.DrawRoundedRectangle(cx-size/2-pad, cy-size/2-pad, size+2*pad, size+2*pad, size*0.2)
	dc.Fill()
	dc.SetRGB(0, 0, 0)
	dc.DrawRoundedRectangle(cx-size/2, cy-size/2, size, size, size*0.16)
	dc.Fill()
	// As big as fits: the letters fill most of the square's width.
	fs := int(size * 0.5)
	for ; fs > 6; fs-- {
		dc.SetFontFace(v2Face(true, fs))
		if w, _ := dc.MeasureString("Q&A"); w <= size*0.8 {
			break
		}
	}
	dc.SetColor(white)
	dc.DrawStringAnchored("Q&A", cx, cy-capHeight(true, fs)*0.04, 0.5, 0.5)
}
