package snort

import (
	"image"
	"image/color"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// The headshot rules of the sites' import (import-headshots.yml): a real
// PNG with see-through corners, a safe file name, at most 5 MB.
const maxHeadshot = 5 << 20

// safeName is a headshot file name the sites accept: "Full Name.png",
// "A & B.png", letters of any language.
var safeName = regexp.MustCompile(`^[\p{L}\p{M}\p{N}][\p{L}\p{M}\p{N} .,&'()_\-]{0,150}\.png$`)

// --- In-person: the brand's circle --------------------------------------------

// circleBrand says which site's circle a 1000x1000 headshot sits on:
// sreday (maroon), llmday (pale yellow-green), platformday (orange), pec
// (rainbow) or "" when it can't tell. Measured on 240 headshots of the four
// sites (2026-11-25): every one sorted right, three faint PEC ones unsure.
func circleBrand(img image.Image) string {
	b := img.Bounds()
	step := b.Dx() / 200
	if step < 1 {
		step = 1
	}
	var n, sre, plat, llm int
	var sats []float64
	for y := b.Min.Y; y < b.Max.Y; y += step {
		for x := b.Min.X; x < b.Max.X; x += step {
			r16, g16, b16, a16 := img.At(x, y).RGBA()
			if a16 < 0xfa00 { // only solid pixels, the corners are see-through
				continue
			}
			// The colours as stored, not premultiplied (the pixel is solid).
			r, g, bl := float64(r16)/65535, float64(g16)/65535, float64(b16)/65535
			mx, mn := math.Max(r, math.Max(g, bl)), math.Min(r, math.Min(g, bl))
			c := mx - mn
			if c <= 0.16 { // the person is grey: only the circle has colour
				continue
			}
			var h float64
			switch mx {
			case r:
				h = math.Mod((g-bl)/c, 6)
			case g:
				h = (bl-r)/c + 2
			default:
				h = (r-g)/c + 4
			}
			h *= 60
			if h < 0 {
				h += 360
			}
			s := c / mx
			n++
			sats = append(sats, s)
			switch {
			case (h >= 335 || h < 22) && s > 0.3:
				sre++
			case h >= 26 && h < 44 && s > 0.7:
				plat++
			case h >= 44 && h < 90 && s < 0.5:
				llm++
			}
		}
	}
	if n < 200 {
		return ""
	}
	sort.Float64s(sats)
	med := sats[len(sats)/2]
	best, brand := 0.0, ""
	for _, c := range []struct {
		v float64
		b string
	}{{float64(sre) / float64(n), "sreday"}, {float64(plat) / float64(n), "platformday"}, {float64(llm) / float64(n), "llmday"}} {
		if c.v > best {
			best, brand = c.v, c.b
		}
	}
	switch {
	case best >= 0.6:
		return brand
	case med >= 0.3 && med <= 0.48 && best < 0.45:
		return "pec"
	}
	return ""
}

// seeThroughCorners: every headshot is a disc on a clear square.
func seeThroughCorners(img image.Image) bool {
	b := img.Bounds()
	for _, p := range []image.Point{{b.Min.X, b.Min.Y}, {b.Max.X - 1, b.Min.Y}, {b.Min.X, b.Max.Y - 1}, {b.Max.X - 1, b.Max.Y - 1}} {
		if _, _, _, a := img.At(p.X, p.Y).RGBA(); a > 0x1000 {
			return false
		}
	}
	return true
}

// --- Conf42: the topic's sky ----------------------------------------------------

// Conf42 headshots are 500x400 on the topic's sky (src/thumbnails/<suffix>.png,
// the clean skies). Several topics share one sky, so the colour alone can't
// name the topic: the file's own _<suffix> does, and the sky must agree.
const skyW, skyH = 500, 400

type skies struct {
	mu    sync.Mutex
	plate map[string][]uint8 // suffix -> 250x200 RGB
}

func shrinkRGB(img image.Image) []uint8 {
	b := img.Bounds()
	out := make([]uint8, 0, 250*200*3)
	for y := 0; y < 200; y++ {
		for x := 0; x < 250; x++ {
			c := color.NRGBAModel.Convert(img.At(b.Min.X+x*b.Dx()/250, b.Min.Y+y*b.Dy()/200)).(color.NRGBA)
			out = append(out, c.R, c.G, c.B)
		}
	}
	return out
}

// skyScore is the share of pixels within 14 of the plate.
func skyScore(a, p []uint8) float64 {
	n := 0
	for i := 0; i+2 < len(a) && i+2 < len(p); i += 3 {
		d := 0
		for k := 0; k < 3; k++ {
			v := int(a[i+k]) - int(p[i+k])
			if v < 0 {
				v = -v
			}
			d = max(d, v)
		}
		if d < 14 {
			n++
		}
	}
	return float64(n) / float64(len(a)/3)
}

// skyFits says whether a headshot sits on the suffix's sky: it must be
// among the best plates (skies shared by topics score the same).
func skyFits(img image.Image, plates map[string][]uint8, suffix string) bool {
	p, ok := plates[suffix]
	if !ok {
		return false
	}
	a := shrinkRGB(img)
	mine := skyScore(a, p)
	if mine < 0.15 {
		return false
	}
	for _, q := range plates {
		if skyScore(a, q) > mine+0.02 {
			return false
		}
	}
	return true
}

// conf42Name is the file name of a Conf42 headshot: the upload's own name
// when it ends in a known _<suffix>, else the name with the event's suffix.
func conf42Name(upload, suffix string, known map[string][]uint8) (name, sfx string) {
	stem := strings.TrimSuffix(upload, ".png")
	if i := strings.LastIndex(stem, "_"); i > 0 {
		if _, ok := known[stem[i+1:]]; ok {
			return upload, stem[i+1:]
		}
	}
	if suffix == "" {
		return "", ""
	}
	return stem + "_" + suffix + ".png", suffix
}
