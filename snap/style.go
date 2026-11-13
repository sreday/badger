package snap

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Learning Marek's style from his sends (Marek 2026-11-12: "I need Snap to
// re-read drafts that end up being sent, and Claude to understand my style
// better with every send").
//
// When Claude's draft shows up in Gmail (a thread reply or a Draft with
// Claude email), Snap keeps its text. Every sync it looks again: once the
// draft is gone from Gmail's drafts and Mark's own message sits in its
// thread, the pair (what Claude wrote, what Mark sent) is kept. Two things
// learn from the pairs:
//   - every draft request carries the newest edited pairs verbatim, so the
//     very next draft already sees how he rewrote the last ones;
//   - the hourly reading routine distils all new pairs into Mark's style
//     notes (a short, growing rulebook), which also go out with every
//     draft request.
// Drafts sent unchanged count too: Claude hears what it got right.

const (
	keepSends   = 80
	sendWatch   = 21 * 24 * time.Hour // a draft not sent within 3 weeks is forgotten
	pairsInLine = 4                   // edited pairs that go out with each draft request
	maxNotes    = 25
)

// Send is one Claude draft and, once it went out, what Mark actually sent.
type Send struct {
	Draft    string    `json:"draft"` // Gmail draft id
	Thread   string    `json:"thread"`
	Kind     string    `json:"kind"`            // "reply" or "compose"
	Brief    string    `json:"brief,omitempty"` // what Claude was asked
	Subject  string    `json:"subject,omitempty"`
	At       time.Time `json:"at"` // when Snap saw the draft
	Claude   string    `json:"claude"`
	Sent     string    `json:"sent,omitempty"`
	SentAt   time.Time `json:"sent_at,omitempty"`
	Gone     bool      `json:"gone,omitempty"`     // deleted without a send
	Digested bool      `json:"digested,omitempty"` // in the style notes already
}

func (p Send) Open() bool { return p.Sent == "" && !p.Gone }

// Same: sent as Claude wrote it (spaces aside).
func (p Send) Same() bool {
	return strings.Join(strings.Fields(p.Claude), " ") == strings.Join(strings.Fields(p.Sent), " ")
}

// Style is the distilled rulebook.
type Style struct {
	Notes []string  `json:"notes"`
	At    time.Time `json:"at"`
	From  int       `json:"from"` // sends it was learned from, in all
}

func (s *Server) loadStyle() {
	if data, err := os.ReadFile(filepath.Join(s.dir, "sends.json")); err == nil {
		json.Unmarshal(data, &s.sends)
	}
	if data, err := os.ReadFile(filepath.Join(s.dir, "style.json")); err == nil {
		json.Unmarshal(data, &s.style)
	}
}

// DraftBody: a draft's text (no quoted thread, no signature) and subject.
func (g *Gmail) DraftBody(id string) (text, subject string, err error) {
	var d struct {
		Message gMessage `json:"message"`
	}
	if err = g.get("/drafts/"+url.PathEscape(id), url.Values{"format": {"full"}}, &d); err != nil {
		return "", "", err
	}
	for _, h := range d.Message.Payload.Headers {
		if strings.EqualFold(h.Name, "subject") {
			subject = h.Value
		}
	}
	return newText(bodyText(d.Message.Payload)), subject, nil
}

