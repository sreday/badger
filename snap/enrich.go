package snap

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// Enrich the thread (Marek 2026-11-13): Quick question is a chat ("Ask
// Claude"), and after a reply, "Enrich the thread" has Claude re-wire the
// card with what the chat established: a new headline, status and three
// options (applied at once, so the wheel changes live), a short work-safe
// trace of the chat kept on the thread for every later read, and a lesson
// for similar threads. It runs on the same routine as the questions.

// Enrich is one "Enrich the thread" job.
type Enrich struct {
	ID      string    `json:"id,omitempty"`
	At      time.Time `json:"at,omitempty"`
	Status  string    `json:"status,omitempty"`  // asking, done, failed
	History string    `json:"history,omitempty"` // the thread's history id when Marek clicked
	Trace   string    `json:"trace,omitempty"`   // what the chat established, as kept on the thread
	Note    string    `json:"note,omitempty"`    // what changed on the card, one line
	DoneAt  time.Time `json:"done_at,omitempty"`
	Error   string    `json:"error,omitempty"`
}

// Working: Claude is at it.
func (e Enrich) Working(now time.Time) bool {
	return e.Status == "asking" && now.Sub(e.At) <= qaTimeout
}

const enrichExpect = 90 // seconds, about

const enrichInstructions = `

An item with "mode": "enrich" is not a question. Mark chatted with you about this thread ("conversation": his questions and your answers, oldest first) and now asks you to ENRICH THE THREAD: re-wire its card with what the chat established. Read the conversation, the card as it stands ("thread": headline, status, summary, actions, known_context) and the emails; search his mailbox again if a fact needs checking. Then answer this item, in the same "answers" list, with:
BE BRIEF. Mark reads the result on a small card, and a wall of text is a failure: he wants the updated state, not the story of how you got there. No lists of names, no file names, no amounts he already knows, no "one correction to my earlier answer".
- "answer": what changed, in ONE short sentence, at most 15 words ("Offer already sent 30 Oct; the ball is with ING now.").
- "card": the card rewritten in that light, with the same fields and rules as "card_rules" describes for a flagged thread: loose_end, label, why, summary, waiting_on, ball_between, follow_up_on, priority, company, event, and actions (ALWAYS exactly 3 when loose_end is true, first = your recommendation, built on the new facts; apply "standing_rules" and use "upcoming_events"). If the chat showed the matter is settled and nothing is at stake any more, "loose_end": false and no actions. Do not include id, history, category or topic.
  In the card, "why" IS the updated state and replaces the old text: one or two short sentences, at most 35 words, in the present ("You sent ING the offer on 30 Oct. No reply for two weeks: the ball is with them."). Never append to the old text, never recount the chat.
- "trace": the one or two facts the chat established that a later read of this thread must know, in ONE sentence of at most 30 words ("Offer sent 30 Oct in a separate thread, 28,000 PLN; no contract sent yet."). It is kept with the thread for your own later reads, not shown to Mark, so keep it strictly to work: the deal, the people involved, dates, money, documents, decisions, promises. Leave out anything personal, off-topic, joking, rude or odd that came up in the chat, and never repeat Mark's wording of such things; if the chat held nothing new about the work, "trace": "".
- "lesson": optional. One short rule that would have made the card right the first time and will help on SIMILAR threads later, or "". "lesson_scope": "company" when it is a fact about how this company works ("ING procurement needs a PO before any invoice"), "general" when it is about how to read threads like this one ("Check his sent mail for an offer before saying the ball is with Mark").
- "sources": as for a question.

For a plain question (no "mode", or "mode": "ask"), "conversation" holds the earlier turns of the same chat: read them first, so a follow-up like "and what price did I quote?" is answered in context.`

