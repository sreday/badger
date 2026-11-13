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
