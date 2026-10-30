package pkg

import (
	"embed"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"strconv"
	"strings"
	"sync"

	"github.com/fogleman/gg"
	qrcode "github.com/skip2/go-qrcode"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
)

// The 2.0 badge: plain black, Atkinson Hyperlegible (made by the Braille
// Institute so letters can't be mistaken for each other), a QR code almost
// twice the size of the classic one, rounded corners, and no WiFi code.
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
	badgeQRSize         = 330 // smaller QRs lose scans on long LinkedIn links (tested at 297 and 264)
	badgeQRPadding      = 18
	badgeQRCornerRadius = 22.0
	// Logo width / QR width, and its white ring. With quartile error
	// correction (badgeQRLevel) an 18% logo reads as well as no logo at all
	// (jsQR on 96 camera-like frames per badge: 170-500 px wide, blurred,
	// tilted, washed out by glare); 25% starts to cost reads.
	badgeLogoShare  = 0.18
	badgeEdge       = 2.0  // a hairline of white all around the badge
	badgeLogoPad    = 0.07 // white ring around the logo, share of its width
	badgeWidth      = 1000 // the classic badge's size: same label, same print command
	badgeHeight     = 600
	badgeBackground = color.RGBA{0x00, 0x00, 0x00, 0xff} // the whole background: pure black
	// Quartile (25%) rather than highest (30%) correction: fewer, bigger
	// squares, so the code reads from further away and in worse light (25-35%
	// more camera frames read than highest with a 25% logo, same tests).
	badgeQRLevel = qrcode.High
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

// GenerateBadgeV2 draws the 2.0 badge (1000x600, the same label as the
// classic one).
func GenerateBadgeV2(data BadgeData) (image.Image, error) {
	if err := loadV2Fonts(); err != nil {
		return nil, err
	}
	v2FaceMu.Lock()
	defer v2FaceMu.Unlock()

	dc, tileX, tileY, tile := badgeCanvas()
	w, h := float64(badgeWidth), float64(badgeHeight)

	// QR in the middle right; the name starts level with its top edge, and
	// the text fits beside it (the longest names may use the margin below).
	box := textBox{
		width: tileX - badgeMargin - 40,
		fitH:  tile,
		maxH:  h - tileY - badgeMargin*0.6,
	}
	if data.LinkedIn == "" {
		box.width = w - 2*badgeMargin
	}

	// Name, job title, company: big, clear, white, always in full (see
	// badge2_layout.go). A field Luma didn't have ("403 not found") is left
	// blank.
	var fields []badgeField
	for _, f := range []badgeField{
		{shown(data.Name), roleName, true, 1},
		{shown(data.Title), roleTitle, false, .85},
		{shown(data.Company), roleCompany, true, .92},
	} {
		if strings.TrimSpace(f.text) != "" {
			fields = append(fields, f)
		}
	}
	lines, tops, _ := layoutText(dc, fields, box)
	top := tileY
	for i, l := range lines {
		dc.SetFontFace(v2Face(l.bold, l.size))
		dc.SetRGBA(1, 1, 1, l.alpha)
		dc.DrawString(l.text, badgeMargin, top+tops[i]+capHeight(l.bold, l.size))
	}

	// LinkedIn QR, top-right on a white tile: the code sponsors scan.
	if data.LinkedIn != "" {
		url := strings.Replace(data.LinkedIn, "https://www.linkedin.com/", "linkedin://", 1)
		if err := drawQRTile(dc, url, badgeQRLevel, badgeLogoShare, tileX, tileY, tile, drawLinkedInLogo); err != nil {
			return nil, fmt.Errorf("LinkedIn QR: %w", err)
		}
	}
	return finishBadge(dc), nil
}

// badgeCanvas is a blank black badge, and where its QR tile goes (middle
// right): x, y and size.
func badgeCanvas() (dc *gg.Context, tileX, tileY, tile float64) {
	dc = gg.NewContext(badgeWidth, badgeHeight)
	dc.SetColor(badgeBackground)
	dc.Clear()
	tile = float64(badgeQRSize + 2*badgeQRPadding)
	return dc, float64(badgeWidth) - badgeMargin*0.6 - tile, (float64(badgeHeight) - tile) / 2, tile
}

// drawQRTile draws a QR code for content on a white tile, with a logo in
// the middle (logoShare of the QR's width), at error correction level, so
// the logo costs nothing: the code still reads with it covered. Long content (a long WiFi name and
// password) would make that code too dense to read small or blurred, so
// past version 8 it uses medium correction, which still carries the logo
// (both tested with jsQR down to a 200px-wide badge, sharp and blurred).
func drawQRTile(dc *gg.Context, content string, level qrcode.RecoveryLevel, logoShare, tileX, tileY, tile float64, logo func(dc *gg.Context, cx, cy, size float64, c color.Color)) error {
	qr, err := qrcode.New(content, level)
	if err != nil {
		return err
	}
	if qr.VersionNumber > 8 {
		if qr, err = qrcode.New(content, qrcode.Medium); err != nil {
			return err
		}
	}
	qr.DisableBorder = true
	dc.SetColor(white)
	dc.DrawRoundedRectangle(tileX, tileY, tile, tile, badgeQRCornerRadius)
	dc.Fill()
	dc.DrawImage(qr.Image(badgeQRSize), int(tileX)+badgeQRPadding, int(tileY)+badgeQRPadding)
	logo(dc, tileX+tile/2, tileY+tile/2, float64(badgeQRSize)*logoShare, color.Black)
	return nil
}

