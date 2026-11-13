package snap

import "regexp"

// Sister brands and initiatives (Marek 2026-11-13: "Make Snap recognize our
// sister brands and initiatives: CFP.ninja, certified.fyi, reliaburger,
// badger, and tag accordingly"). They aren't conferences, so a thread about
// one of them has no event; it carries the brand as its tag instead, and
// the brand gets its own colour behind the picked row.

var sisterBrands = []struct {
	re    *regexp.Regexp
	name  string
	color string
}{
	{regexp.MustCompile(`(?i)\bcfp[ .-]?ninja\b`), "CFP.ninja", "#0d9488"},
	{regexp.MustCompile(`(?i)\bcertified[ .-]?fyi\b`), "certified.fyi", "#ca8a04"},
	{regexp.MustCompile(`(?i)\brelia[ -]?burger\b`), "Reliaburger", "#b4532f"},
	{regexp.MustCompile(`(?i)\bbadger\b`), "Badger", "#334155"},
}

// brandOf: the sister brand a thread is about ("" if none), looked for in
// what Claude wrote first, then in the subject.
func brandOf(t Thread) string {
	for _, text := range []string{t.Event, t.Label + " " + t.Topic, t.Subject, t.Why + " " + t.Summary} {
		for _, b := range sisterBrands {
			if b.re.MatchString(text) {
				return b.name
			}
		}
	}
	return ""
}

// tagOf is the pill on a card: its conference, or the sister brand when the
// thread is about one of those.
func tagOf(t Thread) string {
	if c := confName(t.Event); c != "" {
		return c
	}
	return brandOf(t)
}

// The sister brands are spelled right everywhere too (fixBrands, rowline.go).
func init() {
	for _, b := range sisterBrands {
		brandFixes = append(brandFixes, struct {
			re   *regexp.Regexp
			want string
		}{b.re, b.name})
	}
}
