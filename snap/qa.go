package snap

// Quick question: Marek asks Claude something about one thread ("didn't I
// send them the tentative contract?"). Snap fires the "Snap: quick question"
// routine, which reads the question here (GET /api/qa), searches his mail
// through Snap (GET /api/search, Snap's own read-only Gmail access, so the
// routine needs no Gmail connector) and posts the answer back (POST /api/qa).
// When the answer found context the card missed, "Update the thread with
// context" keeps it on the thread (Thread.Context): every later read of the
// thread gets it in the excerpt, and the thread is read again straight away.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// QA is one quick question on a thread.
type QA struct {
	ID         string     `json:"id"`
	Q          string     `json:"q"`
	At         time.Time  `json:"at"`
	Status     string     `json:"status"` // asking, answered, failed
	Answer     string     `json:"answer,omitempty"`
	Context    string     `json:"context,omitempty"` // what the card should know; empty = nothing new found
	Sources    []QASource `json:"sources,omitempty"`
	AnsweredAt time.Time  `json:"answered_at,omitempty"`
	Applied    bool       `json:"applied,omitempty"`
	Error      string     `json:"error,omitempty"`
}

// QASource is one email the answer rests on.
type QASource struct {
	Thread  string `json:"thread"`
	Subject string `json:"subject"`
	Date    string `json:"date"`
}

const (
	qaMarker  = "SNAP QUICK QUESTION"
	qaTimeout = 20 * time.Minute // no answer by then = failed
	qaKeep    = 12               // per thread
)

// qqConfig is the quick-question routine: from the env, or pasted once into
// the Quick question window (data/snap/qq.json).
type qqConfig struct {
	Routine string `json:"routine"`
	Token   string `json:"token"`
}

const qqRoutineDefault = "trig_019HZjxUBRYhDiJJFHdGVjdH" // "Snap: quick question"

func (s *Server) qq() qqConfig {
	c := qqConfig{Routine: os.Getenv("SNAP_QQ_ROUTINE"), Token: os.Getenv("SNAP_QQ_TOKEN")}
	var f qqConfig
	if data, err := os.ReadFile(filepath.Join(s.dir, "qq.json")); err == nil {
		json.Unmarshal(data, &f)
	}
	if f.Routine != "" {
		c.Routine = f.Routine
	}
	if f.Token != "" {
		c.Token = f.Token
	}
	if c.Routine == "" {
		c.Routine = qqRoutineDefault
	}
	return c
}

// CanAsk: the quick-question routine can be started.
func (s *Server) CanAsk() bool { c := s.qq(); return c.Routine != "" && c.Token != "" }

// handleQQToken: POST /admin/qq-token stores the routine's API token.
func (s *Server) handleQQToken(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimSpace(r.FormValue("token"))
	if len(tok) < 20 || strings.ContainsAny(tok, " \n\t") {
		http.Error(w, "That doesn't look like a routine token", http.StatusBadRequest)
		return
	}
	c := s.qq()
	c.Token = tok
	if rt := strings.TrimSpace(r.FormValue("routine")); strings.HasPrefix(rt, "trig_") {
		c.Routine = rt
	}
	writeJSON(s.dir, "qq.json", c)
	w.WriteHeader(http.StatusNoContent)
}

// qaPrompt is the fire payload: the routine's own prompt does the rest.
func (s *Server) qaPrompt(t Thread, q QA) string {
	return fmt.Sprintf("%s %s\n\nMark asked a quick question about the thread %q (Gmail thread id %s): fetch it from Snap and answer it as your prompt says.", qaMarker, q.ID, t.Subject, t.ID)
}