// handleEnrich: POST /admin/thread/{id}/enrich.
func (s *Server) handleEnrich(w http.ResponseWriter, r *http.Request) {
	id, ok := s.known(w, r)
	if !ok {
		return
	}
	c := s.qq()
	if c.Routine == "" || c.Token == "" {
		http.Error(w, "The quick-question routine isn't connected yet: paste its token below", http.StatusServiceUnavailable)
		return
	}
	t, _ := s.store.Get(id)
	now := s.now()
	answered := 0
	for _, q := range t.QA {
		if q.Status == "answered" {
			answered++
		}
	}
	if answered == 0 {
		http.Error(w, "Ask Claude something first: there's nothing to enrich the thread with yet", http.StatusBadRequest)
		return
	}
	if t.Enrich.Working(now) {
		s.writeQA(w, id) // Claude is already on it
		return
	}
	var rb [6]byte
	rand.Read(rb[:])
	e := Enrich{ID: "e" + hex.EncodeToString(rb[:]), At: now, Status: "asking", History: t.HistoryID}
	text := fmt.Sprintf("%s %s\n\nMark asked to ENRICH the thread %q (Gmail thread id %s) with what your chat about it established. Fetch it from Snap as for a question (GET /snap/api/qa: it is the item with \"mode\": \"enrich\") and answer it as the \"instructions\" there say, in the usual POST /snap/api/qa, as one entry of \"answers\" that also carries its \"card\", \"trace\", \"lesson\" and \"lesson_scope\" fields.", qaMarker, e.ID, t.Subject, t.ID)
	if err := s.fireTrigger(c.Routine, c.Token, text); err != nil {
		log.Printf("Snap: enrich: %v", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.recordPick("enrich", "", &t)
	s.store.Update(id, func(t *Thread) { t.Enrich = e })
	s.writeQA(w, id)
}

// conversation: the chat so far, oldest first (answered turns only), up to
// the question with id before ("" = all of it).
func conversation(t Thread, before string) []map[string]string {
	out := []map[string]string{}
	for _, q := range t.QA {
		if before != "" && q.ID == before {
			break
		}
		if q.Status == "answered" {
			out = append(out, map[string]string{"asked": q.At.Format("2006-01-02 15:04"), "mark": q.Q, "claude": q.Answer})
		}
	}
	return out
}

// enrichAnswer is the extra an enrich answer carries.
type enrichAnswer struct {
	Card        *Analysis `json:"card"`
	Trace       string    `json:"trace"`
	Lesson      string    `json:"lesson"`
	LessonScope string    `json:"lesson_scope"`
}

// applyEnrich puts Claude's re-wired card on the thread. It reports the
// lesson to keep, if any (kept outside the store's lock).
func (s *Server) applyEnrich(t *Thread, answer string, a enrichAnswer, errText string, now time.Time) (lesson, scope, company string) {
	e := &t.Enrich
	e.DoneAt = now
	if strings.TrimSpace(answer) == "" && a.Card == nil && strings.TrimSpace(a.Trace) == "" {
		e.Status, e.Error = "failed", clip(errText, 300)
		if e.Error == "" {
			e.Error = "Claude came back without a card"
		}
		return
	}
	e.Status, e.Error, e.Note = "done", "", clip(answer, 160)
	e.Trace = clip(strings.TrimSpace(a.Trace), 240)
	if e.Trace != "" {
		t.Context = append(t.Context, fmt.Sprintf("%s (from your chat with Claude, %s)", e.Trace, now.In(s.tz).Format("2 Jan")))
		if len(t.Context) > 8 {
			t.Context = t.Context[len(t.Context)-8:]
		}
	}
	for i := range t.QA {
		if t.QA[i].Status == "answered" {
			t.QA[i].Applied = true
		}
	}
	// Mark asked for the card to be re-wired, so it is, at once (the state
	// he reads is the one the chat arrived at). With no card back, or when
	// mail came in while Claude worked, the thread also goes to Claude for a
	// fresh read, with the trace in its excerpt.
	moved := t.HistoryID != e.History
	if a.Card == nil {
		t.ClaudeHistory = ""
	} else {
		card := *a.Card
		card.ID, card.Category, card.Topic = t.ID, t.Category, t.Topic
		if card.Company == "" {
			card.Company = t.Company
		}
		if card.Event == "" {
			card.Event = t.Event
		}
		if card.CancelName == "" {
			card.CancelName, card.CancelEvent = t.CancelName, t.CancelEvent
		}
		if len(card.Actions) > 3 {
			card.Actions = card.Actions[:3]
		}
		by := t.AnalyzedBy
		if !t.ByClaude() {
			by = "claude"
		}
		listed, listedAt := t.StillListed, t.ListedAt
		applyAnalysis(t, card, t.HistoryID, by, now)
		if t.CancelName != "" {
			t.StillListed, t.ListedAt = listed, listedAt
		}
		if moved {
			t.ClaudeHistory = ""
		}
	}
	return strings.TrimSpace(a.Lesson), a.LessonScope, t.Company
}

// keepLesson files an enrich lesson: with the company's facts, or with the
// standing rules (tuning.go).
func (s *Server) keepLesson(lesson, scope, company string) {
	lesson = clip(lesson, 260)
	if lesson == "" {
		return
	}
	if scope == "company" && strings.TrimSpace(company) != "" {
		s.mu.Lock()
		old := s.facts[companyKey(company)].Facts
		s.mu.Unlock()
		if !strings.Contains(strings.ToLower(old), strings.ToLower(lesson)) {
			s.saveFacts(company, strings.TrimSpace(old+" "+lesson))
		}
		return
	}
	s.addRule(lesson)
}
