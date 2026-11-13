package snap

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/sreday/badger/bridge"
)

// Snap and Snoop (Marek 2026-11-13). Snap never reads Snoop's data and
// Snoop never reads Snap's: they talk through the bridge package, and Snout
// hands each one the other's view (SetCalls here).
//
// What Snap does with Snoop's calls:
//   - "Related call" under Quick question: the call with the thread's
//     people, one click away in Snoop.
//   - Claude reads a thread together with the notes of the calls with the
//     same people, so it knows what was agreed out loud. New notes send an
//     open loose end back to Claude.
//   - A promise Mark made on a call sends the matching thread to Claude
//     again; promises with no thread at all are listed on the Hot tab.
//   - A call booked with a card's people parks the card until the call.
//   - Quick question can search the calls; company facts are shared.
//
// What Snap offers Snoop (bridge.Mail): drafting the email that settles a
// call's step ("Resolve in Snap"), how that draft is doing, the cards with
// a call's people (the pre-call brief), and what Mark has sent them since.

// CallRef is the trace of a call on a draft Snap made for it.
type CallRef struct {
	ID    string    `json:"id"`
	Title string    `json:"title"`
	URL   string    `json:"url"`
	At    time.Time `json:"at"`
	What  string    `json:"what,omitempty"` // the step the email settles
}

// SetCalls connects Snoop's view (Snout does this once, at start).
func (s *Server) SetCalls(c bridge.Calls) { s.calls = c }

func emailsOf(people []Person) []string {
	out := make([]string, 0, len(people))
	for _, p := range people {
		out = append(out, p.Email)
	}
	return out
}

func (s *Server) snapURL(t Thread, now time.Time) string {
	base := strings.TrimRight(s.cfg.PublicURL, "/")
	if base == "" {
		base = "https://scan.sreday.com"
	}
	u := base + s.cfg.Base + "/?view=" + t.Category
	if b := t.Bucket(now); b != "active" {
		u += "&st=" + b
	}
	return u + "&t=" + t.ID
}

func (s *Server) gmailURL(thread string) string {
	return "https://mail.google.com/mail/u/?authuser=" + s.cfg.Account + "#all/" + thread
}

// --- bridge.Mail: what Snoop can ask of Snap ---------------------------------

// Draft starts the email that settles something from a call. Claude finds
// the conversation with those people and replies in it, or writes a new
// email when there is none; nothing is sent.
func (s *Server) Draft(req bridge.DraftRequest) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Follow up on a call. Mark had the call %q on %s", req.CallTitle, req.CallAt.In(s.tz).Format("Mon 2 Jan 2006"))
	var who []string
	for _, p := range req.People {
		if p.Name != "" {
			who = append(who, p.Name+" <"+p.Email+">")
		} else {
			who = append(who, p.Email)
		}
	}
	if len(who) > 0 {
		fmt.Fprintf(&b, " with %s", strings.Join(who, ", "))
	}
	if req.Company != "" {
		fmt.Fprintf(&b, " (%s)", req.Company)
	}
	b.WriteString(".\n\nWhat this email must settle: " + req.Brief + "\n")
	if req.Notes != "" {
		b.WriteString("\nFrom the call (Snoop's notes; use them for facts, don't recap the call):\n" + req.Notes)
	}
	b.WriteString("\nFind the email conversation with these people about this matter and reply in it; if there is none, write a new email to them. One draft, addressed to the people from the call.")
	c, err := s.startCompose(b.String(), "After the call with "+firstOf(req.Company, req.CallTitle)+": "+clip(req.Brief, 120),
		&CallRef{ID: req.CallID, Title: req.CallTitle, URL: req.CallURL, At: req.CallAt, What: clip(req.Brief, 200)})
	if err != nil {
		return "", err
	}
	return c.ID, nil
}

