package snap

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
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
func (s *Server) draftPrompt(t Thread) string {
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
		b.WriteString("Where it stands (Snap's read): " + st + "\n")
	}
	if t.StillListed != "" {
		b.WriteString("Note: " + t.StillListed + ".\n")
	}
	if len(t.Actions) > 0 {
		var steps []string
		for _, a := range t.Actions {
			steps = append(steps, a.Title)
		}
		b.WriteString("Next steps Snap suggests: " + strings.Join(steps, "; ") + ".\n")
	}
	if t.Label != "" {
		b.WriteString("The loose end: " + t.Label + ".\n")
	}
	b.WriteString("Write the reply that moves this on. Some steps are Mark's own work, not the email's (a website or talks.csv edit, a contract, an invoice): never claim in the draft that such a step is done or under way; if the reply depends on it, phrase it as what happens next.\n")
	b.WriteString("\nMark's voice: open with \"Hey <first name>,\", short, warm and direct, plain text, no em dashes, no jokes, real links (never shortened). Sign off \"Mark\". Write in the language of the thread.")
	return b.String()
}

// fireDraft starts the draft routine for one thread.
func (s *Server) fireDraft(t Thread) error {
	body, _ := json.Marshal(map[string]string{"text": s.draftPrompt(t)})
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf(s.fireBase, s.cfg.DraftRoutine), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.DraftToken)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "experimental-cc-routine-2026-04-01")
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct{ Message string } `json:"error"`
		}
		json.Unmarshal(out, &e)
		switch resp.StatusCode {
		case http.StatusTooManyRequests:
			return fmt.Errorf("Claude's hourly limit for drafts is reached, try again later")
		case http.StatusUnauthorized:
			return fmt.Errorf("the draft routine's token is wrong or was regenerated: update SNAP_DRAFT_TOKEN")
		}
		return fmt.Errorf("starting Claude: %d %s", resp.StatusCode, e.Error.Message)
	}
	return nil
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
	if err := s.fireDraft(t); err != nil {
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
