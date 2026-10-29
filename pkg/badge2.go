package pkg

import (
	"embed"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"strings"
	"sync"

	"github.com/fogleman/gg"
	qrcode "github.com/skip2/go-qrcode"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
)

// The 2.0 badge: Atkinson Hyperlegible (made by the Braille Institute so
// letters can't be mistaken for each other), a QR code almost twice the
// size of the classic one, rounded corners, and no WiFi code.
// GenerateBadge (badge.go) is the classic badge, kept as it was.

//go:embed fonts/*.ttf
var badgeFonts embed.FS

var (
	v2Once              sync.Once
	v2Bold, v2Regular   *opentype.Font
	v2Err               error
	v2FaceMu            sync.Mutex
	v2Faces             = map[v2FaceKey]font.Face{}
	white               = color.White
	badgeCornerRadius   = 30.0 // 3 mm on a 62 mm label: the curve of Brother's die-cut labels
	badgeMargin         = 56.0
	badgeQRSize         = 330
	badgeQRPadding      = 18
	badgeQRCornerRadius = 22.0
)

type v2FaceKey struct {
	bold bool
	size int
}

func loadV2Fonts() error {
	v2Once.Do(func() {
		for _, f := range []struct {
			file string
			dst  **opentype.Font
		}{{"fonts/AtkinsonHyperlegible-Bold.ttf", &v2Bold}, {"fonts/AtkinsonHyperlegible-Regular.ttf", &v2Regular}} {
			data, err := badgeFonts.ReadFile(f.file)
			if err != nil {
				v2Err = err
				return
			}
			if *f.dst, err = opentype.Parse(data); err != nil {
				v2Err = fmt.Errorf("parsing %s: %w", f.file, err)
				return
			}
		}
	})
	return v2Err
}