// handleAsk: POST /admin/thread/{id}/ask (q).
func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	id, ok := s.known(w, r)
	if !ok {
		return
	}
	q := strings.TrimSpace(r.FormValue("q"))
	if q == "" {
		http.Error(w, "Type a question", http.StatusBadRequest)
		return
	}
	if len(q) > 2000 {
		q = q[:2000]
	}
	c := s.qq()
	if c.Routine == "" || c.Token == "" {
		http.Error(w, "The quick-question routine isn't connected yet: paste its token below", http.StatusServiceUnavailable)
		return
	}
	var rb [6]byte
	rand.Read(rb[:])
	qa := QA{ID: hex.EncodeToString(rb[:]), Q: q, At: s.now(), Status: "asking"}
	t, _ := s.store.Get(id)
	if err := s.fireTrigger(c.Routine, c.Token, s.qaPrompt(t, qa)); err != nil {
		log.Printf("Snap: quick question: %v", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.recordBrief("question", q, &t)
	s.recordPick("question", q, &t)
	s.store.Update(id, func(t *Thread) {
		t.QA = append(t.QA, qa)
		if len(t.QA) > qaKeep {
			t.QA = t.QA[len(t.QA)-qaKeep:]
		}
	})
	s.writeQA(w, id)
}

// handleQA: GET /admin/thread/{id}/qa, polled by the window.
func (s *Server) handleQA(w http.ResponseWriter, r *http.Request) {
	id, ok := s.known(w, r)
	if !ok {
		return
	}
	s.writeQA(w, id)
}

func (s *Server) writeQA(w http.ResponseWriter, id string) {
	now := s.now()
	late := func(q QA) bool { return q.Status == "asking" && now.Sub(q.At) > qaTimeout }
	lateEnrich := func(e Enrich) bool { return e.Status == "asking" && now.Sub(e.At) > qaTimeout }
	if t, _ := s.store.Get(id); func() bool {
		for _, q := range t.QA {
			if late(q) {
				return true
			}
		}
		return lateEnrich(t.Enrich)
	}() {
		s.store.Update(id, func(t *Thread) {
			for i := range t.QA {
				if late(t.QA[i]) {
					t.QA[i].Status, t.QA[i].Error = "failed", "Claude didn't answer in time"
				}
			}
			if lateEnrich(t.Enrich) {
				t.Enrich.Status, t.Enrich.Error = "failed", "Claude didn't come back in time"
			}
		})
	}
	t, _ := s.store.Get(id)
	list := t.QA
	if list == nil {
		list = []QA{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	answered := false
	for _, q := range list {
		answered = answered || q.Status == "answered"
	}
	json.NewEncoder(w).Encode(map[string]any{"qa": list, "context": t.Context, "ready": s.CanAsk(), "enrich": t.Enrich, "can_enrich": answered && t.Enrich.Status != "asking"})
}

// handleApply: POST /admin/thread/{id}/qa/{qid}/apply keeps the answer's
// context on the thread and has Claude read the thread again with it.
func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	id, ok := s.known(w, r)
	if !ok {
		return
	}
	qid := r.PathValue("qid")
	found := false
	day := s.now().In(s.tz).Format("2 Jan")
	s.store.Update(id, func(t *Thread) {
		for i := range t.QA {
			q := &t.QA[i]
			if q.ID != qid || q.Context == "" {
				continue
			}
			found = true
			if !q.Applied {
				q.Applied = true
				t.Context = append(t.Context, fmt.Sprintf("%s (asked %s: %s)", q.Context, day, clip(q.Q, 120)))
				t.ClaudeHistory = "" // read it again, with the context
			}
		}
	})
	if !found {
		http.Error(w, "No context to add", http.StatusNotFound)
		return
	}
	s.runNow()
	s.writeQA(w, id)
}

// --- The routine's side ------------------------------------------------------

type qaItem struct {
	ID           string              `json:"id"`
	Mode         string              `json:"mode"` // ask, or enrich (enrich.go)
	Question     string              `json:"question"`
	Asked        string              `json:"asked"`
	Conversation []map[string]string `json:"conversation"` // the chat so far, oldest first
	Thread       map[string]any      `json:"thread"`
	Searches     []string            `json:"suggested_searches"`
	CardRules    string              `json:"card_rules,omitempty"`     // enrich: how a card is written
	Rules        []string            `json:"standing_rules,omitempty"` // enrich: the tuning rules
	Events       string              `json:"upcoming_events,omitempty"`
}

const qaInstructions = `Each item is a question Mark typed about one email thread in Snap. "thread" has what Snap's card says (headline, status, summary, actions: that is the read that may be missing something) and the thread's emails ("mails", newest last, with attachment names in "files").
Answer from his mailbox. Search it with GET /snap/api/search?q=<Gmail search query> (same bearer token, the q URL-encoded): Gmail search syntax works (from:, to:, has:attachment, filename:pdf, subject:, after:2026/09/01, "exact phrase", OR). "suggested_searches" are good first queries; run a few more of your own until you are sure (his SENT mail matters most: in:sent from:me). Each search returns up to 10 threads with their emails and attachment names.
Write "answer" for Mark: 1-4 plain sentences, the facts with dates ("Yes: you sent ING the tentative contract on 3 Nov, ING_Conf42_contract.pdf, in 'Conf42 x ING'"), or say plainly that you could not find it and what you searched.
Write "context" only when you found something the card should know and doesn't (a sent contract, an agreed price, a date, a promise, a later email in another thread): one or two factual sentences that make sense on their own when Claude reads the thread again later. Leave it "" when nothing new turned up.
"sources": the emails the answer rests on, {"thread": "<thread id from the search>", "subject": "...", "date": "YYYY-MM-DD"}.`

// mailBlock is a thread's emails for the routine.
func (s *Server) mailBlock(p *ParsedThread, textLimit int) []map[string]any {
	var out []map[string]any
	for _, m := range p.Mails {
		who := m.From.Address
		if m.From.Name != "" {
			who = m.From.Name + " <" + m.From.Address + ">"
		}
		if m.FromMe {
			who = "Mark (" + m.From.Address + ")"
		}
		var to []string
		for _, a := range m.To {
			to = append(to, a.Address)
		}
		text := m.Text
		if text == "" {
			text = m.Snippet
		}
		e := map[string]any{"date": m.At.In(s.tz).Format("2006-01-02 15:04"), "from": who, "to": to, "text": clip(text, textLimit)}
		if len(m.Files) > 0 {
			e["files"] = m.Files
		}
		out = append(out, e)
	}
	return out
}

// defaultSearches: the people's work domains and the company, a year back.
func defaultSearches(t Thread) []string {
	var out []string
	for _, d := range workDomains(t.People) {
		out = append(out, fmt.Sprintf("(from:%s OR to:%s) newer_than:400d", d, d))
	}
	if c := strings.TrimSpace(t.Company); c != "" {
		out = append(out, fmt.Sprintf("%q newer_than:400d", c))
		out = append(out, fmt.Sprintf("in:sent %q has:attachment", c))
	}
	return out
}

// handleQAQueue: GET /api/qa, the questions waiting for an answer.
func (s *Server) handleQAQueue(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	items := []qaItem{}
	for _, t := range s.store.All() {
		enrich := t.Enrich.Working(now)
		var th map[string]any
		block := func() map[string]any {
			if th != nil {
				return th
			}
			th = map[string]any{"id": t.ID, "subject": t.Subject, "headline": t.Label, "status": t.Why, "summary": t.Summary, "waiting": t.Waiting, "priority": t.Priority, "follow_up_on": t.FollowUp, "company": t.Company, "event": t.Event, "people": t.People, "actions": t.Actions, "known_context": t.Context}
			if p, err := s.gmail.Thread(t.ID, true); err == nil {
				th["mails"] = s.mailBlock(p, 2500)
			} else {
				th["mails_error"] = err.Error()
			}
			return th
		}
		for _, q := range t.QA {
			if q.Status != "asking" || now.Sub(q.At) > qaTimeout {
				continue
			}
			items = append(items, qaItem{ID: q.ID, Mode: "ask", Question: q.Q, Asked: q.At.In(s.tz).Format("Mon 2 Jan 2006 15:04"), Conversation: conversation(t, q.ID), Thread: block(), Searches: defaultSearches(t)})
		}
		if enrich {
			s.mu.Lock()
			rules := append([]string{}, s.tuning.Rules...)
			known := s.threadContextLocked(t)
			s.mu.Unlock()
			b := block()
			if known != "" {
				b["notes"] = known
			}
			items = append(items, qaItem{ID: t.Enrich.ID, Mode: "enrich", Question: "Enrich the thread: re-wire the card with what this chat established.", Asked: t.Enrich.At.In(s.tz).Format("Mon 2 Jan 2006 15:04"),
				Conversation: conversation(t, ""), Thread: b, Searches: defaultSearches(t), CardRules: Instructions, Rules: rules, Events: s.eventsList(now)})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"today":        now.In(s.tz).Format("Monday 2 January 2006"),
		"mailbox":      s.cfg.Account,
		"instructions": qaInstructions + enrichInstructions,
		"questions":    items,
	})
}

// handleSearch: GET /api/search?q= runs a Gmail search for the routine.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || len(q) > 500 {
		http.Error(w, "q: a Gmail search, up to 500 characters", http.StatusBadRequest)
		return
	}
	var res struct {
		Threads []ThreadRef `json:"threads"`
	}
	if err := s.gmail.get("/threads", url.Values{"q": {q}, "maxResults": {"10"}}, &res); err != nil {
		http.Error(w, "Gmail: "+err.Error(), http.StatusBadGateway)
		return
	}
	out := []map[string]any{}
	for _, ref := range res.Threads {
		p, err := s.gmail.Thread(ref.ID, true)
		if err != nil {
			continue
		}
		out = append(out, map[string]any{"thread": p.ID, "subject": p.Subject, "mails": s.mailBlock(p, 1500)})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"q": q, "threads": out})
}