// keepDraft remembers Claude's draft as it was when it showed up.
func (s *Server) keepDraft(draftID, thread, kind, brief string) {
	s.mu.Lock()
	for _, p := range s.sends {
		if p.Draft == draftID {
			s.mu.Unlock()
			return
		}
	}
	s.mu.Unlock()
	text, subject, err := s.gmail.DraftBody(draftID)
	if err != nil || text == "" {
		if err != nil {
			log.Printf("Snap: reading Claude's draft %s: %v", draftID, err)
		}
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sends = append(s.sends, Send{Draft: draftID, Thread: thread, Kind: kind, Brief: clip(brief, 300), Subject: subject, At: s.now(), Claude: text})
	if len(s.sends) > keepSends {
		s.sends = s.sends[len(s.sends)-keepSends:]
	}
	writeJSON(s.dir, "sends.json", s.sends)
}

// draftInThread: the newest draft in a thread ("" if none).
func (s *Server) draftInThread(thread string) string {
	ds, err := s.gmail.Drafts()
	if err != nil {
		return ""
	}
	for _, d := range ds {
		if d.Thread == thread {
			return d.ID
		}
	}
	return ""
}

// replyBrief: what Claude was asked for on a thread, for the record.
func replyBrief(t Thread) string {
	if t.Label != "" {
		return "Reply: " + t.Label
	}
	return "Reply: " + t.Subject
}

// keepRecentDrafts picks up Claude's drafts made before this existed, or
// while Snap was restarting (best effort, at start).
func (s *Server) keepRecentDrafts() {
	if email, _ := s.google.Connected(); email == "" {
		return
	}
	now := s.now()
	for _, t := range s.store.All() {
		if t.HasDraft && !t.DraftAsked.IsZero() && now.Sub(t.DraftAsked) < logWindow {
			if id := s.draftInThread(t.ID); id != "" {
				s.keepDraft(id, t.ID, "reply", replyBrief(t))
			}
		}
	}
	for _, c := range s.recentComposes(now) {
		if c.Thread != "" {
			if id := s.draftInThread(c.Thread); id != "" {
				s.keepDraft(id, c.Thread, "compose", c.Brief)
			}
		}
	}
}

// checkSends: which kept drafts went out, and what Mark actually sent.
// Runs with every sync.
func (s *Server) checkSends() {
	s.mu.Lock()
	var open []Send
	for _, p := range s.sends {
		if p.Open() {
			open = append(open, p)
		}
	}
	s.mu.Unlock()
	if len(open) == 0 {
		return
	}
	ds, err := s.gmail.Drafts()
	if err != nil {
		return
	}
	still := map[string]bool{}
	for _, d := range ds {
		still[d.ID] = true
	}
	now := s.now()
	for _, p := range open {
		if still[p.Draft] {
			if now.Sub(p.At) > sendWatch {
				s.updateSend(p.Draft, func(p *Send) { p.Gone = true })
			}
			continue
		}
		th, err := s.gmail.Thread(p.Thread, true)
		if err != nil {
			continue
		}
		var sent *Mail
		for i := range th.Mails {
			if m := th.Mails[i]; m.FromMe && m.At.After(p.At.Add(-time.Hour)) && m.Text != "" {
				sent = &th.Mails[i] // the first of Mark's messages since the draft
				break
			}
		}
		if sent == nil {
			s.updateSend(p.Draft, func(p *Send) { p.Gone = true })
			continue
		}
		text, at := sent.Text, sent.At
		s.updateSend(p.Draft, func(p *Send) { p.Sent, p.SentAt = text, at })
	}
}

func (s *Server) updateSend(draft string, f func(*Send)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.sends {
		if s.sends[i].Draft == draft {
			f(&s.sends[i])
		}
	}
	writeJSON(s.dir, "sends.json", s.sends)
}

// styleBlock goes at the end of every draft request: the rulebook and the
// newest drafts Mark rewrote before sending.
func (s *Server) styleBlock() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	if len(s.style.Notes) > 0 {
		fmt.Fprintf(&b, "\n\nMARK'S STYLE, LEARNED FROM HOW HE EDITS CLAUDE'S DRAFTS BEFORE SENDING (%d sends so far). Follow these; they beat any general idea of his voice:\n", s.style.From)
		for _, n := range s.style.Notes {
			b.WriteString("- " + n + "\n")
		}
	}
	var pairs []Send
	same := 0
	for i := len(s.sends) - 1; i >= 0; i-- {
		p := s.sends[i]
		if p.Sent == "" {
			continue
		}
		if p.Same() {
			same++
			continue
		}
		if len(pairs) < pairsInLine {
			pairs = append(pairs, p)
		}
	}
	if len(pairs) > 0 {
		b.WriteString("\n\nHIS LATEST EDITS (newest first): what Claude drafted, then what Mark actually sent. Write it the way he sent it.\n")
		for _, p := range pairs {
			fmt.Fprintf(&b, "\n[%s, %s]\nCLAUDE WROTE:\n%s\nMARK SENT:\n%s\n", p.SentAt.In(s.tz).Format("2 Jan"), clip(p.Subject, 80), clipLines(p.Claude, 1200), clipLines(p.Sent, 1200))
		}
	}
	if same > 0 {
		fmt.Fprintf(&b, "\n(%d recent drafts went out exactly as Claude wrote them.)\n", same)
	}
	return b.String()
}

