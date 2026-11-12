package snap

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// "Draft something new with Claude" (Marek 2026-11-12): a page-wide line on
// top of Snap where Mark types what he'd type in the terminal ("find the
// ClickHouse sponsorship thread and follow up with them") and Claude does it
// on his subscription, without launching Claude Code. It fires the same
// routine as the thread drafts ("Snap: draft a reply", Gmail connector) in
// its second mode: the payload starts with composeMarker, and Claude
// searches Gmail itself, reads every thread that matters, and saves ONE
// draft (a reply in the right thread, or a new email). Nothing is sent.
//
// Snap notes the drafts that exist when it fires, then lists the drafts
// every 30 seconds for 15 minutes; the first new one is Claude's.

const (
	composeMarker = "MODE: NEW DRAFT"
	composeExpect = 150 // seconds: searching takes longer than a thread reply
	composeKeep   = 20  // jobs remembered
)

// Compose is one "draft something new" request.
type Compose struct {
	ID      string    `json:"id"`
	Brief   string    `json:"brief"`
	Asked   time.Time `json:"asked"`
	Before  []string  `json:"before,omitempty"` // draft ids that existed at fire time
	Ready   time.Time `json:"ready,omitempty"`
	Thread  string    `json:"thread,omitempty"`  // the draft's Gmail thread
	Message string    `json:"message,omitempty"` // the draft's message id
	Subject string    `json:"subject,omitempty"`
	To      string    `json:"to,omitempty"`
}

// Working: Claude is on it and no draft showed up yet.
func (c Compose) Working(now time.Time) bool {
	return c.Ready.IsZero() && now.Sub(c.Asked) < draftWatch
}

// Missing: the watch ran out without a draft.
func (c Compose) Missing(now time.Time) bool {
	return c.Ready.IsZero() && now.Sub(c.Asked) >= draftWatch
}

func (s *Server) loadComposes() {
	if data, err := os.ReadFile(filepath.Join(s.dir, "compose.json")); err == nil {
		json.Unmarshal(data, &s.composes)
	}
}

// saveComposes writes the jobs; call with s.mu held.
func (s *Server) saveComposes() {
	data, _ := json.MarshalIndent(s.composes, "", "  ")
	tmp := filepath.Join(s.dir, "compose.json.tmp")
	if os.WriteFile(tmp, data, 0o600) == nil {
		os.Rename(tmp, filepath.Join(s.dir, "compose.json"))
	}
}

func (s *Server) updateCompose(id string, f func(*Compose)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.composes {
		if s.composes[i].ID == id {
			f(&s.composes[i])
			s.saveComposes()
			return
		}
	}
}

// recentComposes: newest first, the last day's.
func (s *Server) recentComposes(now time.Time) []Compose {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Compose
	for i := len(s.composes) - 1; i >= 0 && len(out) < 3; i-- {
		if c := s.composes[i]; now.Sub(c.Asked) < 24*time.Hour {
			out = append(out, c)
		}
	}
	return out
}

// composePrompt is the routine's fire payload for a free-form brief.
func (s *Server) composePrompt(brief string) string {
	var b strings.Builder
	b.WriteString(composeMarker + "\n\n")
	b.WriteString("Mark typed this into Snap, the way he'd ask you in Claude Code:\n\n")
	b.WriteString(brief + "\n\n")
	fmt.Fprintf(&b, "Today is %s (Mark's time zone: %s). His mailbox is %s.\n", s.now().In(s.tz).Format("Mon 2 Jan 2006, 15:04"), s.tz, s.cfg.Account)
	b.WriteString("Do it the way you would in a terminal session: search Gmail as widely as you need (company names, people, domains, subjects, his sent mail), open and read the whole threads that matter, and get the full picture before you write. Then save exactly ONE Gmail draft: a reply in the existing thread when there is one to continue, otherwise a new email. Never send.")
	return b.String()
}

