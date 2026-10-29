package pkg

import (
	"strings"

	"github.com/fogleman/gg"
	"golang.org/x/image/font"
)

// Badge text layout. Name, job title and company are always printed in
// full, never cut short with "...": each one may wrap (long words break
// after their hyphens), and the layout keeps the name as big as it can,
// then the title and company, shrinking only as far as the badge needs.

type fieldRole int

const (
	roleName fieldRole = iota
	roleTitle
	roleCompany
)

// badgeField is one piece of text to lay out.
type badgeField struct {
	text  string
	role  fieldRole
	bold  bool
	alpha float64
}

// badgeLine is one laid-out line of text.
type badgeLine struct {
	text  string
	bold  bool
	size  int
	alpha float64
	field int // which field (name, title, company) it belongs to
}

// token is a word, or the part of a hyphenated word up to its hyphen
// ("Konstantinopoulou-"), so long names can break there.
type token struct {
	text  string
	space bool // a space follows
}

func tokenize(s string) []token {
	var out []token
	for _, w := range strings.Fields(s) {
		var parts []string
		for _, p := range strings.SplitAfter(w, "-") {
			if p != "" {
				parts = append(parts, p)
			}
		}
		for i, p := range parts {
			out = append(out, token{p, i == len(parts)-1})
		}
	}
	return out
}

func joinTokens(ts []token) string {
	var b strings.Builder
	for i, t := range ts {
		b.WriteString(t.text)
		if t.space && i < len(ts)-1 {
			b.WriteByte(' ')
		}
	}
	return b.String()
}

// wrapText splits text into the fewest lines (up to maxLines) that fit
// width, with the lines as even as possible. nil if it can't fit.
func wrapText(dc *gg.Context, text string, bold bool, size int, width float64, maxLines int) []string {
	ts := tokenize(text)
	n := len(ts)
	if n == 0 {
		return nil
	}
	dc.SetFontFace(v2Face(bold, size))
	seg := make([][]float64, n+1) // seg[i][j]: width of tokens i..j-1 on one line
	for i := 0; i < n; i++ {
		seg[i] = make([]float64, n+1)
		for j := i + 1; j <= n; j++ {
			seg[i][j], _ = dc.MeasureString(joinTokens(ts[i:j]))
		}
	}
	// best[m][j]: the narrowest widest line putting the first j tokens on m lines.
	const inf = 1e18
	best := make([][]float64, maxLines+1)
	from := make([][]int, maxLines+1)
	for m := range best {
		best[m] = make([]float64, n+1)
		from[m] = make([]int, n+1)
		for j := range best[m] {
			best[m][j] = inf
		}
	}
	best[0][0] = 0
	for m := 1; m <= maxLines; m++ {
		for j := 1; j <= n; j++ {
			for i := m - 1; i < j; i++ {
				if best[m-1][i] == inf {
					continue
				}
				wd := seg[i][j]
				if best[m-1][i] > wd {
					wd = best[m-1][i]
				}
				if wd < best[m][j] {
					best[m][j], from[m][j] = wd, i
				}
			}
		}
		if best[m][n] <= width {
			lines := make([]string, m)
			for k, j := m, n; k > 0; k-- {
				i := from[k][j]
				lines[k-1] = joinTokens(ts[i:j])
				j = i
			}
			return lines
		}
	}
	return nil
}

// textBox is the text column left of the QR: width wide, the QR's height
// (fitH) to sit beside it, and maxH down to the badge's bottom margin,
// which only the longest names use.
type textBox struct {
	width, fitH, maxH float64
}

// Title and company print at one size on every badge, so a stack of
// badges looks the same; they only shrink for the most crowded ones.
const titleSize, companySize = 40, 46

// layoutPass is one round of the search: the name's sizes (in steps of 4,
// so names come in a few sizes, not every size) and most lines, the
// smallest title tried, and how tall the text may be.
type layoutPass struct {
	nameFrom, nameTo, nameLines int
	minTitle, titleLines        int
	tall                        bool // may use the margin below the QR
}