// handleQAAnswers: POST /api/qa takes the answers.
func (s *Server) handleQAAnswers(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Answers []struct {
			ID      string     `json:"id"`
			Answer  string     `json:"answer"`
			Context string     `json:"context"`
			Sources []QASource `json:"sources"`
			Error   string     `json:"error"`
			enrichAnswer
		} `json:"answers"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		http.Error(w, "Bad JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	got := map[string]int{}
	for i, a := range in.Answers {
		got[a.ID] = i
	}
	applied, now := 0, s.now()
	type lesson struct{ text, scope, company string }
	var lessons []lesson
	s.store.UpdateAll(func(t *Thread) {
		if j, ok := got[t.Enrich.ID]; ok && t.Enrich.ID != "" && t.Enrich.Status == "asking" {
			a := in.Answers[j]
			l, scope, company := s.applyEnrich(t, a.Answer, a.enrichAnswer, a.Error, now)
			lessons = append(lessons, lesson{l, scope, company})
			applied++
		}
		for i := range t.QA {
			q := &t.QA[i]
			j, ok := got[q.ID]
			if !ok || q.Status == "answered" {
				continue
			}
			a := in.Answers[j]
			q.Answer, q.Context, q.Sources, q.AnsweredAt = clip(strings.TrimSpace(a.Answer), 2000), clip(strings.TrimSpace(a.Context), 800), a.Sources, now
			q.Status, q.Error = "answered", ""
			if q.Answer == "" {
				q.Status, q.Error = "failed", clip(a.Error, 300)
				if q.Error == "" {
					q.Error = "Claude came back without an answer"
				}
			}
			applied++
		}
	})
	for _, l := range lessons {
		s.keepLesson(l.text, l.scope, l.company)
	}
	if len(lessons) > 0 {
		s.checkLineups(now)
		s.runNow() // a thread sent back for a fresh read gets its excerpt
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"applied": applied})
}