// v2Face returns a cached font face; faces are only used under v2FaceMu.
func v2Face(bold bool, size int) font.Face {
	k := v2FaceKey{bold, size}
	if f, ok := v2Faces[k]; ok {
		return f
	}
	src := v2Regular
	if bold {
		src = v2Bold
	}
	f, err := opentype.NewFace(src, &opentype.FaceOptions{Size: float64(size), DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		return nil
	}
	v2Faces[k] = f
	return f
}

// badgeLine is one line of text on the badge, already sized.
type badgeLine struct {
	text string
	bold bool
	size int
	rgba [4]float64
}

// fitLine finds the largest size from max down to min at which text fits
// width; at min it shortens the text with an ellipsis.
func fitLine(dc *gg.Context, text string, bold bool, max, min int, width float64) (string, int) {
	for size := max; size >= min; size -= 2 {
		dc.SetFontFace(v2Face(bold, size))
		if w, _ := dc.MeasureString(text); w <= width {
			return text, size
		}
	}
	dc.SetFontFace(v2Face(bold, min))
	r := []rune(text)
	for len(r) > 1 {
		r = r[:len(r)-1]
		t := strings.TrimSpace(string(r)) + "…"
		if w, _ := dc.MeasureString(t); w <= width {
			return t, min
		}
	}
	return text, min
}

// nameLines puts the name on one big line, or two when one line would be
// too small to read across a room.
func nameLines(dc *gg.Context, name string, width float64) []badgeLine {
	const max, oneLineMin, min = 84, 60, 38
	if t, size := fitLine(dc, name, true, max, oneLineMin, width); t == name {
		return []badgeLine{{t, true, size, [4]float64{1, 1, 1, 1}}}
	}
	words := strings.Fields(name)
	if len(words) < 2 {
		t, size := fitLine(dc, name, true, max, min, width)
		return []badgeLine{{t, true, size, [4]float64{1, 1, 1, 1}}}
	}
	// Split where the two lines are closest in length.
	best, bestDiff := 1, 1<<30
	for i := 1; i < len(words); i++ {
		a, b := len(strings.Join(words[:i], " ")), len(strings.Join(words[i:], " "))
		if d := a - b; d*d < bestDiff {
			best, bestDiff = i, d*d
		}
	}
	first, second := strings.Join(words[:best], " "), strings.Join(words[best:], " ")
	_, s1 := fitLine(dc, first, true, max-8, min, width)
	_, s2 := fitLine(dc, second, true, max-8, min, width)
	size := s1
	if s2 < size {
		size = s2
	}
	t1, _ := fitLine(dc, first, true, size, size, width)
	t2, _ := fitLine(dc, second, true, size, size, width)
	return []badgeLine{{t1, true, size, [4]float64{1, 1, 1, 1}}, {t2, true, size, [4]float64{1, 1, 1, 1}}}
}

// GenerateBadgeV2 draws the 2.0 badge (1000x600, the same label as the
// classic one).
func GenerateBadgeV2(data BadgeData) (image.Image, error) {
	if templateImage == nil {
		return nil, fmt.Errorf("badge assets not loaded")
	}
	if err := loadV2Fonts(); err != nil {
		return nil, err
	}
	v2FaceMu.Lock()
	defer v2FaceMu.Unlock()

	b := templateImage.Bounds()
	w, h := float64(b.Dx()), float64(b.Dy())
	dc := gg.NewContextForImage(templateImage)

	tile := float64(badgeQRSize + 2*badgeQRPadding)
	tileX, tileY := w-badgeMargin*0.6-tile, badgeMargin*0.6
	textW := tileX - badgeMargin - 40
	if data.LinkedIn == "" {
		textW = w - 2*badgeMargin
	}

	// Name, company, job title: big, clear, white.
	lines := nameLines(dc, data.Name, textW)
	if data.Company != "" {
		t, size := fitLine(dc, data.Company, true, 46, 30, textW)
		lines = append(lines, badgeLine{t, true, size, [4]float64{1, 1, 1, .92}})
	}
	if data.Title != "" {
		t, size := fitLine(dc, data.Title, false, 38, 26, textW)
		lines = append(lines, badgeLine{t, false, size, [4]float64{1, 1, 1, .85}})
	}
	// Each line's top, then the block centred on the QR tile (or on the
	// space above the skyline when there's no QR).
	tops := make([]float64, len(lines))
	y := 0.0
	for i, l := range lines {
		if i > 0 {
			gap := 0.30
			if lines[i-1].bold && l.bold && lines[i-1].size == l.size {
				gap = 0.08 // the name's second line
			}
			y += float64(lines[i-1].size) * gap
		}
		tops[i] = y
		y += float64(l.size) * 1.08
	}
	top := tileY + (tile-y)/2
	if top < badgeMargin*0.6 {
		top = badgeMargin * 0.6
	}
	for i, l := range lines {
		dc.SetFontFace(v2Face(l.bold, l.size))
		dc.SetRGBA(l.rgba[0], l.rgba[1], l.rgba[2], l.rgba[3])
		dc.DrawStringAnchored(l.text, badgeMargin, top+tops[i], 0, 1)
	}

	// LinkedIn QR, top-right on a white tile: the code sponsors scan.
	if data.LinkedIn != "" {
		url := strings.Replace(data.LinkedIn, "https://www.linkedin.com/", "linkedin://", 1)
		qr, err := qrcode.New(url, qrcode.Low)
		if err != nil {
			return nil, fmt.Errorf("LinkedIn QR: %w", err)
		}
		qr.DisableBorder = true
		dc.SetColor(white)
		dc.DrawRoundedRectangle(tileX, tileY, tile, tile, badgeQRCornerRadius)
		dc.Fill()
		dc.DrawImage(qr.Image(badgeQRSize), int(tileX)+badgeQRPadding, int(tileY)+badgeQRPadding)
	}

	// Rounded badge corners: outside the curve is blank paper.
	img := image.NewRGBA(image.Rect(0, 0, int(w), int(h)))
	draw.Draw(img, img.Bounds(), &image.Uniform{white}, image.Point{}, draw.Src)
	mask := gg.NewContext(int(w), int(h))
	mask.DrawRoundedRectangle(0, 0, w, h, badgeCornerRadius)
	mask.Fill()
	draw.DrawMask(img, img.Bounds(), dc.Image(), image.Point{}, mask.AsMask(), image.Point{}, draw.Over)
	return img, nil
}
