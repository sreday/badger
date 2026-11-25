package snort

import (
	"image"
	"math"
	"regexp"
	"sort"
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

// --- Conf42: the tag in the name ----------------------------------------------

// Conf42 headshots are tagged with their topic in the file name, as
// conf42.com/headshots makes them: "Full Name_obs.png". The tag decides.
var conf42TagRe = regexp.MustCompile(`^(.+)_([a-z0-9]+)\.png$`)

// conf42Tag is the topic tag of a Conf42 headshot name ("" when untagged).
func conf42Tag(name string) string {
	if m := conf42TagRe.FindStringSubmatch(name); m != nil {
		return m[2]
	}
	return ""
}
