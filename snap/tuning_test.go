package snap

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTuningFromPicks(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	s := newTestServer(t, now)
	clock := now
	s.now = func() time.Time { return clock }
	th := flagged("acme", "sponsors", "high", now.Add(-time.Hour))
	th.Company = "Acme"
	th.Actions = []Action{{Title: "Nudge Jane"}, {Title: "Offer SREday NYC"}, {Title: "Snooze a week"}}
	s.store.Update("acme", func(x *Thread) { *x = th })
	h := s.Handler()
	bearer := "Bearer " + s.token
	queue := func() map[string]json.RawMessage {
		var q map[string]json.RawMessage
		json.Unmarshal(call(t, h, "GET", "/api/queue", bearer, "").Body.Bytes(), &q)
		return q
	}
	if string(queue()["tuning"]) != "null" {
		t.Fatal("a tuning job with nothing to learn from")
	}
	// Mark takes an option, then twice writes his own brief.
	s.recordPick("option", "", &th, 1)
	clock = clock.Add(time.Minute)
	s.recordPick("other", "Ask for a 15 minute call on Thursday, no more nudges", &th)
	clock = clock.Add(time.Minute)
	s.recordPick("question", "Did I already send them the prospectus?", &th)
	q := queue()
	var job TuneJob
	json.Unmarshal(q["tuning"], &job)
	if len(job.Misses) != 2 || job.Misses[0].Kind != "other" || job.Misses[0].Offered[0] != "Nudge Jane" || !strings.Contains(job.Misses[0].Wrote, "15 minute call") {
		t.Fatalf("tuning job: %+v", job)
	}
	if !strings.Contains(job.HitRate, "last 2 picks on cards, 1 were his own brief") {
		t.Fatalf("hit rate: %q", job.HitRate)
	}
	ans := `{"model":"m","threads":[],"tuning":{"reasons":["Acme: a nudge missed because he wanted to close; he wanted a call with a date"],"rules":["Quiet sponsors: first option asks for a short call with a date","Check OTHER THREADS for what Mark already sent"],"learnt":2}}`
	rec := call(t, h, "POST", "/api/answers", bearer, ans)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"tuning_learnt":2`) {
		t.Fatalf("answers: %d %s", rec.Code, rec.Body)
	}
	q = queue()
	if string(q["tuning"]) != "null" {
		t.Fatal("the same misses were handed out twice")
	}
	var ins string
	json.Unmarshal(q["instructions"], &ins)
	if !strings.Contains(ins, "WHAT YOU GOT WRONG BEFORE") || !strings.Contains(ins, "- Quiet sponsors: first option asks for a short call with a date") {
		t.Fatal("the rules aren't in the instructions")
	}
	// The menu shows them; Mark deletes one and it stays deleted.
	body := call(t, h, "GET", "/", "pw", "").Body.String()
	if !strings.Contains(body, "Other used 1 of the last 2 picks (Sponsors 1)") || !strings.Contains(body, "Quiet sponsors: first option") || !strings.Contains(body, "a nudge missed because") {
		t.Error("the Claude menu lacks the tuning corner")
	}
	if rec := call(t, h, "POST", "/admin/tuning/forget", "pw", "i=0"); rec.Code != 204 {
		t.Fatalf("forget: %d", rec.Code)
	}
	clock = clock.Add(time.Minute)
	s.recordPick("other", "Offer the Warsaw event instead", &th)
	json.Unmarshal(queue()["tuning"], &job)
	if len(job.Misses) != 1 || len(job.Rules) != 1 || len(job.Deleted) != 1 {
		t.Fatalf("after deleting a rule: %+v", job)
	}
	call(t, h, "POST", "/api/answers", bearer, `{"model":"m","threads":[],"tuning":{"rules":["Quiet sponsors: first option asks for a short call with a date","Offer the event nearest their office"],"learnt":1}}`)
	if v := s.tuneView(); len(v.Rules) != 1 || v.Rules[0] != "Offer the event nearest their office" {
		t.Fatalf("a deleted rule came back: %v", v.Rules)
	}
	// Twice Other on one thread: it says so in the thread's excerpt notes.
	s.recordBrief("other", "one", &th)
	s.recordBrief("other", "two", &th)
	s.mu.Lock()
	notes := s.threadContextLocked(th)
	s.mu.Unlock()
	if !strings.Contains(notes, "passed over your options 2 times") {
		t.Errorf("no word on the repeated misses: %q", notes)
	}
	// It all survives a restart.
	s2, err := New(s.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.picks) != 4 || len(s2.tuning.Rules) != 1 {
		t.Fatalf("after a restart: %d picks, %v", len(s2.picks), s2.tuning.Rules)
	}
}

func TestChatAndEnrich(t *testing.T) {
	now := time.Now().UTC()
	threads := map[string][]fakeMsg{"ing": {
		{from: "Ola <ola@ing.pl>", to: "mark@sreday.com", subject: "SREday Katowice x ING", at: now.Add(-5 * time.Hour), body: "We need a formal offer."},
	}}
	gm := fakeGmail(t, threads, map[string]string{"ing": "10"})
	defer gm.Close()
	fired := []string{}
	fire := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Text string }
		json.NewDecoder(r.Body).Decode(&in)
		fired = append(fired, in.Text)
		w.Write([]byte(`{"type":"routine_fire"}`))
	}))
	defer fire.Close()
	s, err := New(Config{Base: "/snap", DataDir: t.TempDir(), AdminPassword: "pw", Account: "mark@sreday.com", Every: time.Hour, TeamDomains: team, TZ: "Europe/London"})
	if err != nil {
		t.Fatal(err)
	}
	s.gmail = NewGmail(func() (string, error) { return "tok", nil })
	s.gmail.API = gm.URL
	s.fireBase = fire.URL + "/%s"
	s.store.Update("ing", func(th *Thread) {
		*th = flagged("ing", "sponsors", "high", now.Add(-5*time.Hour))
		th.Subject, th.Company, th.HistoryID, th.ClaudeHistory, th.AnalyzedHistory = "SREday Katowice x ING", "ING", "10", "10", "10"
		th.Label, th.Why = "ING Poland needs formal offer", "Ola asked for an offer; ball with Mark."
		th.Actions = []Action{{Title: "Send the offer", Kind: "reply"}, {Title: "Propose a call", Kind: "reply"}, {Title: "Snooze", Kind: "snooze"}}
	})
	h := s.Handler()
	bearer := "Bearer " + s.token
	call(t, h, "POST", "/admin/qq-token", "pw", "token=sk-ant-oat01-qqqqqqqqqqqqqqqqqqqq")

	// Nothing to enrich with before the first answer.
	if rec := call(t, h, "POST", "/admin/thread/ing/enrich", "pw", ""); rec.Code != 400 {
		t.Fatalf("enrich before any answer: %d", rec.Code)
	}
	ask := func(q string) string {
		rec := call(t, h, "POST", "/admin/thread/ing/ask", "pw", "q="+strings.ReplaceAll(q, " ", "+"))
		if rec.Code != 200 {
			t.Fatalf("ask: %d %s", rec.Code, rec.Body)
		}
		th, _ := s.store.Get("ing")
		return th.QA[len(th.QA)-1].ID
	}
	items := func() []qaItem {
		var out struct {
			Instructions string   `json:"instructions"`
			Questions    []qaItem `json:"questions"`
		}
		json.Unmarshal(call(t, h, "GET", "/api/qa", bearer, "").Body.Bytes(), &out)
		if !strings.Contains(out.Instructions, "ENRICH THE THREAD") {
			t.Fatal("the instructions don't cover enrich")
		}
		return out.Questions
	}
	q1 := ask("Did I send them the offer")
	// One question at a time: a second one waits for the answer.
	if rec := call(t, h, "POST", "/admin/thread/ing/ask", "pw", "q=And+the+price"); rec.Code != http.StatusConflict {
		t.Fatalf("a second question while the first is open: %d", rec.Code)
	}
	if rec := call(t, h, "GET", "/admin/thread/ing/qa", "pw", ""); !strings.Contains(rec.Body.String(), `"busy":true`) || !strings.Contains(rec.Body.String(), `"expect":75`) {
		t.Fatalf("the window should know Claude is busy, and how long it takes: %s", rec.Body)
	}
	if acts := s.activities(s.now()); len(acts) != 1 || acts[0].Label != "Answering your question" {
		t.Fatalf("activity: %+v", acts)
	}
	if it := items(); len(it) != 1 || it[0].Mode != "ask" || len(it[0].Conversation) != 0 {
		t.Fatalf("first question: %+v", it)
	}
	call(t, h, "POST", "/api/qa", bearer, `{"answers":[{"id":"`+q1+`","answer":"Yes: you sent the offer on 10 Nov in a separate thread.","context":"Offer sent 10 Nov."}]}`)
	if rec := call(t, h, "GET", "/admin/thread/ing/qa", "pw", ""); !strings.Contains(rec.Body.String(), `"can_enrich":true`) {
		t.Fatalf("after an answer the window should offer Enrich: %s", rec.Body)
	}
	// A follow-up carries the chat so far.
	q2 := ask("What price did I quote")
	it := items()
	if len(it) != 1 || len(it[0].Conversation) != 1 || !strings.Contains(it[0].Conversation[0]["claude"], "10 Nov") || it[0].Conversation[0]["mark"] != "Did I send them the offer" {
		t.Fatalf("follow-up without the conversation: %+v", it)
	}
	call(t, h, "POST", "/api/qa", bearer, `{"answers":[{"id":"`+q2+`","answer":"4,500 EUR for Gold."}]}`)

	// Enrich: the routine is started, the item carries the whole chat and the card rules.
	n := len(fired)
	if rec := call(t, h, "POST", "/admin/thread/ing/enrich", "pw", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"asking"`) {
		t.Fatalf("enrich: %d %s", rec.Code, rec.Body)
	}
	if len(fired) != n+1 || !strings.Contains(fired[n], "ENRICH") || !strings.HasPrefix(fired[n], qaMarker) {
		t.Fatalf("enrich didn't start the routine: %v", fired[n:])
	}
	call(t, h, "POST", "/admin/thread/ing/enrich", "pw", "") // a second click waits for the first
	if len(fired) != n+1 {
		t.Fatal("a second click started the routine again")
	}
	it = items()
	if len(it) != 1 || it[0].Mode != "enrich" || len(it[0].Conversation) != 2 || !strings.Contains(it[0].CardRules, "ALWAYS exactly 3") {
		t.Fatalf("enrich item: %+v", it)
	}
	if acts := s.activities(s.now()); len(acts) != 1 || acts[0].Label != "Enriching the thread" {
		t.Fatalf("activity: %+v", acts)
	}
	answer := map[string]any{"answers": []map[string]any{{
		"id": it[0].ID, "answer": "Offer already sent, so the ball is with ING; options now chase the PO.",
		"card": map[string]any{"loose_end": true, "label": "ING offer sent, awaiting PO", "why": "Offer sent 10 Nov at 4,500 EUR; ING has not answered.", "summary": "s", "waiting_on": "them", "priority": "high",
			"actions": []map[string]string{{"title": "Ask Ola for the PO", "kind": "reply", "prompt": "p"}, {"title": "Offer a call Friday", "kind": "reply", "prompt": "p"}, {"title": "Pitch a 2027 bundle", "kind": "reply", "prompt": "p"}}},
		"trace":  "Mark sent ING the offer on 10 Nov: 4,500 EUR for Gold.",
		"lesson": "ING procurement needs a PO before any invoice", "lesson_scope": "company",
	}}}
	body, _ := json.Marshal(answer)
	if rec := call(t, h, "POST", "/api/qa", bearer, string(body)); !strings.Contains(rec.Body.String(), `"applied":1`) {
		t.Fatalf("enrich answer: %s", rec.Body)
	}
	th, _ := s.store.Get("ing")
	if th.Label != "ING offer sent, awaiting PO" || th.Waiting != "them" || len(th.Actions) != 3 || th.Actions[0].Title != "Ask Ola for the PO" {
		t.Fatalf("the card wasn't re-wired: %+v", th)
	}
	if th.Category != "sponsors" || !th.ByClaude() || th.Pending() || th.Enrich.Status != "done" {
		t.Fatalf("after enrich: cat %q by %q pending %v enrich %+v", th.Category, th.AnalyzedBy, th.Pending(), th.Enrich)
	}
	if len(th.Context) != 1 || !strings.Contains(th.Context[0], "4,500 EUR") || !strings.Contains(th.Context[0], "from your chat with Claude") {
		t.Fatalf("no trace on the thread: %v", th.Context)
	}
	s.mu.Lock()
	notes := s.threadContextLocked(th)
	s.mu.Unlock()
	if !strings.Contains(notes, "ING procurement needs a PO") || !strings.Contains(notes, "4,500 EUR") {
		t.Fatalf("the lesson and trace should reach Claude's next read: %q", notes)
	}
	page := call(t, h, "GET", "/?view=sponsors&t=ing", "pw", "").Body.String()
	if !strings.Contains(page, "Ask Ola for the PO") || strings.Contains(page, "From your chat with Claude") || !strings.Contains(page, "Offer sent 10 Nov at 4,500 EUR") || !strings.Contains(page, "Enrich the thread") {
		t.Error("the page doesn't show the new wheel, the trace or the Enrich button")
	}
	// A chat with nothing about work leaves no trace; a general lesson becomes a rule.
	call(t, h, "POST", "/admin/thread/ing/enrich", "pw", "")
	it = items()
	call(t, h, "POST", "/api/qa", bearer, `{"answers":[{"id":"`+it[0].ID+`","answer":"Nothing new for the card.","trace":"","lesson":"Check his sent mail for an offer before saying the ball is with Mark","lesson_scope":"general"}]}`)
	th, _ = s.store.Get("ing")
	if len(th.Context) != 1 || th.Enrich.Status != "done" {
		t.Fatalf("an empty trace was kept: %v", th.Context)
	}
	if th.ClaudeHistory != "" {
		t.Error("with no card back, the thread should go to Claude for a fresh read")
	}
	if !strings.Contains(s.tuningBlock(), "Check his sent mail for an offer") {
		t.Error("a general lesson should become a standing rule")
	}
	// New mail while Claude was enriching: the card is left alone, the thread re-read.
	s.store.Update("ing", func(x *Thread) { x.ClaudeHistory = "10" })
	call(t, h, "POST", "/admin/thread/ing/enrich", "pw", "")
	it = items()
	s.store.Update("ing", func(x *Thread) { x.HistoryID = "11" })
	call(t, h, "POST", "/api/qa", bearer, `{"answers":[{"id":"`+it[0].ID+`","answer":"x","card":{"loose_end":true,"label":"STALE"},"trace":"A second fact."}]}`)
	th, _ = s.store.Get("ing")
	if th.Label != "STALE" || len(th.Context) != 2 || th.ClaudeHistory != "" {
		t.Fatalf("a stale card was applied: %q %v", th.Label, th.Context)
	}
}
