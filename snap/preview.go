package snap

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// Preview and Correct draft (Marek 2026-11-13): next to "Draft ready in
// Gmail" a Preview button opens the draft right on the card, in place of
// the wheel. Marek can edit it there and press "Correct draft": Snap is
// read-only in Gmail, so the draft routine (Gmail connector) swaps the
// draft's text for his version, word for word, and Snap watches until Gmail
// shows it. Every correction is a lesson: the pair (what Claude wrote, what
// Mark made of it) joins the sends Claude learns his style and substance
// from (style.go), at once, without waiting for the email to go out.

const (
	fixMarker = "MODE: CORRECT DRAFT"
	fixWatch  = 10 * time.Minute
	fixExpect = 60 // seconds, about
	fixMax    = 20000
)

// Fix is one "Correct draft" job on a thread.
type Fix struct {
	At     time.Time `json:"at,omitempty"`
	Want   string    `json:"want,omitempty"`   // the text Mark wants in the draft
	Status string    `json:"status,omitempty"` // saving, done, failed
}

// Saving: Claude is putting Mark's version into Gmail.
func (f Fix) Saving(now time.Time) bool { return f.Status == "saving" && now.Sub(f.At) < fixWatch }

func sameText(a, b string) bool {
	return strings.Join(strings.Fields(a), " ") == strings.Join(strings.Fields(b), " ")
}

// handlePreview: GET /admin/thread/{id}/draft, the draft as it sits in Gmail.
func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	id, ok := s.known(w, r)
	if !ok {
		return
	}
	t, _ := s.store.Get(id)
	w.Header().Set("Content-Type", "application/json")
	if t.Fix.Saving(s.now()) {
		json.NewEncoder(w).Encode(map[string]any{"text": t.Fix.Want, "saving": true})
		return
	}
	draft := s.draftInThread(id)
	if draft == "" {
		http.Error(w, "The draft is no longer in Gmail: it was sent or deleted", http.StatusNotFound)
		return
	}
	text, subject, err := s.gmail.DraftBody(draft)
	if err != nil {
		http.Error(w, "Gmail: "+err.Error(), http.StatusBadGateway)
		return
	}
	_, to := s.gmail.DraftHead(draft)
	json.NewEncoder(w).Encode(map[string]any{"draft": draft, "text": text, "subject": subject, "to": to})
}

// fixPrompt is the draft routine's payload for a correction.
func (s *Server) fixPrompt(t Thread, draft, text string) string {
	return fmt.Sprintf("%s\n\nMark edited the draft reply in the thread %q (Gmail thread id %s, draft id %s) himself, in Snap. Replace that draft's text with his version below, word for word, as your prompt says for this mode. Do not rewrite, fix, shorten or add anything, and send nothing.\n\n<<<MARK'S TEXT\n%s\nMARK'S TEXT>>>", fixMarker, t.Subject, t.ID, draft, text)
}

// handleCorrect: POST /admin/thread/{id}/draft/correct (text).
func (s *Server) handleCorrect(w http.ResponseWriter, r *http.Request) {
	id, ok := s.known(w, r)
	if !ok {
		return
	}
	if !s.CanDraft() {
		http.Error(w, "Drafting isn't set up: SNAP_DRAFT_ROUTINE and SNAP_DRAFT_TOKEN", http.StatusServiceUnavailable)
		return
	}
	want := strings.TrimSpace(strings.ReplaceAll(r.FormValue("text"), "\r\n", "\n"))
	if want == "" {
		http.Error(w, "The draft is empty", http.StatusBadRequest)
		return
	}
	if len(want) > fixMax {
		http.Error(w, "That's too long for a draft", http.StatusBadRequest)
		return
	}
	t, _ := s.store.Get(id)
	now := s.now()
	if t.Fix.Saving(now) {
		http.Error(w, "Claude is still saving your last correction", http.StatusConflict)
		return
	}
	draft := s.draftInThread(id)
	if draft == "" {
		http.Error(w, "The draft is no longer in Gmail: it was sent or deleted", http.StatusNotFound)
		return
	}
	before, _, err := s.gmail.DraftBody(draft)
	if err != nil {
		http.Error(w, "Gmail: "+err.Error(), http.StatusBadGateway)
		return
	}
	if sameText(before, want) {
		http.Error(w, "Nothing changed in the draft", http.StatusBadRequest)
		return
	}
	if err := s.fireTrigger(s.cfg.DraftRoutine, s.cfg.DraftToken, s.fixPrompt(t, draft, want)); err != nil {
		if err == errBadToken {
			err = fmt.Errorf("the draft routine's token is wrong or was regenerated: update SNAP_DRAFT_TOKEN")
		}
		log.Printf("Snap: correct draft for %s: %v", id, err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.keepCorrection(draft, t, before, want)
	s.recordPick("correct", "", &t)
	s.store.Update(id, func(t *Thread) { t.Fix = Fix{At: now, Want: want, Status: "saving"} })
	go s.watchFix(id, draft, want)
	back(w, r)
}

// keepCorrection files the pair for the style lessons, and makes Mark's
// version the text the eventual send is compared with.
func (s *Server) keepCorrection(draft string, t Thread, before, after string) {
	var rb [6]byte
	rand.Read(rb[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	c := Send{Draft: "fix-" + hex.EncodeToString(rb[:]), Thread: t.ID, Kind: "correction", Brief: clip(replyBrief(t), 300), Subject: t.Subject,
		At: s.now(), Claude: before, Sent: after, SentAt: s.now()}
	for i := range s.sends {
		if p := &s.sends[i]; p.Draft == draft {
			if p.Brief != "" {
				c.Brief = p.Brief
			}
			if p.Subject != "" {
				c.Subject = p.Subject
			}
			p.Claude = after
		}
	}
	s.sends = append(s.sends, c)
	if len(s.sends) > keepSends {
		s.sends = s.sends[len(s.sends)-keepSends:]
	}
	writeJSON(s.dir, "sends.json", s.sends)
}

// watchFix looks at the thread's draft until it reads as Mark wrote it.
func (s *Server) watchFix(id, oldDraft, want string) {
	defer s.pumpDrafts() // a slot is free: the next queued draft starts
	for end := time.Now().Add(fixWatch); time.Now().Before(end); {
		time.Sleep(s.draftGap)
		draft := s.draftInThread(id)
		if draft == "" {
			continue
		}
		text, _, err := s.gmail.DraftBody(draft)
		if err != nil || !sameText(text, want) {
			continue
		}
		if t, ok := s.store.Get(id); ok {
			s.noteTook("fix", s.now().Sub(t.Fix.At))
		}
		s.store.Update(id, func(t *Thread) { t.Fix.Status, t.HasDraft = "done", true })
		if draft != oldDraft {
			// Claude made a new draft in place of the old one: the old one
			// isn't a send to wait for any more, the new one is.
			s.updateSend(oldDraft, func(p *Send) { p.Gone = true })
			t, _ := s.store.Get(id)
			s.keepDraft(draft, id, "reply", replyBrief(t))
		}
		return
	}
	s.store.Update(id, func(t *Thread) {
		if t.Fix.Status == "saving" {
			t.Fix.Status = "failed"
		}
	})
}
