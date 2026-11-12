package snoop

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sreday/badger/sites"
)

// Claude reads the calls (Marek 2026-11-12: "for Snap and Snoop, I need
// Claude to be as performant as possible, proposing aggressive solutions as
// well, making sure there's no loose ends at any point").
//
// Same model as Snap, but Snoop's own: a Claude Code routine on Marek's
// subscription fetches GET /api/claude/queue (bearer token in
// DataDir/snoop/claude-token), reads each finished call (transcript,
// Gemini's notes, the invite, the Calendly answers) and posts its notes to
// POST /api/claude/answers. Claude's notes then win over Gemini's and the
// on-device ones: a summary, every commitment and open question as a next
// step with an owner and a date, and a few "Push:" plays to get more out of
// the call. A call is read again when its transcript or notes change.

const (
	claudeBatch  = 4                   // calls per queue: transcripts are long
	claudeWindow = 45 * 24 * time.Hour // calls older than this aren't read any more
	maxInput     = 90000               // characters of one call handed over
)

type claudeState struct {
	token    string
	mu       sync.Mutex
	events   string
	eventsAt time.Time
}

func newClaude(dir string) (*claudeState, error) {
	path := dir + string(os.PathSeparator) + "claude-token"
	if data, err := os.ReadFile(path); err == nil && len(bytes.TrimSpace(data)) >= 32 {
		return &claudeState{token: string(bytes.TrimSpace(data))}, nil
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	tok := hex.EncodeToString(b)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &claudeState{token: tok}, os.WriteFile(path, []byte(tok+"\n"), 0o600)
}

// ClaudeInstructions go out with every queue, so the routine's own prompt
// stays short and a change here ships with Snout.
const ClaudeInstructions = `You read Mark's calls. Mark (mark@sreday.com) organises tech conferences: SREday, LLMday, PLATFORMday, the Prompt Engineering Conference (PEC) and Conf42 (online). His business runs on sponsors (companies paying for packages, booths, leads, keynotes), hosts (companies giving a venue), speakers and a small team (Miko, Anna, Aleksandra, Maxim and others). Each item in "calls" is one finished Google Meet: the invite, the Calendly answers when it was booked as a sales call, any notes Gemini or the browser wrote, the steps Mark already ticked off, and the transcript.

Your notes replace the automatic ones, so they must be the best read of the call Mark can get. Two things matter most:

1. NO LOOSE ENDS, EVER. List every commitment either side made (send the prospectus, check the budget, intro to a colleague, confirm a date, share a deck), every open question nobody answered, every number, date or decision that still needs confirming, and every follow-up email that should go out. Mark's own commitments first: they are what slips. If something was half-agreed, it's a step ("Mark: get Jane's yes on the Gold package in writing").
2. BE AGGRESSIVE ABOUT VALUE. Treat every call as "extract max value". Add 1 to 3 steps that start with "Push:": bold, concrete plays that get more out of this relationship, grounded in what was said. For example: ask for the decision with a date, propose a yearly or multi-event bundle, offer an event near their offices (see UPCOMING EVENTS), a keynote or speaker from their team, hosting an event or meetup, an intro to a peer company, co-marketing, going a level up to whoever signs. Decisive, never pushy or rude. Never invent prices, discounts or packages that weren't mentioned.

Return for EVERY call:
- id and read: copied exactly from the queue.
- summary: 2 to 5 plain sentences: who they are, what they want, what was agreed, money and dates mentioned, where it stands and who has the ball.
- next_steps: the steps, most valuable first. Each one line: owner first ("Mark:", "Anna:", or the other person's first name and company, "Jane (Acme):"), then the concrete action, then a due date ("by Fri 14 Nov", worked out from the call date; if nobody named one, pick a sensible one and write it). Push plays are steps too, starting "Push:". Leave out steps the "Already done" list covers. Internal team calls get steps for whoever owns them; calls with nothing to do get an empty list.
- company_facts: for a sponsor, host or partner call, a quick background check with web search (one or two searches on the company name only): headquarters and offices, rough size, what they sell, recent news. One or two sentences, or "" if not useful. Use it to pick events and plays that fit.

Write plainly, as Mark's sharp chief of staff would: no fluff, no em dashes. Never put transcript text into a web search. Answer with JSON only: {"model": "<your model id>", "calls": [ ... ]}.`

// claudeInput is what Claude reads for one call; the hash says whether it
// changed since Claude's last read (ticks don't count).
func (s *Server) claudeInput(c *Call) (input, hash string) {
	var b strings.Builder
	fmt.Fprintf(&b, "Title: %s\n", c.Title)
	fmt.Fprintf(&b, "When: %s to %s (%s)\n", c.Start.In(s.tz).Format("Mon 2 Jan 2006, 15:04"), c.End.In(s.tz).Format("15:04"), s.tz)
	if c.Organizer != "" {
		fmt.Fprintf(&b, "Organiser: %s\n", c.Organizer)
	}
	if len(c.Attendees) > 0 {
		var who []string
		for _, a := range c.Attendees {
			w := a.Email
			if a.Name != "" {
				w = a.Name + " <" + a.Email + ">"
			}
			if a.Response != "" {
				w += " (" + a.Response + ")"
			}
			who = append(who, w)
		}
		fmt.Fprintf(&b, "Invited: %s\n", strings.Join(who, "; "))
	}
	if c.Calendly {
		fmt.Fprintf(&b, "Booked through Calendly: %s\n", c.CalendlyEvent)
		keys := make([]string, 0, len(c.CalendlyAnswers))
		for k := range c.CalendlyAnswers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "  %s: %s\n", k, c.CalendlyAnswers[k])
		}
	}
	for _, n := range []*Notes{c.APINotes, c.ExtNotes} {
		if n == nil || (n.Summary == "" && len(n.NextSteps) == 0) {
			continue
		}
		fmt.Fprintf(&b, "Notes by %s:\n%s\n", n.By, n.Summary)
		for _, st := range n.NextSteps {
			fmt.Fprintf(&b, "  - %s\n", st)
		}
	}
	if lines := c.Lines(); len(lines) > 0 {
		fmt.Fprintf(&b, "Transcript (%s):\n", c.TranscriptSource())
		for _, l := range lines {
			fmt.Fprintf(&b, "[%s] %s: %s\n", l.At.In(s.tz).Format("15:04"), l.Speaker, l.Text)
		}
	}
	input = b.String()
	if len(input) > maxInput {
		input = input[:maxInput] + "\n[transcript cut here: the call was very long]\n"
	}
	sum := sha256.Sum256([]byte(input))
	hash = hex.EncodeToString(sum[:8])
	if done := c.doneSteps(); len(done) > 0 {
		input += "Already done (Mark ticked these off): " + strings.Join(done, "; ") + "\n"
	}
	return input, hash
}

