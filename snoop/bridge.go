package snoop

import (
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sreday/badger/bridge"
)

// Snoop and Snap (Marek 2026-11-13). Snoop never reads Snap's data and
// Snap never reads Snoop's: they talk through the bridge package, and Snout
// hands each one the other's view (SetMail here).
//
// What Snoop does with it:
//   - "Resolve in Snap" on a Needs attention tile: Snap drafts the email
//     that settles that step (in the right thread, in Mark's learned
//     style). The tile shows where the draft stands and ticks itself once
//     the email is sent. The call keeps the trace.
//   - A missed sales call resolves itself when Mark has emailed its people
//     since; any call notes when he followed up by email.
//   - The call page lists Snap's open cards with the call's people: the
//     pre-call brief.
//   - Snoop offers its calls to Snap (bridge.Calls): notes for Claude's
//     reading of the mail, the "Related call" pill, promises still open.

// teamDomains: the organisers' own domains never link a call to a thread.
var teamDomains = []string{"sreday.com", "conf42.com", "llmday.com", "platformday.com", "promptengineering.rocks", "devopsnotdead.com"}

const (
	promiseWindow = 45 * 24 * time.Hour // how far back call promises are still chased
	snapTraces    = 12
)

// SnapTrace is one "Resolve in Snap" on a call: what was asked, and how it went.
type SnapTrace struct {
	Key     string    `json:"key"` // the step's index, or "m" for the missed-call follow-up
	Ref     string    `json:"ref"` // Snap's reference for the draft
	What    string    `json:"what"`
	At      time.Time `json:"at"`
	Status  string    `json:"status,omitempty"` // working, ready, sent, missing
	Subject string    `json:"subject,omitempty"`
	Gmail   string    `json:"gmail,omitempty"`
	Snap    string    `json:"snap,omitempty"`
	DoneAt  time.Time `json:"done_at,omitempty"`
}

// Working: Snap is still drafting.
func (t SnapTrace) Working(now time.Time) bool {
	return t.Status == "working" && now.Sub(t.At) < 20*time.Minute
}

// SetMail connects Snap's view (Snout does this once, at start).
func (s *Server) SetMail(m bridge.Mail) { s.mail = m }

var mineStep = regexp.MustCompile(`(?i)^\s*(mark|marek)\b`)

func (s *Server) callURL(id string) string { return s.cfg.PublicURL + s.cfg.Base + "/admin/call/" + id }

// people: the call's guests, without Mark and the team's own shared boxes.
func (s *Server) people(c *Call) []bridge.Person {
	var out []bridge.Person
	for _, a := range c.Attendees {
		if a.Email == "" || strings.EqualFold(a.Email, s.cfg.Account) {
			continue
		}
		out = append(out, bridge.Person{Name: a.Name, Email: strings.ToLower(a.Email)})
	}
	return out
}

// view is a call as other tools see it.
func (s *Server) view(c *Call, now time.Time) bridge.Call {
	v := bridge.Call{ID: c.ID, Title: c.Title, URL: s.callURL(c.ID), Company: c.Company(), Start: c.Start, End: c.End, Updated: c.End,
		Upcoming: now.Before(c.Start) && !c.Cancelled && !c.Declined, Missed: c.Missed(now), Resolved: c.Resolved, Facts: c.CompanyFacts, People: s.people(c)}
	if v.Title == "" {
		v.Title = "Meet " + c.MeetingCode
	}
	if c.ClaudeAt.After(v.Updated) {
		v.Updated = c.ClaudeAt
	}
	if n := c.Notes(); n != nil {
		v.Summary = n.Summary
		for i, st := range n.NextSteps {
			v.Steps = append(v.Steps, bridge.Step{I: i, Text: st, Done: c.Ticked[i], Mine: mineStep.MatchString(st)})
		}
	}
	return v
}

// --- bridge.Calls ------------------------------------------------------------

func (s *Server) Call(id string) (bridge.Call, bool) {
	c, ok := s.store.Get(id)
	if !ok {
		return bridge.Call{}, false
	}
	return s.view(&c, s.now()), true
}

func (s *Server) CallsWith(emails []string, company string) []bridge.Call {
	m := bridge.NewMatcher(emails, company, teamDomains)
	if m.Empty() {
		return nil
	}
	now := s.now()
	var out []bridge.Call
	for _, c := range s.store.All() {
		if c.Cancelled || c.Declined {
			continue
		}
		if !m.Hit(bridge.Emails(s.people(&c)), c.Company()) {
			continue
		}
		out = append(out, s.view(&c, now))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.After(out[j].Start) })
	return out
}

