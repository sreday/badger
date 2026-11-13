package snap

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Tuning (Marek 2026-11-13): "claude reads logs from Other and Quick
// question to fine tune the way the tool works (if I keep pressing Other
// then the suggestions miss the beat, we need claude to figure out why)".
//
// Snap logs every pick on a card: one of Claude's three options, Other (with
// what Marek typed instead), a snooze, a dead end, a quick question, an
// enrich. When new misses came in (Other, questions), the hourly queue
// carries a tuning job, like the style job: Claude works out WHY each one
// missed and rewrites a short list of standing rules. Those rules lead every
// later read's instructions. Marek sees them in the Claude menu and can
// delete one he disagrees with; a deleted rule doesn't come back.

const (
	keepPicks   = 200
	maxRules    = 25
	maxReasons  = 12
	briefsShown = 15 // raw briefs in the instructions, under the rules
)

// Pick is one choice Marek made on a card.
type Pick struct {
	At       time.Time `json:"at"`
	Kind     string    `json:"kind"`             // option, other, snooze, deadend, question, enrich
	Option   int       `json:"option,omitempty"` // 1-3 for "option"
	Thread   string    `json:"thread,omitempty"`
	Subject  string    `json:"subject,omitempty"`
	Company  string    `json:"company,omitempty"`
	Category string    `json:"category,omitempty"`
	Headline string    `json:"headline,omitempty"`
	Offered  []string  `json:"offered,omitempty"`
	Text     string    `json:"text,omitempty"` // what he typed (other, question)
}

// miss: a pick that says the options or the card weren't enough.
func (p Pick) miss() bool { return p.Kind == "other" || p.Kind == "question" }

// Tuning is what Claude worked out from the misses.
type Tuning struct {
	Rules   []string  `json:"rules"`             // standing rules, strongest first
	Reasons []string  `json:"reasons,omitempty"` // why the latest misses missed
	Deleted []string  `json:"deleted,omitempty"` // rules Marek removed
	Seen    time.Time `json:"seen"`              // misses up to here are digested
	At      time.Time `json:"at"`
	From    int       `json:"from"` // misses digested so far
}

func (s *Server) loadTuning() {
	if data, err := os.ReadFile(filepath.Join(s.dir, "picks.json")); err == nil {
		json.Unmarshal(data, &s.picks)
	}
	if data, err := os.ReadFile(filepath.Join(s.dir, "tuning.json")); err == nil {
		json.Unmarshal(data, &s.tuning)
	}
}

