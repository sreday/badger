package snoop

import (
	"net/url"
	"regexp"
	"strings"
	"time"
)

// The call page (Marek 2026-11-16): the steps sit on two sides, ours and the
// guest's, and the transcript is shown proofread.

// Fix is one correction to the transcript: a word or name speech-to-text
// got wrong, and what was really said.
type Fix struct {
	Wrong string `json:"wrong"`
	Right string `json:"right"`
}

const maxFixes = 60

// Correction is Marek setting the notes straight.
type Correction struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// Link is one useful link under the TLDR.
type Link struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// cleanLinks keeps up to 3 real web links.
func cleanLinks(in []Link) []Link {
	var out []Link
	for _, l := range in {
		l.Title, l.URL = strings.TrimSpace(l.Title), strings.TrimSpace(l.URL)
		u, err := url.Parse(l.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || len(l.URL) > 300 || len(out) == 3 {
			continue
		}
		if l.Title == "" {
			l.Title = strings.TrimPrefix(u.Host, "www.")
		}
		out = append(out, Link{truncate(l.Title, 40), l.URL})
	}
	return out
}

// cleanFixes keeps the corrections that are safe to apply everywhere.
func cleanFixes(in []Fix) []Fix {
	var out []Fix
	seen := map[string]bool{}
	for _, f := range in {
		f.Wrong, f.Right = strings.TrimSpace(f.Wrong), strings.TrimSpace(f.Right)
		k := strings.ToLower(f.Wrong)
		if len([]rune(f.Wrong)) < 3 || len(f.Wrong) > 80 || len(f.Right) > 80 || f.Right == "" || f.Wrong == f.Right || seen[k] || len(out) == maxFixes {
			continue
		}
		seen[k] = true
		out = append(out, f)
	}
	return out
}

// proofer applies a call's fixes to a piece of text, whole words only.
func (c *Call) proofer() func(string) string {
	type rule struct {
		re    *regexp.Regexp
		right string
	}
	var rules []rule
	for _, f := range c.Fixes {
		re, err := regexp.Compile(`(?i)(^|[^\pL\pN])` + regexp.QuoteMeta(f.Wrong) + `($|[^\pL\pN])`)
		if err != nil {
			continue
		}
		rules = append(rules, rule{re, f.Right})
	}
	return func(s string) string {
		for _, r := range rules {
			// Twice: two hits sharing one space ("Reliabar Reliabar").
			for i := 0; i < 2; i++ {
				s = r.re.ReplaceAllString(s, "${1}"+strings.ReplaceAll(r.right, "$", "$$")+"${2}")
			}
		}
		return s
	}
}

// Spoken filler: "uh", "umm", "yyy", "eee", "hmm", "mhm" and the like.
var (
	fillerRe   = regexp.MustCompile(`(?i)(^|[^\pL\pN])(?:u+h+m*|u+m+|e+r+m+|h+m{2,}|m+h+m+|m{2,}|y{3,}|e{3,}|a{3,}|a+h{2,}|e+h{2,})($|[^\pL\pN])[,.…]*`)
	gapsRe     = regexp.MustCompile(`\s{2,}`)
	dupPunctRe = regexp.MustCompile(`[,;](?:\s*([,;.!?]))+`)
	leadRe     = regexp.MustCompile(`^[\s,.;:…-]+`)
)

// trimFiller takes the spoken filler out of one line; "" when nothing is left.
func trimFiller(s string) string {
	for i := 0; i < 3; i++ { // fillers in a row share their spaces
		s = fillerRe.ReplaceAllString(s, "${1}${2}")
	}
	// "I I I think", "the the": keep one.
	words := strings.Fields(s)
	out := words[:0]
	for _, w := range words {
		if n := len(out); n > 0 && strings.EqualFold(strings.Trim(out[n-1], ","), strings.Trim(w, ",")) && len([]rune(w)) <= 4 {
			out[n-1] = w
			continue
		}
		out = append(out, w)
	}
	s = strings.Join(out, " ")
	s = strings.ReplaceAll(strings.ReplaceAll(gapsRe.ReplaceAllString(s, " "), " ,", ","), " .", ".")
	s = dupPunctRe.ReplaceAllString(s, "$1")
	s = strings.TrimSpace(leadRe.ReplaceAllString(s, ""))
	if strings.IndexFunc(s, func(r rune) bool { return r >= 'A' && r <= 'z' || r > 127 || r >= '0' && r <= '9' }) < 0 {
		return ""
	}
	return s
}

// ProofLines: the transcript as Snoop shows it (Marek 2026-11-16: "trim the
// fat of the conversation... there's no need to have it in snoop"): Claude's
// corrections applied, the lines Claude cut left out ("gimme a minute", "can
// you hear me"), the spoken filler gone. The stored lines stay as Meet wrote
// them, and Meet's own transcript doc is linked from the page.
func (c *Call) ProofLines() []Line {
	lines := c.Lines()
	fix := c.proofer()
	cut := make(map[int]bool, len(c.Cut))
	for _, i := range c.Cut {
		cut[i] = true
	}
	out := make([]Line, 0, len(lines))
	for i, l := range lines {
		if cut[i+1] {
			continue
		}
		text := l.Text
		if re, ok := c.Rewrites[i+1]; ok {
			text = re
		}
		text = trimFiller(fix(text))
		if text == "" {
			continue
		}
		out = append(out, Line{At: l.At, Speaker: fix(l.Speaker), Text: text})
	}
	return out
}

// teamNames: first names that put a step on our side when the notes didn't
// come sorted (Gemini writes "[Mark Pawlikowski, Aleksandra Los] ...").
var teamNames = []string{"mark", "marek", "miko", "anna", "aleksandra", "maxim"}

var ownerRe = regexp.MustCompile(`^\s*(?:\[([^\]]+)\]|([^:]{2,60}):)`)

func teamDomain(email string) bool {
	_, dom, _ := strings.Cut(strings.ToLower(email), "@")
	for _, d := range teamDomains {
		if dom == d {
			return true
		}
	}
	return false
}

// OurStep: is step i of the notes ours (Mark's or the team's) or the guest's?
func (c *Call) OurStep(n *Notes, i int) bool {
	if n.Sided {
		return i < n.Ours
	}
	m := ownerRe.FindStringSubmatch(n.NextSteps[i])
	if m == nil {
		return true // nobody named: ours to sort out
	}
	owner := strings.ToLower(m[1] + m[2])
	if strings.HasPrefix(owner, "push") {
		return true
	}
	names := append([]string{}, teamNames...)
	for _, a := range c.Attendees {
		if first, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(a.Name)), " "); first != "" && teamDomain(a.Email) {
			names = append(names, first)
		}
	}
	for _, w := range strings.FieldsFunc(owner, func(r rune) bool { return r == ' ' || r == ',' || r == '&' || r == '/' || r == '(' || r == ')' }) {
		for _, nm := range names {
			if w == nm {
				return true
			}
		}
	}
	return false
}

