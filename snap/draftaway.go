package snap

import (
	"net/mail"
	"net/url"
	"strings"
	"time"
)

// A reply that lands in another thread (2026-11-13). The draft routine
// answers "in the thread where the conversation is now": for a website form
// that is often the lead's own follow-up mail, not the form thread Snap
// asked about. Snap used to look only at the thread it asked about, so the
// wheel kept spinning for the full quarter of an hour although the draft
// had been in Gmail after 20 seconds. Now the watcher also takes a new draft
// that sits in a thread of the same topic or goes to the same people, and
// the card remembers where its draft lives (Thread.DraftThread, DraftID).

// draftSet: the ids of the drafts in Gmail right now.
func (s *Server) draftSet() map[string]bool {
	out := map[string]bool{}
	ds, err := s.gmail.Drafts()
	if err != nil {
		return nil
	}
	for _, d := range ds {
		out[d.ID] = true
	}
	return out
}

// DraftTo: who a draft goes to (To and Cc), lower case.
func (g *Gmail) DraftTo(id string) []string {
	var d struct {
		Message gMessage `json:"message"`
	}
	if g.get("/drafts/"+url.PathEscape(id), url.Values{"format": {"metadata"}}, &d) != nil {
		return nil
	}
	var out []string
	for _, h := range d.Message.Payload.Headers {
		if n := strings.ToLower(h.Name); n != "to" && n != "cc" {
			continue
		}
		if as, err := mail.ParseAddressList(h.Value); err == nil {
			for _, a := range as {
				out = append(out, strings.ToLower(a.Address))
			}
		}
	}
	return out
}

// outside: an address that isn't the team's own.
func outside(addr string) bool {
	d := domainOf(addr)
	if d == "" {
		return false
	}
	for _, m := range mine.domains {
		if d == m {
			return false
		}
	}
	return true
}

func outsiders(t Thread) map[string]bool {
	out := map[string]bool{}
	for _, p := range t.People {
		if e := strings.ToLower(strings.TrimSpace(p.Email)); outside(e) {
			out[e] = true
		}
	}
	return out
}

// draftElsewhere finds the reply to thread id when Claude saved it in
// another thread: a draft that wasn't there when Snap asked, in a thread on
// the same topic or with the same people, or addressed to them.
func (s *Server) draftElsewhere(id string, before map[string]bool) (DraftRef, bool) {
	t, ok := s.store.Get(id)
	if !ok || before == nil {
		return DraftRef{}, false
	}
	ds, err := s.gmail.Drafts()
	if err != nil {
		return DraftRef{}, false
	}
	taken := map[string]bool{}
	for _, o := range s.store.All() {
		if o.DraftID != "" {
			taken[o.DraftID] = true
		}
	}
	s.mu.Lock()
	for _, p := range s.sends {
		taken[p.Draft] = true
	}
	s.mu.Unlock()
	who, topic := outsiders(t), normTopic(t.Topic)
	for _, d := range ds {
		if before[d.ID] || taken[d.ID] || d.Thread == "" || d.Thread == id || s.threadDrafting(d.Thread) {
			continue
		}
		if o, ok := s.store.Get(d.Thread); ok {
			if topic != "" && normTopic(o.Topic) == topic {
				return d, true
			}
			for e := range outsiders(o) {
				if who[e] {
					return d, true
				}
			}
		}
		for _, a := range s.gmail.DraftTo(d.ID) {
			if who[a] {
				return d, true
			}
		}
	}
	return DraftRef{}, false
}

// draftFor: the draft that answers a thread, wherever it lives ("" if none).
func (s *Server) draftFor(id string) string {
	if t, ok := s.store.Get(id); ok && t.DraftThread != "" {
		if d := s.draftInThread(t.DraftThread); d != "" {
			return d // the newest there: a correction may have replaced the first one
		}
		return t.DraftID
	}
	return s.draftInThread(id)
}

// checkAway looks after the cards whose draft lives in another thread: once
// that draft is no longer in Gmail it was sent (the card parks, like any
// answered one) or thrown away (the options come back).
func (s *Server) checkAway(still map[string]bool) {
	for _, t := range s.store.All() {
		if t.DraftID == "" || still[t.DraftID] || t.Fix.Saving(s.now()) {
			continue
		}
		if d := s.draftInThread(t.DraftThread); d != "" {
			s.store.Update(t.ID, func(t *Thread) { t.DraftID = d }) // replaced by a corrected one
			continue
		}
		sent := false
		at := s.now()
		if p, err := s.gmail.Thread(t.DraftThread, false); err != nil {
			continue // Gmail hiccup: look again next minute
		} else if n := len(p.Mails); n > 0 && p.Mails[n-1].FromMe && p.Mails[n-1].At.After(t.DraftAsked) {
			sent, at = true, p.Mails[n-1].At
		}
		s.store.Update(t.ID, func(t *Thread) {
			t.HasDraft, t.DraftThread, t.DraftID, t.DraftAsked = false, "", "", time.Time{}
			if sent {
				parkSent(t, at)
			}
		})
	}
}
