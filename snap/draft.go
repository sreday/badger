package snap

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// "Draft reply with Claude" (Marek 2026-11-12: "no need to open anything,
// just get claude in through gmail mcp and get the draft done"). The button
// fires the routine "Snap: draft a reply" (Claude Code on Marek's
// subscription, Gmail connector) through its API trigger, with this
// thread's details as the payload. Claude reads the thread and saves a
// reply as a Gmail draft in it; nothing is sent. Snap then looks at the
// thread every 30 seconds for 15 minutes and shows "Draft ready" once the
// draft is there.
//
// Config: SNAP_DRAFT_ROUTINE (the routine's trig_ id) and SNAP_DRAFT_TOKEN
// (its API trigger token, sk-ant-oat01-..., made in the routine's Edit
// screen: Trigger, +, API, Generate token).

const (
	fireURL      = "https://api.anthropic.com/v1/claude_code/routines/%s/fire"
	draftRefire  = 3 * time.Minute  // a second click within this just waits for the first
	draftWatch   = 15 * time.Minute // how long Snap looks for the draft
	draftLookGap = 30 * time.Second
)

// CanDraft: the draft routine is set up.
func (s *Server) CanDraft() bool { return s.cfg.DraftRoutine != "" && s.cfg.DraftToken != "" }

// draftPrompt is the request for one thread (the routine's fire payload).
func (s *Server) draftPrompt(t Thread, ai int, note string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Draft a reply in this thread of mark@sreday.com, as Mark, and save it as a Gmail draft in the thread. Don't send it.\n\n")
	fmt.Fprintf(&b, "Thread: %q (Gmail thread id %s)", t.Subject, t.ID)
	var who []string
	for _, p := range t.People {
		if p.Name != "" {
			who = append(who, p.Name+" <"+p.Email+">")
		} else {
			who = append(who, p.Email)
		}
		if len(who) == 4 {
			break
		}
	}
	if len(who) > 0 {
		fmt.Fprintf(&b, ", with %s", strings.Join(who, ", "))
	}
	fmt.Fprintf(&b, ". Last message %s", t.LastAt.In(s.tz).Format("2 Jan 2006"))
	switch t.LastFrom {
	case "":
	case "me":
		b.WriteString(" from Mark himself, so this is a follow-up nudge")
	default:
		fmt.Fprintf(&b, " from %s", t.LastFrom)
	}
	b.WriteString(".\n")
	if st := strings.TrimSpace(t.Summary + " " + t.Why); st != "" {
		b.WriteString("Snap's notes (hints, may be wrong; the thread wins): " + st + "\n")
	}
	if t.StillListed != "" {
		b.WriteString("Note: " + t.StillListed + ".\n")
	}
	if note != "" {
		// "Other": Mark's own words win over Snap's suggestions.
		b.WriteString("Mark's own instruction for this reply (follow it; it overrides any suggestion below): " + note + "\n")
	} else if ai >= 0 && ai < len(t.Actions) {
		a := t.Actions[ai]
		b.WriteString("The reply Mark picked: " + a.Title + ".\n")
		if a.Prompt != "" {
			b.WriteString("Details: " + a.Prompt + "\n")
		}
	}
	if note == "" && (ai < 0 || ai >= len(t.Actions)) && len(t.Actions) > 0 {
		var steps []string
		for _, a := range t.Actions {
			steps = append(steps, a.Title)
		}
		b.WriteString("Next steps Snap suggests: " + strings.Join(steps, "; ") + ".\n")
	}
	if t.Label != "" {
		b.WriteString("The loose end: " + t.Label + ".\n")
	}
	if sib := s.siblings(t, s.store.All()); len(sib) > 0 {
		b.WriteString("Other emails on the same topic (read them too; put the reply in the thread where the conversation is now, the one with the newest real message, and use that thread's id instead if it isn't this one):\n")
		for _, o := range sib {
			fmt.Fprintf(&b, "- %q, Gmail thread id %s, last message %s from %s\n", o.Subject, o.ID, o.LastAt.In(s.tz).Format("2 Jan 2006"), o.LastFrom)
		}
	}
	b.WriteString("Write the reply that moves this on. Some steps are Mark's own work, not the email's (a website or talks.csv edit, a contract, an invoice): never claim in the draft that such a step is done or under way; if the reply depends on it, phrase it as what happens next.\n")
	return b.String()
}

// fireDraft starts the draft routine for one thread.
func (s *Server) fireDraft(t Thread, ai int, note string) error {
	return s.fireRoutine(s.draftPrompt(t, ai, note))
}

// handleDraft: POST /admin/thread/{id}/draft.
func (s *Server) handleDraft(w http.ResponseWriter, r *http.Request) {
	id, ok := s.known(w, r)
	if !ok {
		return
	}
	if !s.CanDraft() {
		http.Error(w, "Drafting isn't set up: SNAP_DRAFT_ROUTINE and SNAP_DRAFT_TOKEN", http.StatusServiceUnavailable)
		return
	}
	t, _ := s.store.Get(id)
	now := s.now()
	if now.Sub(t.DraftAsked) < draftRefire {
		back(w, r) // Claude is already on it
		return
	}
	ai := -1 // which action's reply (the card has a button per reply)
	if v, err := strconv.Atoi(r.FormValue("a")); err == nil {
		ai = v
	}
	note := strings.TrimSpace(r.FormValue("note")) // "Other": what Mark typed
	if len(note) > 2000 {
		note = note[:2000]
	}
	if note != "" {
		s.recordBrief("other", note, &t)
	}
	if err := s.fireDraft(t, ai, note); err != nil {
		log.Printf("Snap: draft for %s: %v", id, err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.store.Update(id, func(t *Thread) { t.DraftAsked, t.HasDraft = now, false })
	go s.watchDraft(id)
	back(w, r)
}

// watchDraft looks at the thread until the draft shows up.
func (s *Server) watchDraft(id string) {
	for end := time.Now().Add(draftWatch); time.Now().Before(end); {
		time.Sleep(s.draftGap)
		p, err := s.gmail.Thread(id, false)
		if err != nil || !p.HasDraft {
			continue
		}
		s.store.Update(id, func(t *Thread) { t.HasDraft = true })
		return
	}
}

// Drafting: Claude was asked and the draft isn't there yet.
func (t *Thread) Drafting(now time.Time) bool {
	return !t.DraftAsked.IsZero() && !t.HasDraft && now.Sub(t.DraftAsked) < draftWatch
}