// doneSteps: the next steps Mark ticked off.
func (c *Call) doneSteps() []string {
	n := c.Notes()
	if n == nil {
		return nil
	}
	var out []string
	for i, st := range n.NextSteps {
		if c.Ticked[i] {
			out = append(out, st)
		}
	}
	return out
}

// claudeDue: a finished call with something to read that Claude hasn't read
// in this form yet.
func (s *Server) claudeDue(c *Call, now time.Time) (string, string, bool) {
	if c.Declined || c.Cancelled || now.Sub(c.End) > claudeWindow || c.Empty(now) {
		return "", "", false
	}
	switch st, _ := c.Status(now); st {
	case StatusDone, StatusTranscript, StatusNoTranscript:
	default:
		return "", "", false // still on, or still waiting for the transcript
	}
	if len(c.Lines()) == 0 && (c.APINotes == nil || c.APINotes.Summary == "") && (c.ExtNotes == nil || c.ExtNotes.Summary == "") {
		return "", "", false
	}
	in, hash := s.claudeInput(c)
	return in, hash, hash != c.ClaudeRead
}

type claudeItem struct {
	ID    string `json:"id"`
	Read  string `json:"read"`
	Input string `json:"input"`
}

func (s *Server) claudeQueue(now time.Time) []claudeItem {
	calls := s.store.All()
	sort.Slice(calls, func(i, j int) bool { return calls[i].End.After(calls[j].End) }) // newest first
	var out []claudeItem
	for i := range calls {
		if in, hash, ok := s.claudeDue(&calls[i], now); ok {
			out = append(out, claudeItem{calls[i].ID, hash, in})
		}
	}
	return out
}

