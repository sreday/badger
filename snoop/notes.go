package snoop

import (
	"html"
	"regexp"
	"strings"
)

var (
	meetCodeRe = regexp.MustCompile(`meet\.google\.com/([a-z]{3}-[a-z]{4}-[a-z]{3})`)
	docURLRe   = regexp.MustCompile(`docs\.google\.com/document/d/([A-Za-z0-9_-]+)`)

	styleRe  = regexp.MustCompile(`(?is)<(style|script|head)\b.*?</(style|script|head)>`)
	blockRe  = regexp.MustCompile(`(?i)<br\s*/?>|</(p|div|h[1-6]|li|tr|table|ul|ol)>`)
	tagRe    = regexp.MustCompile(`<[^>]*>`)
	spaceRe  = regexp.MustCompile(`[ \t\x{a0}]+`)
	bulletRe = regexp.MustCompile(`^[\s\x{2022}\x{25CF}\x{25E6}\x{2610}\x{2611}\x{2612}\x{25A1}\-\*]+`)
)

// htmlLines turns exported Google Doc HTML into its non-empty text lines,
// one per paragraph, heading or list item.
func htmlLines(doc string) []string {
	doc = styleRe.ReplaceAllString(doc, "")
	doc = blockRe.ReplaceAllString(doc, "\n")
	doc = tagRe.ReplaceAllString(doc, "")
	doc = html.UnescapeString(doc)
	var out []string
	for _, l := range strings.Split(doc, "\n") {
		l = strings.TrimSpace(spaceRe.ReplaceAllString(l, " "))
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// Section headings in "Notes by Gemini" docs.
const (
	secSummary = "summary"
	secDetails = "details"
	secSteps   = "next steps"
	secEnd     = "end"
)

func geminiHeading(l string) string {
	k := strings.ToLower(strings.TrimRight(l, ": "))
	switch {
	case k == "summary":
		return secSummary
	case k == "details":
		return secDetails
	case k == "suggested next steps" || k == "next steps" || k == "action items":
		return secSteps
	case k == "transcript",
		strings.HasPrefix(k, "you should review gemini"),
		strings.HasPrefix(k, "want to see more"),
		strings.HasPrefix(k, "tip: you can always access"),
		strings.HasPrefix(k, "get tips and learn how gemini"),
		strings.HasPrefix(k, "please provide feedback about using gemini"),
		strings.HasPrefix(k, "how is the quality of these specific notes"):
		return secEnd
	}
	return ""
}

// ParseGeminiNotes pulls the Summary and Next steps out of an exported
// "Notes by Gemini" doc. The Details section is long and repeats the
// transcript, so it's left in the original doc (linked from Snoop's).
func ParseGeminiNotes(doc string) Notes {
	n := Notes{By: "Gemini in Meet"}
	var summary []string
	sec := ""
	for _, l := range htmlLines(doc) {
		if h := geminiHeading(l); h != "" {
			sec = h
			continue
		}
		switch sec {
		case secSummary:
			summary = append(summary, l)
		case secSteps:
			// Gemini writes this line instead of an empty list.
			if step := strings.TrimSpace(bulletRe.ReplaceAllString(l, "")); step != "" && !strings.HasPrefix(strings.ToLower(step), "no suggested next steps") {
				n.NextSteps = append(n.NextSteps, step)
			}
		}
	}
	n.Summary = strings.Join(summary, "\n\n")
	return n
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
