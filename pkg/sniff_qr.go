package pkg

import (
	"image"

	"github.com/fogleman/gg"
	qrcode "github.com/skip2/go-qrcode"
)

// Sniff's QR carries a "Q&A" pill in the middle, so people see what it's
// for: the badge's trick (drawQRTile). The code is made at the highest
// error correction, which recovers far more than the pill covers (about 8%
// of the code), so it reads as if the pill weren't there. Tested with jsQR,
// the reader Snout's scanner uses, at several sizes.
const (
	sniffPillW    = 0.30 // the pill's width, share of the code's width
	sniffPillH    = 0.13 // its height
	sniffPillRing = 0.03 // the white ring around it
)

// SniffQR draws url as a size x size QR (with the usual quiet zone unless
// noBorder) with the "Q&A" pill in the middle.
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
	qr, err := qrcode.New(url, qrcode.Highest)
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
	cx, cy := float64(size)/2, float64(size)/2
	w, h, ring := code*sniffPillW, code*sniffPillH, code*sniffPillRing
	dc.SetColor(white)
	dc.DrawRoundedRectangle(cx-w/2-ring, cy-h/2-ring, w+2*ring, h+2*ring, (h+2*ring)/2)
	dc.Fill()
	dc.SetRGB(0, 0, 0)
	dc.DrawRoundedRectangle(cx-w/2, cy-h/2, w, h, h/2)
	dc.Fill()
	fs := int(h * 0.62)
	if fs < 6 {
		fs = 6
	}
	dc.SetFontFace(v2Face(true, fs))
	dc.SetColor(white)
	dc.DrawStringAnchored("Q&A", cx, cy-h*0.02, 0.5, 0.5)
	return dc.Image(), nil
}