func (s *Server) OpenPromises() []bridge.Call {
	now := s.now()
	var out []bridge.Call
	for _, c := range s.store.All() {
		if c.Cancelled || now.Before(c.End) || now.Sub(c.End) > promiseWindow || len(s.people(&c)) == 0 {
			continue
		}
		v := s.view(&c, now)
		if (v.Missed && !v.Resolved) || len(v.Open(true)) > 0 {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.After(out[j].Start) })
	return out
}

func (s *Server) SearchCalls(q string, n int) []bridge.CallHit {
	words := strings.Fields(strings.ToLower(q))
	if len(words) == 0 {
		return nil
	}
	now := s.now()
	has := func(text string) bool {
		text = strings.ToLower(text)
		for _, w := range words {
			if !strings.Contains(text, w) {
				return false
			}
		}
		return true
	}
	any := func(text string) bool {
		text = strings.ToLower(text)
		for _, w := range words {
			if len(w) > 3 && strings.Contains(text, w) {
				return true
			}
		}
		return false
	}
	calls := s.store.All()
	sort.Slice(calls, func(i, j int) bool { return calls[i].Start.After(calls[j].Start) })
	var out []bridge.CallHit
	for _, c := range calls {
		if c.Cancelled || now.Before(c.Start) {
			continue
		}
		v := s.view(&c, now)
		head := v.Title + " " + v.Company + " " + v.Summary
		for _, p := range v.People {
			head += " " + p.Name + " " + p.Email
		}
		for _, st := range v.Steps {
			head += " " + st.Text
		}
		hit := bridge.CallHit{Call: v}
		for _, l := range c.Lines() {
			if any(l.Text) && len(hit.Lines) < 8 {
				hit.Lines = append(hit.Lines, fmt.Sprintf("[%s] %s: %s", l.At.In(s.tz).Format("15:04"), l.Speaker, clipText(l.Text, 300)))
			}
		}
		if !has(head) && len(hit.Lines) == 0 {
			continue
		}
		out = append(out, hit)
		if n > 0 && len(out) == n {
			break
		}
	}
	return out
}

func (s *Server) CompanyFacts(company string) string {
	want := bridge.Norm(company)
	if want == "" {
		return ""
	}
	var best Call
	for _, c := range s.store.All() {
		if c.CompanyFacts != "" && bridge.Norm(c.Company()) == want && c.Start.After(best.Start) {
			best = c
		}
	}
	return best.CompanyFacts
}

func clipText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// --- Resolve in Snap ---------------------------------------------------------

// CanResolve: Snap is there to draft.
func (s *Server) CanResolve() bool { return s.mail != nil }