// recordPick logs one choice; opt is 1-3 for one of Claude's options.
func (s *Server) recordPick(kind, text string, t *Thread, opt ...int) {
	p := Pick{At: s.now(), Kind: kind, Text: clip(text, 600)}
	if len(opt) > 0 {
		p.Option = opt[0]
	}
	if t != nil {
		p.Thread, p.Subject, p.Company, p.Category, p.Headline = t.ID, t.Subject, t.Company, t.Category, t.Label
		for _, a := range t.Actions {
			p.Offered = append(p.Offered, a.Title)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.picks = append(s.picks, p)
	if len(s.picks) > keepPicks {
		s.picks = s.picks[len(s.picks)-keepPicks:]
	}
	writeJSON(s.dir, "picks.json", s.picks)
}

// othersOn: how many times Marek typed his own brief on this thread.
func (s *Server) othersOn(thread string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, p := range s.picks {
		if p.Thread == thread && p.Kind == "other" {
			n++
		}
	}
	return n
}

// hitRate: how often the options were enough, lately.
func hitRate(picks []Pick, last int) (others, total int, perTab map[string]int) {
	perTab = map[string]int{}
	for i := len(picks) - 1; i >= 0 && total < last; i-- {
		p := picks[i]
		if p.Kind != "option" && p.Kind != "other" && p.Kind != "snooze" && p.Kind != "deadend" {
			continue
		}
		total++
		if p.Kind == "other" {
			others++
			perTab[p.Category]++
		}
	}
	return
}

// TuneJob is the queue's request to work out the misses (nil when there's
// nothing new).
type TuneJob struct {
	Task    string     `json:"task"`
	Rules   []string   `json:"rules"`
	Deleted []string   `json:"deleted_by_mark,omitempty"`
	HitRate string     `json:"hit_rate"`
	Misses  []TuneMiss `json:"misses"`
}

// TuneMiss is one miss as the routine sees it.
type TuneMiss struct {
	At       string   `json:"at"`
	Kind     string   `json:"kind"` // "other": he wrote his own brief; "question": he had to ask
	Tab      string   `json:"tab,omitempty"`
	Company  string   `json:"company,omitempty"`
	Subject  string   `json:"subject,omitempty"`
	Headline string   `json:"your_headline,omitempty"`
	Offered  []string `json:"you_offered,omitempty"`
	Wrote    string   `json:"mark_wrote"`
}

const tuneTask = `Work out why your options missed. "misses" are the times Mark did not take one of your three options on a card. kind "other": he typed his own brief for the reply instead ("mark_wrote"), having seen "you_offered". kind "question": the card didn't tell him enough and he had to ask ("mark_wrote" is his question). For EACH miss, find the real reason, comparing what you offered with what he wanted: the wrong play (you nudged, he wanted to close; you pitched, he wanted to ask), the wrong event, city or date, too soft or too pushy, too generic, a fact that was in his mail and not on the card (something already sent, a price, a person), the wrong person addressed, the wrong reading of who has the ball, a card that should not have been flagged at all, or options that were three flavours of one idea. Be specific and honest; "he prefers his own words" is not a reason.
Return "tuning": {"reasons": [one line per miss: "<company or subject>: <what you offered> missed because <reason>; he wanted <what>"], "rules": [the FULL updated list of standing rules for your future reads, not only the new ones], "learnt": <number of misses you read>}.
Rules: keep the ones in "rules" that still hold, sharpen those the new misses confirm, fix or drop any they contradict, add new ones. At most 25, strongest first, each one short, concrete and checkable when you write a card ("Sponsors who went quiet after the prospectus: first option asks for a 15-minute call with a date, not another nudge"; "Before 'ball with Mark', check OTHER THREADS for a reply he already sent"; "Hosts: never offer a paid package first"). A rule must change what you write next time; no platitudes. Never bring back a rule listed in "deleted_by_mark": he removed it. Don't put private details (prices, personal matters) in a rule; company names are fine when the rule is about that company.`

// tuningJob: the misses not worked out yet.
func (s *Server) tuningJob() *TuneJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := &TuneJob{Task: tuneTask, Rules: append([]string{}, s.tuning.Rules...), Deleted: s.tuning.Deleted}
	upTo := s.tuning.Seen
	for _, p := range s.picks {
		if !p.miss() || !p.At.After(s.tuning.Seen) {
			continue
		}
		j.Misses = append(j.Misses, TuneMiss{At: p.At.In(s.tz).Format("2 Jan 15:04"), Kind: p.Kind, Tab: p.Category, Company: p.Company,
			Subject: clip(p.Subject, 100), Headline: p.Headline, Offered: p.Offered, Wrote: p.Text})
		if p.At.After(upTo) {
			upTo = p.At
		}
	}
	if len(j.Misses) == 0 {
		return nil
	}
	others, total, _ := hitRate(s.picks, 40)
	j.HitRate = fmt.Sprintf("Of Mark's last %d picks on cards, %d were his own brief instead of one of your options.", total, others)
	s.tuneUpTo = upTo
	return j
}

// TuneAnswer is the routine's diagnosis and the rewritten rules.
type TuneAnswer struct {
	Reasons []string `json:"reasons"`
	Rules   []string `json:"rules"`
	Learnt  int      `json:"learnt"`
}

func cleanLines(in []string, max, width int, drop map[string]bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range in {
		n = clip(strings.TrimSpace(n), width)
		k := strings.ToLower(n)
		if n == "" || seen[k] || drop[k] || len(out) >= max {
			continue
		}
		seen[k] = true
		out = append(out, n)
	}
	return out
}

func (s *Server) applyTuning(a *TuneAnswer) int {
	if a == nil || (len(a.Rules) == 0 && len(a.Reasons) == 0) {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tuneUpTo.IsZero() || !s.tuneUpTo.After(s.tuning.Seen) {
		return 0 // no job was out: nothing to digest
	}
	n := 0
	for _, p := range s.picks {
		if p.miss() && p.At.After(s.tuning.Seen) && !p.At.After(s.tuneUpTo) {
			n++
		}
	}
	drop := map[string]bool{}
	for _, d := range s.tuning.Deleted {
		drop[strings.ToLower(d)] = true
	}
	if rules := cleanLines(a.Rules, maxRules, 260, drop); len(rules) > 0 {
		s.tuning.Rules = rules
	}
	if reasons := cleanLines(a.Reasons, maxReasons, 320, nil); len(reasons) > 0 {
		s.tuning.Reasons = reasons
	}
	s.tuning.Seen, s.tuning.At = s.tuneUpTo, s.now()
	s.tuning.From += n
	writeJSON(s.dir, "tuning.json", s.tuning)
	return n
}

// addRule keeps one more standing rule (a lesson from "Enrich the thread").
func (s *Server) addRule(rule string) {
	rule = clip(strings.TrimSpace(rule), 260)
	if rule == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range append(append([]string{}, s.tuning.Rules...), s.tuning.Deleted...) {
		if strings.EqualFold(r, rule) {
			return
		}
	}
	s.tuning.Rules = append(s.tuning.Rules, rule)
	if len(s.tuning.Rules) > maxRules {
		s.tuning.Rules = s.tuning.Rules[len(s.tuning.Rules)-maxRules:]
	}
	s.tuning.At = s.now()
	writeJSON(s.dir, "tuning.json", s.tuning)
}

// tuningBlock leads the instructions' extras: the rules Claude wrote for
// itself from Mark's misses.
func (s *Server) tuningBlock() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.tuning.Rules) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nWHAT YOU GOT WRONG BEFORE, AND THE RULES THAT CAME OUT OF IT. Mark passed over your options or had to ask; these are the rules you wrote for yourself from those misses. Apply every one that fits a card before you write it; they beat the general guidance above.\n")
	for _, r := range s.tuning.Rules {
		b.WriteString("- " + r + "\n")
	}
	return b.String()
}