// finishBadge adds the thin white margin and the rounded corners: outside
// the black is blank paper.
func finishBadge(dc *gg.Context) image.Image {
	w, h := float64(badgeWidth), float64(badgeHeight)
	img := image.NewRGBA(image.Rect(0, 0, badgeWidth, badgeHeight))
	draw.Draw(img, img.Bounds(), &image.Uniform{white}, image.Point{}, draw.Src)
	mask := gg.NewContext(badgeWidth, badgeHeight)
	mask.DrawRoundedRectangle(badgeEdge, badgeEdge, w-2*badgeEdge, h-2*badgeEdge, badgeCornerRadius-badgeEdge/2)
	mask.Fill()
	draw.DrawMask(img, img.Bounds(), dc.Image(), image.Point{}, mask.AsMask(), image.Point{}, draw.Over)
	return img
}

// linkedInMark is LinkedIn's "in" mark (24x24 SVG path, Simple Icons): the
// blue square with the letters cut out.
const linkedInMark = "M20.447 20.452h-3.554v-5.569c0-1.328-.027-3.037-1.852-3.037-1.853 0-2.136 1.445-2.136 2.939v5.667H9.351V9h3.414v1.561h.046c.477-.9 1.637-1.85 3.37-1.85 3.601 0 4.267 2.37 4.267 4.892v4.851zM5.337 7.433c-1.144 0-2.063-.926-2.063-2.065 0-1.138.92-2.063 2.063-2.063 1.14 0 2.064.925 2.064 2.063 0 1.139-.925 2.065-2.064 2.065zm1.782 13.019H3.555V9h3.564v11.452zM22.225 0H1.771C.792 0 0 .774 0 1.729v20.542C0 23.227.792 24 1.771 24h20.451C23.2 24 24 23.227 24 22.271V1.729C24 .774 23.2 0 22.222 0h.003z"

// shown is a badge field as printed: Luma's "403 not found" placeholder is
// left blank.
func shown(s string) string {
	if strings.EqualFold(strings.TrimSpace(s), "403 not found") {
		return ""
	}
	return s
}

// drawLinkedInLogo draws the LinkedIn mark centred on cx, cy in colour c
// (black), on a thin white ring, so people see what the QR is for.
func drawLinkedInLogo(dc *gg.Context, cx, cy, size float64, c color.Color) {
	pad := size * badgeLogoPad
	dc.SetColor(white)
	dc.DrawRoundedRectangle(cx-size/2-pad, cy-size/2-pad, size+2*pad, size+2*pad, size*0.18)
	dc.Fill()
	dc.Push()
	dc.Translate(cx-size/2, cy-size/2)
	dc.Scale(size/24, size/24)
	drawSVGPath(dc, linkedInMark)
	dc.Pop()
	dc.SetColor(c)
	dc.SetFillRuleEvenOdd()
	dc.Fill()
	dc.SetFillRuleWinding()
}

// drawSVGPath adds an SVG path (M, H, V, L, C, Z, absolute and relative)
// to the context's current path.
func drawSVGPath(dc *gg.Context, d string) {
	var nums []float64
	var cmd byte
	x, y, sx, sy := 0.0, 0.0, 0.0, 0.0
	flush := func() {
		rel := cmd >= 'a'
		ox, oy := 0.0, 0.0
		if rel {
			ox, oy = x, y
		}
		switch cmd | 0x20 { // lower case
		case 'm':
			for i := 0; i+1 < len(nums); i += 2 {
				if rel {
					ox, oy = x, y
				}
				x, y = ox+nums[i], oy+nums[i+1]
				if i == 0 {
					dc.MoveTo(x, y)
					sx, sy = x, y
				} else {
					dc.LineTo(x, y)
				}
			}
		case 'l':
			for i := 0; i+1 < len(nums); i += 2 {
				if rel {
					ox, oy = x, y
				}
				x, y = ox+nums[i], oy+nums[i+1]
				dc.LineTo(x, y)
			}
		case 'h':
			for _, n := range nums {
				if rel {
					x += n
				} else {
					x = n
				}
				dc.LineTo(x, y)
			}
		case 'v':
			for _, n := range nums {
				if rel {
					y += n
				} else {
					y = n
				}
				dc.LineTo(x, y)
			}
		case 'c':
			for i := 0; i+5 < len(nums); i += 6 {
				if rel {
					ox, oy = x, y
				}
				dc.CubicTo(ox+nums[i], oy+nums[i+1], ox+nums[i+2], oy+nums[i+3], ox+nums[i+4], oy+nums[i+5])
				x, y = ox+nums[i+4], oy+nums[i+5]
			}
		case 'z':
			dc.ClosePath()
			x, y = sx, sy
		}
		nums = nums[:0]
	}
	for i := 0; i < len(d); {
		c := d[i]
		switch {
		case strings.IndexByte("MmHhVvLlCcZz", c) >= 0:
			if cmd != 0 {
				flush()
			}
			cmd = c
			i++
		case c == '-' || c == '.' || (c >= '0' && c <= '9'):
			// A number: sign, digits, one dot. "-.027-3.037" is two numbers.
			j, dot := i, false
			if d[j] == '-' {
				j++
			}
			for j < len(d) && (d[j] >= '0' && d[j] <= '9' || d[j] == '.' && !dot) {
				if d[j] == '.' {
					dot = true
				}
				j++
			}
			n, _ := strconv.ParseFloat(d[i:j], 64)
			nums = append(nums, n)
			i = j
		default:
			i++
		}
	}
	if cmd != 0 {
		flush()
	}
}