// handleResolveInSnap: POST /admin/call/{id}/snap (i = step index, or missed=1).
func (s *Server) handleResolveInSnap(w http.ResponseWriter, r *http.Request) {
	c, ok := s.store.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if s.mail == nil {
		http.Error(w, "Snap isn't connected", http.StatusServiceUnavailable)
		return
	}
	now := s.now()
	key, what := "m", ""
	if r.FormValue("missed") == "1" {
		what = "Follow up on the call that didn't happen: apologise for the no-show, share the sponsorship info they asked about, and offer a new time."
		if c.OthersDeclined {
			what = "They declined the call and haven't rebooked: write a short, friendly note that keeps the door open, shares the offer, and makes it easy to pick a new time."
		}
	} else {
		i, err := strconv.Atoi(r.FormValue("i"))
		n := c.Notes()
		if err != nil || n == nil || i < 0 || i >= len(n.NextSteps) {
			http.Error(w, "No such step", http.StatusBadRequest)
			return
		}
		key, what = strconv.Itoa(i), n.NextSteps[i]
	}
	for _, t := range c.Snap {
		if t.Key == key && t.Working(now) {
			s.backToActions(w, r) // Snap is already on it
			return
		}
	}
	var notes strings.Builder
	if n := c.Notes(); n != nil {
		if n.Summary != "" {
			notes.WriteString("What happened on the call: " + n.Summary + "\n")
		}
		for i, st := range n.NextSteps {
			done := ""
			if c.Ticked[i] {
				done = " (done)"
			}
			notes.WriteString("- " + st + done + "\n")
		}
	}
	if c.Calendly {
		notes.WriteString("Booked through Calendly: " + c.CalendlyEvent + "\n")
		for k, v := range c.CalendlyAnswers {
			notes.WriteString("  " + k + ": " + v + "\n")
		}
	}
	if c.CompanyFacts != "" {
		notes.WriteString("About their company: " + c.CompanyFacts + "\n")
	}
	v := s.view(&c, now)
	ref, err := s.mail.Draft(bridge.DraftRequest{CallID: c.ID, CallTitle: v.Title, CallURL: v.URL, CallAt: c.Start, People: v.People, Company: v.Company, Brief: what, Notes: notes.String()})
	if err != nil {
		log.Printf("Snoop: resolve in Snap for %s: %v", c.ID, err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.store.Update(c.ID, func(c *Call) {
		c.Snap = append(c.Snap, SnapTrace{Key: key, Ref: ref, What: clipText(what, 200), At: now, Status: "working"})
		if len(c.Snap) > snapTraces {
			c.Snap = c.Snap[len(c.Snap)-snapTraces:]
		}
	})
	s.backToActions(w, r)
}

func (s *Server) backToActions(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Requested-With") == "fetch" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	to := r.Header.Get("Referer")
	if to == "" {
		to = s.cfg.Base + "/#actions"
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// snapTick asks Snap how the drafts are doing and what Mark has sent, and
// settles what that settles. Cheap (both tools are in one process), so it
// runs whenever a page is drawn.
func (s *Server) snapTick() {
	if s.mail == nil {
		return
	}
	now := s.now()
	for _, c := range s.store.All() {
		if c.Cancelled || now.Before(c.End) || now.Sub(c.End) > promiseWindow {
			continue
		}
		var changes []func(c *Call)
		for i, t := range c.Snap {
			if t.Status == "sent" || t.Status == "missing" || t.Ref == "" {
				continue
			}
			st := s.mail.DraftState(t.Ref)
			if st.Status == "" || (st.Status == t.Status && st.Subject == t.Subject) {
				continue
			}
			i, st, key := i, st, t.Key
			changes = append(changes, func(c *Call) {
				if i >= len(c.Snap) {
					return
				}
				tr := &c.Snap[i]
				tr.Status, tr.Subject, tr.Gmail, tr.Snap = st.Status, st.Subject, st.Gmail, st.Snap
				if st.Status != "sent" {
					return
				}
				// The email went out: the step is done.
				tr.DoneAt = st.At
				if key == "m" {
					c.Resolved = true
				} else if n, err := strconv.Atoi(key); err == nil {
					if c.Ticked == nil {
						c.Ticked = make(map[int]bool)
					}
					c.Ticked[n] = true
				}
			})
		}
		// Any email from Mark to the call's people since the call: note it,
		// and a missed call is followed up.
		if people := s.people(&c); len(people) > 0 {
			if sent, ok := s.mail.SentTo(bridge.Emails(people), c.End); ok && !sent.At.Equal(c.MailedAt) {
				changes = append(changes, func(c *Call) {
					c.MailedAt, c.MailedSubject, c.MailedURL = sent.At, sent.Subject, sent.URL
					if c.Missed(now) || c.OthersDeclined {
						c.Resolved = true
					}
				})
			}
		}
		if len(changes) > 0 {
			s.store.Update(c.ID, func(c *Call) {
				for _, f := range changes {
					f(c)
				}
			})
		}
	}
}

// traceFor: the latest "Resolve in Snap" on one tile.
func traceFor(c Call, key string) *SnapTrace {
	for i := len(c.Snap) - 1; i >= 0; i-- {
		if c.Snap[i].Key == key {
			t := c.Snap[i]
			return &t
		}
	}
	return nil
}

// brief: Snap's cards with the call's people, for the call page.
func (s *Server) brief(c *Call) []bridge.Card {
	if s.mail == nil {
		return nil
	}
	cards := s.mail.CardsWith(bridge.Emails(s.people(c)), c.Company())
	if len(cards) > 6 {
		cards = cards[:6]
	}
	return cards
}

// knownFromSnap: Snap's background on the call's company, when Snoop has none.
func (s *Server) knownFromSnap(c *Call) string {
	if s.mail == nil || c.CompanyFacts != "" || c.Company() == "" {
		return ""
	}
	return s.mail.CompanyFacts(c.Company())
}
