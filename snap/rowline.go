package snap

import (
	"regexp"
	"strings"
)

// The list's third line (Marek 2026-11-13): the conference and who has the
// ball, nothing else. "Prompt Engineering Conference" is "PEC"; a ball
// between two people is "Aleksandra <> Raymond", first names only.

var (
	pecName   = regexp.MustCompile(`(?i)(the\s+)?prompt engineering conference(\s*\(PEC\))?`)
	betweenBy = regexp.MustCompile(`(?i)\s*(?:,|&|\+|<>|\band\b|\bvs\.?\b|\bwith\b)\s*`)
	notName   = regexp.MustCompile(`\(.*?\)|@\S+`)
)

// confName is the event as the list shows it.
func confName(event string) string {
	return strings.Join(strings.Fields(pecName.ReplaceAllString(event, "PEC")), " ")
}

// betweenShort turns "Aleksandra (SREday) and Raymond Smith at Kyndryl" into
// "Aleksandra <> Raymond".
func betweenShort(s string) string {
	var names []string
	for _, part := range betweenBy.Split(notName.ReplaceAllString(s, " "), -1) {
		f := strings.Fields(part)
		if len(f) == 0 {
			continue
		}
		if strings.EqualFold(f[0], "the") && len(f) > 1 {
			f = f[1:]
		}
		names = append(names, strings.Trim(f[0], ".,;:'\""))
		if len(names) == 2 {
			break
		}
	}
	return strings.Join(names, " <> ")
}

// ball is the pill: its colour class and its words. Yellow when it's with
// Mark, green with them, blue between others.
func ball(t Thread) (class, text string) {
	switch t.Waiting {
	case "you":
		return "you", "Ball in your park"
	case "them":
		return "them", "Ball in their park"
	case "between":
		if b := betweenShort(t.Between); b != "" {
			return "between", b
		}
		return "between", "Between others"
	}
	return "", ""
}

// Brand spelling (Marek 2026-11-13): SREday, LLMday, PLATFORMday, Conf42,
// Prompt Engineering Conference / PEC, however Claude or the mail wrote
// them. Domains and addresses (sreday.com, mark@conf42.com) are left alone.
var brandFixes = []struct {
	re   *regexp.Regexp
	want string
}{
	{regexp.MustCompile(`(?i)\bsre[ -]?days?\b`), "SREday"},
	{regexp.MustCompile(`(?i)\bllm[ -]?days?\b`), "LLMday"},
	{regexp.MustCompile(`(?i)\bplatform[ -]?days?\b`), "PLATFORMday"},
	{regexp.MustCompile(`(?i)\bconf[ -]?42\b`), "Conf42"},
	{regexp.MustCompile(`(?i)\bprompt[ -]engineering conference\b`), "Prompt Engineering Conference"},
	{regexp.MustCompile(`(?i)\bpec\b`), "PEC"},
}

func fixBrands(s string) string {
	if s == "" {
		return s
	}
	for _, f := range brandFixes {
		locs := f.re.FindAllStringIndex(s, -1)
		for i := len(locs) - 1; i >= 0; i-- {
			a, b := locs[i][0], locs[i][1]
			if a > 0 && strings.ContainsRune("@./-_", rune(s[a-1])) {
				continue // part of an address or a link
			}
			if b < len(s)-1 && (s[b] == '.' || s[b] == '@') && isWordByte(s[b+1]) {
				continue // sreday.com
			}
			if s[a:b] != f.want {
				s = s[:a] + f.want + s[b:]
			}
		}
	}
	return s
}

func isWordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// fixCard spells the brands right in everything a card shows.
func fixCard(t *Thread) {
	t.Label, t.Why, t.Summary, t.Event = fixBrands(t.Label), fixBrands(t.Why), fixBrands(t.Summary), fixBrands(t.Event)
	t.CancelEvent, t.StillListed = fixBrands(t.CancelEvent), fixBrands(t.StillListed)
	if len(t.Actions) > 0 {
		acts := make([]Action, len(t.Actions))
		for i, a := range t.Actions {
			a.Title, a.Prompt = fixBrands(a.Title), fixBrands(a.Prompt)
			acts[i] = a
		}
		t.Actions = acts
	}
	if len(t.Context) > 0 {
		ctx := make([]string, len(t.Context))
		for i, c := range t.Context {
			ctx[i] = fixBrands(c)
		}
		t.Context = ctx
	}
}