func (s *Server) requireClaude(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if s.claude == nil || subtle.ConstantTimeCompare([]byte(got), []byte(s.claude.token)) != 1 {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// handleClaudeQueue: GET /api/claude/queue.
func (s *Server) handleClaudeQueue(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	all := s.claudeQueue(now)
	items := all
	if len(items) > claudeBatch {
		items = items[:claudeBatch]
	}
	if items == nil {
		items = []claudeItem{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"today":        now.In(s.tz).Format("Monday 2 January 2006"),
		"instructions": ClaudeInstructions + s.upcomingEvents(now),
		"calls":        items,
		"remaining":    len(all) - len(items),
	})
}

// ClaudeAnswer is Claude's read of one call.
type ClaudeAnswer struct {
	ID           string   `json:"id"`
	Read         string   `json:"read"`
	Summary      string   `json:"summary"`
	NextSteps    []string `json:"next_steps"`
	CompanyFacts string   `json:"company_facts"`
}

// handleClaudeAnswers: POST /api/claude/answers.
func (s *Server) handleClaudeAnswers(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Model string         `json:"model"`
		Calls []ClaudeAnswer `json:"calls"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&in); err != nil {
		http.Error(w, "bad JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	by := "Claude"
	if m := strings.TrimSpace(in.Model); m != "" {
		by = "Claude (" + m + ")"
	}
	now := s.now()
	applied, stale, unknown := 0, 0, 0
	for _, a := range in.Calls {
		c, ok := s.store.Get(a.ID)
		if !ok {
			unknown++
			continue
		}
		if _, hash := s.claudeInput(&c); hash != a.Read {
			stale++ // the call changed meanwhile: it comes back in the queue
			continue
		}
		s.store.Update(a.ID, func(c *Call) { applyClaude(c, a, by, now) })
		applied++
	}
	if applied > 0 {
		s.syncNow() // rewrites the Drive docs
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"applied": applied, "stale": stale, "unknown": unknown, "remaining": len(s.claudeQueue(now))})
}

// applyClaude stores Claude's notes; steps Mark had ticked stay ticked when
// Claude kept them word for word.
func applyClaude(c *Call, a ClaudeAnswer, by string, now time.Time) {
	done := map[string]bool{}
	for _, st := range c.doneSteps() {
		done[strings.ToLower(strings.TrimSpace(st))] = true
	}
	var steps []string
	for _, st := range a.NextSteps {
		if st = strings.TrimSpace(st); st != "" {
			steps = append(steps, st)
		}
	}
	c.ClaudeNotes = &Notes{Summary: strings.TrimSpace(a.Summary), NextSteps: steps, By: by}
	c.CompanyFacts = strings.TrimSpace(a.CompanyFacts)
	c.ClaudeRead, c.ClaudeAt = a.Read, now
	c.Ticked = nil
	for i, st := range steps {
		if done[strings.ToLower(st)] {
			if c.Ticked == nil {
				c.Ticked = map[int]bool{}
			}
			c.Ticked[i] = true
		}
	}
}

var eventDirRe = regexp.MustCompile(`^20\d\d-`)

// upcomingEvents lists the events on the websites (re-read every 6 hours),
// so the Push plays can name a real event near the company.
func (s *Server) upcomingEvents(now time.Time) string {
	cs := s.claude
	if cs == nil {
		return ""
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if !cs.eventsAt.IsZero() && now.Sub(cs.eventsAt) < 6*time.Hour {
		return cs.events
	}
	type ev struct {
		at   time.Time
		line string
	}
	var evs []ev
	for _, repo := range sites.Repos() {
		body, err := sites.Get(sites.APIURL + "/repos/" + repo + "/contents/")
		if err != nil {
			continue
		}
		var entries []struct{ Name, Type string }
		if json.Unmarshal(body, &entries) != nil {
			continue
		}
		for _, e := range entries {
			if e.Type != "dir" || !eventDirRe.MatchString(e.Name) {
				continue
			}
			meta, err := sites.Get(sites.RawURL + "/" + repo + "/HEAD/" + url.PathEscape(e.Name) + "/metadata.yml")
			if err != nil {
				continue
			}
			var brand, city string
			var start time.Time
			for _, line := range strings.Split(string(meta), "\n") {
				k, v, ok := strings.Cut(sites.StripComment(line), ":")
				if !ok || strings.HasPrefix(line, " ") {
					continue
				}
				v = sites.Unquote(strings.TrimSpace(v))
				switch strings.TrimSpace(k) {
				case "brand_name":
					brand = v
				case "city_name":
					city = v
				case "start_time":
					start, _ = time.Parse(time.RFC3339, v)
				}
			}
			if brand == "" || start.IsZero() || start.Before(now) {
				continue
			}
			evs = append(evs, ev{start, fmt.Sprintf("- %s %s, %s", brand, city, start.In(s.tz).Format("2 Jan 2006"))})
		}
	}
	sort.Slice(evs, func(i, j int) bool { return evs[i].at.Before(evs[j].at) })
	var lines []string
	for _, e := range evs {
		lines = append(lines, e.line)
	}
	cs.events, cs.eventsAt = "", now
	if len(lines) > 0 {
		cs.events = "\n\nUPCOMING EVENTS (from the websites; Conf42 events are online):\n" + strings.Join(lines, "\n") + "\n"
	}
	return cs.events
}