// Guest: the first name of the one person from outside the team, for the
// "Jane's side" heading; "" when there are several or none.
func (c *Call) Guest() string {
	name := ""
	for _, a := range c.Attendees {
		if a.Email == "" || teamDomain(a.Email) {
			continue
		}
		if name != "" {
			return ""
		}
		name, _, _ = strings.Cut(strings.TrimSpace(a.Name), " ")
		if name == "" {
			return ""
		}
	}
	return name
}

// Claude is Snoop's filter: it follows the house rules (kept on the server)
// on what stays out of the notes and the transcript. So while Claude is at
// work, a call it hasn't read yet shows no transcript and no automatic notes
// anywhere in Snoop (the page, the Drive doc, search, what Snap gets); they
// wait in Meet's own doc.

const claudeAliveFor = 24 * time.Hour

// claudeAlive: the reading routine has been here lately.
func (s *Server) claudeAlive(now time.Time) bool {
	cs := s.claude
	if cs == nil {
		return false
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.seen.IsZero() { // after a restart: the latest read says when it last came
		cs.seen = time.Unix(1, 0)
		for _, c := range s.store.All() {
			if c.ClaudeAt.After(cs.seen) {
				cs.seen = c.ClaudeAt
			}
		}
	}
	return now.Sub(cs.seen) < claudeAliveFor
}

// held: the call's raw material waits for Claude's first read.
func (s *Server) held(c *Call, now time.Time) bool {
	if c.ClaudeRead != "" || !s.claudeAlive(now) {
		return false
	}
	if c.LeftOpen() {
		return true // never read, so never filtered: its hours of captions aren't shown either
	}
	_, _, due := s.claudeDue(c, now)
	return due
}

// shown is the call as Snoop may show it or hand it on.
func (s *Server) shown(c Call, now time.Time) Call {
	if s.held(&c, now) {
		c.APILines, c.ExtLines, c.APINotes, c.ExtNotes = nil, nil, nil, nil
	}
	return c
}