// fireRoutine starts the draft routine with one payload.
func (s *Server) fireRoutine(text string) error {
	body, _ := json.Marshal(map[string]string{"text": text})
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

// handleCompose: POST /admin/compose.
func (s *Server) handleCompose(w http.ResponseWriter, r *http.Request) {
	if !s.CanDraft() {
		http.Error(w, "Drafting isn't set up: SNAP_DRAFT_ROUTINE and SNAP_DRAFT_TOKEN", http.StatusServiceUnavailable)
		return
	}
	brief := strings.TrimSpace(r.FormValue("brief"))
	if brief == "" {
		http.Error(w, "Type what Claude should draft", http.StatusBadRequest)
		return
	}
	if len(brief) > 4000 {
		brief = brief[:4000]
	}
	before, err := s.gmail.DraftIDs()
	if err != nil {
		log.Printf("Snap: compose: listing drafts: %v", err)
		http.Error(w, "Snap can't see the Gmail drafts: "+err.Error(), http.StatusBadGateway)
		return
	}
	if err := s.fireRoutine(s.composePrompt(brief)); err != nil {
		log.Printf("Snap: compose: %v", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.recordBrief("compose", brief, nil)
	var rb [6]byte
	rand.Read(rb[:])
	c := Compose{ID: hex.EncodeToString(rb[:]), Brief: brief, Asked: s.now(), Before: before}
	s.mu.Lock()
	s.composes = append(s.composes, c)
	if len(s.composes) > composeKeep {
		s.composes = s.composes[len(s.composes)-composeKeep:]
	}
	s.saveComposes()
	s.mu.Unlock()
	go s.watchCompose(c)
	back(w, r)
}

// watchCompose waits for the first draft that wasn't there at fire time.
func (s *Server) watchCompose(c Compose) {
	old := map[string]bool{}
	for _, id := range c.Before {
		old[id] = true
	}
	for end := c.Asked.Add(draftWatch); time.Now().Before(end); {
		time.Sleep(s.draftGap)
		drafts, err := s.gmail.Drafts()
		if err != nil {
			continue
		}
		for _, d := range drafts {
			if old[d.ID] || s.threadDrafting(d.Thread) {
				continue
			}
			subj, to := s.gmail.DraftHead(d.ID)
			s.updateCompose(c.ID, func(c *Compose) {
				c.Ready, c.Thread, c.Message, c.Subject, c.To, c.Before = time.Now(), d.Thread, d.Message, subj, to, nil
			})
			return
		}
	}
	s.updateCompose(c.ID, func(c *Compose) { c.Before = nil })
}

// threadDrafting: a thread reply is being drafted there (not this job's).
func (s *Server) threadDrafting(id string) bool {
	t, ok := s.store.Get(id)
	return ok && t.Drafting(s.now())
}

// DraftRef is one Gmail draft.
type DraftRef struct{ ID, Message, Thread string }

// Drafts lists the newest drafts (Gmail lists newest first).
func (g *Gmail) Drafts() ([]DraftRef, error) {
	var r struct {
		Drafts []struct {
			ID      string `json:"id"`
			Message struct {
				ID       string `json:"id"`
				ThreadID string `json:"threadId"`
			} `json:"message"`
		} `json:"drafts"`
	}
	if err := g.get("/drafts", url.Values{"maxResults": {"50"}}, &r); err != nil {
		return nil, err
	}
	out := make([]DraftRef, 0, len(r.Drafts))
	for _, d := range r.Drafts {
		out = append(out, DraftRef{d.ID, d.Message.ID, d.Message.ThreadID})
	}
	return out, nil
}

// DraftIDs: the ids of the newest drafts.
func (g *Gmail) DraftIDs() ([]string, error) {
	ds, err := g.Drafts()
	ids := make([]string, 0, len(ds))
	for _, d := range ds {
		ids = append(ids, d.ID)
	}
	return ids, err
}

// DraftHead: a draft's subject and first recipient, best effort.
func (g *Gmail) DraftHead(id string) (subject, to string) {
	var d struct {
		Message gMessage `json:"message"`
	}
	if g.get("/drafts/"+url.PathEscape(id), url.Values{"format": {"metadata"}}, &d) != nil {
		return "", ""
	}
	for _, h := range d.Message.Payload.Headers {
		switch strings.ToLower(h.Name) {
		case "subject":
			subject = h.Value
		case "to":
			to = h.Value
		}
	}
	if a, err := mail.ParseAddressList(to); err == nil && len(a) > 0 {
		to = a[0].Name
		if to == "" {
			to = a[0].Address
		}
		if len(a) > 1 {
			to += fmt.Sprintf(" +%d", len(a)-1)
		}
	}
	return subject, to
}

// ComposeRow is a job as the page shows it.
type ComposeRow struct {
	Compose
	Working, Missing bool
	Link             string
}

func (s *Server) composeRows(now time.Time) []ComposeRow {
	var out []ComposeRow
	for _, c := range s.recentComposes(now) {
		r := ComposeRow{Compose: c, Working: c.Working(now), Missing: c.Missing(now)}
		if c.Thread != "" {
			r.Link = "https://mail.google.com/mail/u/?authuser=" + s.cfg.Account + "#drafts/" + c.Thread
		}
		out = append(out, r)
	}
	return out
}
