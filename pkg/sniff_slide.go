package pkg

import (
	"image"
	"image/color"
	"strings"

	"github.com/fogleman/gg"
)

// SniffSlide is the slide a speaker drops into their deck: scan to ask a
// question or rate the talk. 1920x1080, the conference's colour, a big QR
// that reads from the back of the room, and the short link for anyone who'd
// rather type it.
type SniffSlide struct {
	Brand   string // sreday, llmday, platformday
	Title   string
	Speaker string
	URL     string // what the QR opens
	Short   string // the same link, as people type it
}

var slideColours = map[string][2]color.RGBA{ // background, text
	"sreday":      {{0x71, 0x36, 0x60, 0xff}, {0xff, 0xff, 0xff, 0xff}},
	"llmday":      {{0x26, 0x98, 0x6a, 0xff}, {0xff, 0xff, 0xff, 0xff}},
	"platformday": {{0xe2, 0x97, 0x1d, 0xff}, {0x14, 0x1c, 0x25, 0xff}},
}

// GenerateSniffSlide draws the slide.
func GenerateSniffSlide(s SniffSlide) (image.Image, error) {
	if err := loadV2Fonts(); err != nil {
		return nil, err
	}
	v2FaceMu.Lock()
	defer v2FaceMu.Unlock()

	const W, H, margin = 1920, 1080, 120.0
	cols, ok := slideColours[s.Brand]
	if !ok {
		cols = slideColours["sreday"]
	}
	bg, ink := cols[0], cols[1]
	dc := gg.NewContext(W, H)
	dc.SetColor(bg)
	dc.Clear()

	// The QR on a white tile, right, vertically centred above the link.
	tile, pad := 700.0, 84.0
	tileX, tileY := W-margin-tile, (H-tile)/2-40
	qrImg, err := SniffQR(s.URL, int(tile-2*pad), SniffQROpts{Bare: true})
	if err != nil {
		return nil, err
	}
	dc.SetColor(white)
	dc.DrawRoundedRectangle(tileX, tileY, tile, tile, 36)
	dc.Fill()
	dc.DrawImage(qrImg, int(tileX+pad), int(tileY+pad))
	if err := drawSniffCorners(dc, tileX+14, tileY+14, tile-28); err != nil { // the logo's corners round the code
		return nil, err
	}
	dc.SetColor(ink)
	short, size := s.Short, 52
	for ; size > 28; size -= 2 {
		dc.SetFontFace(v2Face(true, size))
		if w, _ := dc.MeasureString(short); w <= tile+60 {
			break
		}
	}
	dc.DrawStringAnchored(short, tileX+tile/2, tileY+tile+64, 0.5, 0.5)

	// Left: what to do, big; the talk and the speaker below.
	textW := tileX - margin - 90
	y := 210.0
	dc.SetRGBA(float64(ink.R)/255, float64(ink.G)/255, float64(ink.B)/255, 0.8)
	dc.SetFontFace(v2Face(true, 40))
	dc.DrawString("QUESTIONS & FEEDBACK", margin, y)
	y += 40
	dc.SetColor(ink)
	for _, l := range fitLines(dc, "Scan to ask a question", true, 120, 80, textW, 2) {
		y += l.size * 1.08
		dc.SetFontFace(v2Face(true, int(l.size)))
		dc.DrawString(l.text, margin, y)
	}
	y += 78
	dc.SetFontFace(v2Face(false, 54))
	dc.DrawString("and say how the talk went", margin, y)

	// The talk title and speaker sit at the bottom of the left column,
	// always in full (smaller before ever cut).
	var block []slideLine
	block = append(block, fitLines(dc, strings.TrimSpace(s.Title), true, 46, 26, textW, 3)...)
	if sp := strings.TrimSpace(s.Speaker); sp != "" {
		for _, l := range fitLines(dc, sp, false, 40, 24, textW, 2) {
			l.gap = 14
			block = append(block, l)
			break
		}
	}
	bottom := tileY + tile
	h := 0.0
	for _, l := range block {
		h += l.size*1.18 + l.gap
	}
	y = bottom - h
	for _, l := range block {
		y += l.size*1.18 + l.gap
		dc.SetFontFace(v2Face(l.bold, int(l.size)))
		if l.bold {
			dc.SetColor(ink)
		} else {
			dc.SetRGBA(float64(ink.R)/255, float64(ink.G)/255, float64(ink.B)/255, 0.85)
		}
		dc.DrawString(l.text, margin, y-l.size*0.18)
	}
	return dc.Image(), nil
}

type slideLine struct {
	text string
	size float64
	bold bool
	gap  float64
}

// fitLines wraps text at the biggest size from max down to min that fits in
// lines lines of width; at min it wraps into as many lines as it takes.
func fitLines(dc *gg.Context, text string, bold bool, max, min int, width float64, lines int) []slideLine {
	if text == "" {
		return nil
	}
	for size := max; ; size -= 2 {
		dc.SetFontFace(v2Face(bold, size))
		wrapped := dc.WordWrap(text, width)
		if len(wrapped) <= lines || size <= min {
			out := make([]slideLine, 0, len(wrapped))
			for _, t := range wrapped {
				out = append(out, slideLine{text: t, size: float64(size), bold: bold})
			}
			return out
		}
	}
}