// TuneView is the Claude menu's corner on tuning.
type TuneView struct {
	Line    string // "Other used 7 of the last 20 picks (Sponsors 5, Events 2)"
	Rules   []string
	Reasons []string
	At      time.Time
	Waiting int // misses Claude hasn't worked out yet
}

func (s *Server) tuneView() TuneView {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := TuneView{Rules: s.tuning.Rules, Reasons: s.tuning.Reasons, At: s.tuning.At}
	for _, p := range s.picks {
		if p.miss() && p.At.After(s.tuning.Seen) {
			v.Waiting++
		}
	}
	others, total, per := hitRate(s.picks, 20)
	if total > 0 {
		v.Line = fmt.Sprintf("Other used %d of the last %d picks", others, total)
		var parts []string
		for _, c := range categories {
			if per[c.Key] > 0 {
				parts = append(parts, fmt.Sprintf("%s %d", c.Name, per[c.Key]))
			}
		}
		sort.Strings(parts)
		if len(parts) > 0 {
			v.Line += " (" + strings.Join(parts, ", ") + ")"
		}
	}
	return v
}

// handleForgetRule: POST /admin/tuning/forget (i): Marek deletes a rule; it
// stays deleted.
func (s *Server) handleForgetRule(w http.ResponseWriter, r *http.Request) {
	i, err := strconv.Atoi(r.FormValue("i"))
	s.mu.Lock()
	if err != nil || i < 0 || i >= len(s.tuning.Rules) || (r.FormValue("rule") != "" && r.FormValue("rule") != s.tuning.Rules[i]) {
		s.mu.Unlock()
		http.Error(w, "No such rule", http.StatusBadRequest)
		return
	}
	s.tuning.Deleted = append(s.tuning.Deleted, s.tuning.Rules[i])
	if len(s.tuning.Deleted) > 60 {
		s.tuning.Deleted = s.tuning.Deleted[len(s.tuning.Deleted)-60:]
	}
	s.tuning.Rules = append(s.tuning.Rules[:i:i], s.tuning.Rules[i+1:]...)
	writeJSON(s.dir, "tuning.json", s.tuning)
	s.mu.Unlock()
	back(w, r)
}