// clipLines shortens text but keeps its line breaks (the shape of an email is style too).
func clipLines(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// StyleJob is the queue's request to update the rulebook (nil when there's
// nothing new to learn from).
type StyleJob struct {
	Task  string      `json:"task"`
	Notes []string    `json:"notes"`
	Sends []StyleSend `json:"sends"`
}

// StyleSend is one pair as the routine sees it.
type StyleSend struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
	Asked   string `json:"asked,omitempty"`
	Claude  string `json:"claude"`
	Sent    string `json:"sent"`
	Same    bool   `json:"same"`
}

const styleTask = `Update Mark's style notes. "sends" are emails Claude drafted for Mark and what he actually sent ("same": true = he sent it untouched). Some pairs are corrections he made by hand on the draft in Snap ("asked" says so): the same kind of evidence, and the freshest. Compare each pair closely: what did he cut, add, reword, reorder, soften or sharpen; greeting, sign-off, length, line breaks, punctuation, spelling, how direct he is, how he asks, what he never says. Look at the SUBSTANCE as much as the style: what he put in that the draft lacked or took out that it had (an ask, an offer, a date, a deadline, a call, a next step, how much he commits to, who he brings in), and write notes for those too ("With a sponsor gone quiet he asks for a call with a day, he doesn't re-pitch"). Then return the FULL updated notes (not just the new ones): keep what still holds, sharpen notes the new pairs confirm, drop or fix any they contradict, add new ones. At most 25 notes, each one short, concrete and checkable ("Opens with 'Hey <first name>,' never 'Hi'"; "Cuts any sentence that thanks them for their time"), backed by what he actually did, never a guess. Put the strongest first. Never quote private details (names, prices, companies) in a note; describe the pattern.`

// styleJob: the pairs not learned from yet.
func (s *Server) styleJob() *StyleJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := &StyleJob{Task: styleTask, Notes: append([]string{}, s.style.Notes...)}
	for _, p := range s.sends {
		if p.Sent == "" || p.Digested {
			continue
		}
		asked := p.Brief
		if p.Kind == "correction" {
			asked = "(Mark corrected this draft by hand in Snap, before sending: \"sent\" is his version) " + asked
		}
		j.Sends = append(j.Sends, StyleSend{ID: p.Draft, Subject: p.Subject, Asked: asked, Claude: clipLines(p.Claude, 3000), Sent: clipLines(p.Sent, 3000), Same: p.Same()})
	}
	if len(j.Sends) == 0 {
		return nil
	}
	return j
}

// StyleAnswer is the routine's updated rulebook.
type StyleAnswer struct {
	Notes  []string `json:"notes"`
	Learnt []string `json:"learnt"` // the send ids it read
}

func (s *Server) applyStyle(a *StyleAnswer) int {
	if a == nil || len(a.Learnt) == 0 {
		return 0
	}
	var notes []string
	for _, n := range a.Notes {
		if n = strings.TrimSpace(n); n != "" && len(notes) < maxNotes {
			notes = append(notes, clip(n, 240))
		}
	}
	learnt := map[string]bool{}
	for _, id := range a.Learnt {
		learnt[id] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for i := range s.sends {
		if p := &s.sends[i]; learnt[p.Draft] && p.Sent != "" && !p.Digested {
			p.Digested = true
			n++
		}
	}
	if n == 0 {
		return 0
	}
	if len(notes) > 0 {
		s.style.Notes = notes
	}
	s.style.At = s.now()
	s.style.From += n
	writeJSON(s.dir, "sends.json", s.sends)
	writeJSON(s.dir, "style.json", s.style)
	return n
}