func firstOf(a ...string) string {
	for _, s := range a {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// DraftState: where a draft asked for through Draft stands.
func (s *Server) DraftState(ref string) bridge.DraftState {
	now := s.now()
	s.mu.Lock()
	var c *Compose
	for i := range s.composes {
		if s.composes[i].ID == ref {
			cc := s.composes[i]
			c = &cc
		}
	}
	var send *Send
	if c != nil && c.Draft != "" {
		for i := range s.sends {
			if s.sends[i].Draft == c.Draft {
				p := s.sends[i]
				send = &p
			}
		}
	}
	s.mu.Unlock()
	switch {
	case c == nil:
		return bridge.DraftState{}
	case c.Working(now):
		return bridge.DraftState{Status: "working", At: c.Asked}
	case c.Thread == "":
		return bridge.DraftState{Status: "missing", At: c.Asked}
	}
	st := bridge.DraftState{Status: "ready", Subject: c.Subject, To: c.To, At: c.Ready,
		Gmail: "https://mail.google.com/mail/u/?authuser=" + s.cfg.Account + "#drafts/" + c.Thread}
	if t, ok := s.store.Get(c.Thread); ok && t.IsLoose() {
		st.Snap = s.snapURL(t, now)
	}
	switch {
	case send != nil && send.Sent != "":
		st.Status, st.At, st.Gmail = "sent", send.SentAt, s.gmailURL(c.Thread)
	case send != nil && send.Gone:
		st.Status = "missing" // the draft was thrown away
	}
	return st
}

// CardsWith: Snap's loose ends with these people or this company, the ones
// to act on first.
func (s *Server) CardsWith(emails []string, company string) []bridge.Card {
	m := bridge.NewMatcher(emails, company, s.cfg.TeamDomains)
	if m.Empty() {
		return nil
	}
	now := s.now()
	all := s.store.All()
	leads := topicLeads(all)
	var out []bridge.Card
	for _, t := range all {
		if !t.IsLoose() || !m.Hit(emailsOf(t.People), t.Company) {
			continue
		}
		if k := normTopic(t.Topic); k != "" && t.ByClaude() && leads[k].ID != t.ID {
			continue
		}
		fixCard(&t)
		head := t.Label
		if head == "" {
			head = t.Subject
		}
		out = append(out, bridge.Card{ID: t.ID, Headline: head, Status: firstOf(t.Why, t.Summary), Ball: t.Waiting, Bucket: t.Bucket(now),
			Event: confName(t.Event), LastAt: t.LastAt, URL: s.snapURL(t, now)})
	}
	rank := map[string]int{"active": 0, "snoozed": 1, "closed": 2}
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := rank[out[i].Bucket], rank[out[j].Bucket]; a != b {
			return a < b
		}
		return out[i].LastAt.After(out[j].LastAt)
	})
	return out
}

// SentTo: the newest thread where Mark himself wrote last to any of these
// people, after since.
func (s *Server) SentTo(emails []string, since time.Time) (bridge.Sent, bool) {
	m := bridge.NewMatcher(emails, "", s.cfg.TeamDomains)
	if m.Empty() {
		return bridge.Sent{}, false
	}
	var best Thread
	for _, t := range s.store.All() {
		if t.LastFrom == "me" && t.LastAt.After(since) && t.LastAt.After(best.LastAt) && m.Hit(emailsOf(t.People), "") {
			best = t
		}
	}
	if best.ID == "" {
		return bridge.Sent{}, false
	}
	return bridge.Sent{At: best.LastAt, Subject: fixBrands(best.Subject), URL: s.gmailURL(best.ID)}, true
}

// CompanyFacts: Snap's background check on a company ("" if none).
func (s *Server) CompanyFacts(company string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.facts[companyKey(company)].Facts
}

// --- What Snap does with the calls -------------------------------------------

const (
	callsShown  = 3
	callsWindow = 120 * 24 * time.Hour // older calls don't go into Claude's reading
)

// callsFor: the calls with a thread's people or company, newest first.
func (s *Server) callsFor(t Thread) []bridge.Call {
	if s.calls == nil {
		return nil
	}
	return s.calls.CallsWith(emailsOf(t.People), t.Company)
}

// relatedCall picks the call a card links to: the next one booked, else the
// latest one held.
func relatedCall(calls []bridge.Call) (bridge.Call, bool) {
	var next, last *bridge.Call
	for i := range calls {
		c := &calls[i]
		if c.Upcoming {
			if next == nil || c.Start.Before(next.Start) {
				next = c
			}
		} else if last == nil || c.Start.After(last.Start) {
			last = c
		}
	}
	switch {
	case next != nil:
		return *next, true
	case last != nil:
		return *last, true
	}
	return bridge.Call{}, false
}

// callsBlock: the calls with the same people, for the excerpt Claude reads.
func (s *Server) callsBlock(t Thread) string {
	calls := s.callsFor(t)
	if len(calls) == 0 {
		return ""
	}
	now := s.now()
	var b strings.Builder
	n := 0
	for _, c := range calls {
		if n == callsShown || (!c.Upcoming && now.Sub(c.Start) > callsWindow) {
			continue
		}
		if n == 0 {
			b.WriteString("CALLS WITH THE SAME PEOPLE OR COMPANY (from Snoop, Mark's call notes; newest first):\n")
		}
		n++
		when := c.Start.In(s.tz).Format("2 Jan 2006")
		switch {
		case c.Upcoming:
			fmt.Fprintf(&b, "- BOOKED for %s: %q. A call is coming: the thread can wait for it unless something must happen before.\n", c.Start.In(s.tz).Format("Mon 2 Jan 2006, 15:04"), c.Title)
			continue
		case c.Missed && !c.Resolved:
			fmt.Fprintf(&b, "- %s %q: the call did NOT happen (nobody showed up) and it was not followed up.\n", when, c.Title)
			continue
		}
		fmt.Fprintf(&b, "- %s %q", when, c.Title)
		if c.Summary != "" {
			fmt.Fprintf(&b, ": %s", clip(c.Summary, 500))
		}
		b.WriteString("\n")
		for _, st := range c.Steps {
			state := "open"
			if st.Done {
				state = "done"
			}
			fmt.Fprintf(&b, "    next step (%s): %s\n", state, clip(st.Text, 220))
		}
	}
	return b.String()
}