var layoutPasses = []layoutPass{
	{96, 56, 2, titleSize, 2, false}, // the usual: name on one or two big lines, beside the QR
	{96, 48, 3, 32, 2, false},        // long names: three big lines beat two small ones
	{96, 44, 3, 24, 3, true},         // longer still: into the margin below the QR
	{44, 16, 3, 14, 3, true},         // anything else: smaller until it fits
}

var capRatio = map[bool]float64{}

// capHeight is how tall a capital letter is at size: a line's baseline is
// its top plus this.
func capHeight(bold bool, size int) float64 {
	r, ok := capRatio[bold]
	if !ok {
		b, _ := font.BoundString(v2Face(bold, 100), "H")
		r = float64(-b.Min.Y) / 64 / 100
		capRatio[bold] = r
	}
	return r * float64(size)
}

// nameShare is the most of the QR's height the name may take.
const nameShare = 0.5

// spreadShare is how much of the space left beside the QR goes between the
// fields; the rest stays below the text.
const spreadShare = 0.6

// spread loosens short text: when it's shorter than the QR, part of the
// space left over (spreadShare) goes between the fields (name, title,
// company); the name still starts at the QR's top edge. Lines of one field
// stay together.
func spread(lines []badgeLine, tops []float64, h, fitH float64) []float64 {
	if len(lines) == 0 {
		return tops
	}
	last := lines[len(lines)-1]
	gaps := last.field // fields after the first
	// Tops are where capital letters start; the last line ends on its
	// baseline (descenders hang below, as in print).
	extra := fitH - (tops[len(tops)-1] + capHeight(last.bold, last.size))
	extra *= spreadShare
	if gaps == 0 || extra <= 0 {
		return tops
	}
	out := make([]float64, len(tops))
	for i, l := range lines {
		out[i] = tops[i] + extra*float64(l.field)/float64(gaps)
	}
	return out
}

// layoutText lays the fields out in box, top down: the lines, where each
// line's capital letters start, and the text's height.
func layoutText(dc *gg.Context, fields []badgeField, box textBox) ([]badgeLine, []float64, float64) {
	try := func(nameSize, nameLines, title, titleLines int, maxH float64) ([]badgeLine, []float64, float64, bool) {
		var lines []badgeLine
		var tops []float64
		y := 0.0
		for fi, f := range fields {
			size, max := title, titleLines
			switch f.role {
			case roleName:
				size, max = nameSize, nameLines
			case roleCompany:
				size, max = title+companySize-titleSize, 2
			}
			wrapped := wrapText(dc, f.text, f.bold, size, box.width, max)
			if wrapped == nil {
				return nil, nil, 0, false
			}
			if fi > 0 {
				gap := 0.30 // title to company
				if fields[fi-1].role == roleName {
					gap = 0.45 // a little more under the name
				}
				y += float64(size) * gap
			}
			for _, t := range wrapped {
				tops = append(tops, y)
				lines = append(lines, badgeLine{t, f.bold, size, f.alpha, fi})
				y += float64(size) * 1.1
			}
			// The name takes at most half the QR's height (top edge to
			// bottom edge); longer names get smaller instead.
			if f.role == roleName && y-float64(size)*0.1 > box.fitH*nameShare {
				return nil, nil, 0, false
			}
		}
		return lines, tops, y, y <= maxH
	}
	for _, p := range layoutPasses {
		maxH := box.fitH
		if p.tall {
			maxH = box.maxH
		}
		for ns := p.nameFrom; ns >= p.nameTo; ns -= 4 {
			for ts := titleSize; ts >= p.minTitle; ts -= 2 {
				if lines, tops, h, ok := try(ns, p.nameLines, ts, p.titleLines, maxH); ok {
					return lines, spread(lines, tops, h, box.fitH), h
				}
			}
		}
	}
	// Can't happen with real names; print it small rather than cut it.
	lines, tops, h, _ := try(12, 3, 12, 3, box.maxH)
	return lines, tops, h
}
