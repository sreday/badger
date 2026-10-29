package pkg

import (
	"strings"

	"github.com/fogleman/gg"
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

// layoutPass is one round of the search: the name's sizes and most lines,
// and the smallest title and company tried with it.
type layoutPass struct {
	nameFrom, nameTo, nameLines int
	titleLines, minTitle        int
}

var layoutPasses = []layoutPass{
	{84, 52, 2, 2, 26}, // the usual: name on one or two big lines
	{84, 40, 3, 3, 22}, // long names: up to three lines
	{40, 14, 3, 3, 14}, // anything else: smaller until it fits
}

// layoutText lays the fields out in a column width wide and at most maxH
// tall: the lines, each line's top, and the column's height.
func layoutText(dc *gg.Context, fields []badgeField, width, maxH float64) ([]badgeLine, []float64, float64) {
	try := func(nameSize, nameLines, titleSize, titleLines int) ([]badgeLine, []float64, float64, bool) {
		var lines []badgeLine
		var tops []float64
		y := 0.0
		for fi, f := range fields {
			size, max := titleSize, titleLines
			switch f.role {
			case roleName:
				size, max = nameSize, nameLines
			case roleCompany:
				size, max = titleSize+6, 2
			}
			wrapped := wrapText(dc, f.text, f.bold, size, width, max)
			if wrapped == nil {
				return nil, nil, 0, false
			}
			if fi > 0 {
				y += float64(size) * 0.32
			}
			for _, t := range wrapped {
				tops = append(tops, y)
				lines = append(lines, badgeLine{t, f.bold, size, f.alpha})
				y += float64(size) * 1.1
			}
		}
		return lines, tops, y, y <= maxH
	}
	for _, p := range layoutPasses {
		for ns := p.nameFrom; ns >= p.nameTo; ns -= 2 {
			for ts := 42; ts >= p.minTitle; ts -= 2 {
				if lines, tops, h, ok := try(ns, p.nameLines, ts, p.titleLines); ok {
					return lines, tops, h
				}
			}
		}
	}
	// Can't happen with real names; print it small rather than cut it.
	lines, tops, h, _ := try(12, 3, 12, 3)
	return lines, tops, h
}