// callsLine: the latest call, in a sentence, for a draft request.
func (s *Server) callsLine(t Thread) string {
	calls := s.callsFor(t)
	now := s.now()
	for _, c := range calls {
		if c.Upcoming || now.Sub(c.Start) > callsWindow || c.Summary == "" {
			continue
		}
		line := fmt.Sprintf("Mark's call with them on %s (%q, from Snoop's notes; facts to build on, don't recap the call): %s", c.Start.In(s.tz).Format("2 Jan 2006"), c.Title, clip(c.Summary, 500))
		for _, st := range c.Open(false) {
			line += " Open step: " + clip(st.Text, 200) + "."
		}
		return line + "\n"
	}
	return ""
}

// callsTick runs with every read of Gmail: new call notes send the matching
// threads back to Claude, and a booked call parks the card until it's held.
func (s *Server) callsTick(now time.Time) {
	if s.calls == nil {
		return
	}
	// Promises from calls: a thread Claude read before the notes existed is
	// read again in their light ("you promised the deck on the call").
	for _, c := range s.calls.OpenPromises() {
		m := bridge.NewMatcher(bridge.Emails(c.People), c.Company, s.cfg.TeamDomains)
		if m.Empty() {
			continue
		}
		for _, t := range s.store.All() {
			if t.Skip != "" || !t.ByClaude() || t.Done || t.DeadEnd || t.ClaudeHistory == "" || !t.AnalyzedAt.Before(c.Updated) || now.Sub(t.LastAt) > callsWindow {
				continue
			}
			if m.Hit(emailsOf(t.People), t.Company) {
				s.store.Update(t.ID, func(t *Thread) { t.ClaudeHistory = "" })
			}
		}
	}
	// A call is booked with a card's people: the card waits for it.
	for _, t := range s.store.All() {
		if !t.Flagged() || t.Bucket(now) != "active" {
			continue
		}
		var next *bridge.Call
		calls := s.callsFor(t)
		for i := range calls {
			if c := &calls[i]; c.Upcoming && (next == nil || c.Start.Before(next.Start)) {
				next = c
			}
		}
		if next == nil || !next.End.After(t.SnoozeUntil) || next.Start.Equal(t.CallBooked) {
			continue // (a card Mark woke by hand stays awake)
		}
		at, end, title := next.Start, next.End, next.Title
		s.store.Update(t.ID, func(t *Thread) {
			t.SnoozeUntil, t.CallBooked = end, at
			note := "Call booked for " + at.In(s.tz).Format("Mon 2 Jan") + " (" + title + "). "
			if !strings.HasPrefix(t.Why, "Call booked for ") {
				t.Why = note + t.Why
			}
		})
	}
}

// CallPromise is a call that still needs something from Mark and has no
// card in Snap: listed on the Hot tab, with the way into Snoop.
type CallPromise struct {
	Title, Company, URL, What string
	At                        time.Time
	More                      int // further open steps on the same call
}

func (s *Server) callPromises(all []Thread, now time.Time) []CallPromise {
	if s.calls == nil {
		return nil
	}
	var out []CallPromise
	for _, c := range s.calls.OpenPromises() {
		m := bridge.NewMatcher(bridge.Emails(c.People), c.Company, s.cfg.TeamDomains)
		if m.Empty() {
			continue
		}
		covered := false
		for _, t := range all {
			if !m.Hit(emailsOf(t.People), t.Company) {
				continue
			}
			// A card already carries it, or Mark has written to them since the call.
			if (t.IsLoose() && !t.Done && !t.DeadEnd) || (t.LastFrom == "me" && t.LastAt.After(c.End)) {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		p := CallPromise{Title: c.Title, Company: c.Company, URL: c.URL, At: c.Start}
		if open := c.Open(true); len(open) > 0 {
			p.What, p.More = open[0].Text, len(open)-1
		} else {
			p.What = "The call didn't happen and nobody followed up."
		}
		p.What = fixBrands(p.What)
		out = append(out, p)
		if len(out) == 5 {
			break
		}
	}
	return out
}

// handleCalls: GET /api/calls?q= lets the quick-question routine search
// Mark's calls (titles, notes, transcripts) through Snap.
func (s *Server) handleCalls(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || len(q) > 300 {
		http.Error(w, "q: a few words to look for in the calls, up to 300 characters", http.StatusBadRequest)
		return
	}
	hits := []bridge.CallHit{}
	if s.calls != nil {
		hits = append(hits, s.calls.SearchCalls(q, 6)...)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"q": q, "calls": hits})
}

// traceCall leaves the call's mark on the thread its email went into, so
// the card (and Claude's next read) know where the email came from.
func (s *Server) traceCall(thread string, c CallRef) {
	if _, ok := s.store.Get(thread); !ok {
		return // a new email: the thread reaches Snap with the next read of Gmail
	}
	line := fmt.Sprintf("Drafted from your call %q on %s: %s", c.Title, c.At.In(s.tz).Format("2 Jan"), c.What)
	s.store.Update(thread, func(t *Thread) {
		for _, old := range t.Context {
			if old == line {
				return
			}
		}
		t.Context = append(t.Context, line)
		if len(t.Context) > 8 {
			t.Context = t.Context[len(t.Context)-8:]
		}
	})
}
