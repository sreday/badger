package snoop

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClaudeReadsCalls(t *testing.T) {
	s := newTestServer(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	s.claude.eventsAt = now // no GitHub in tests
	s.claude.events = "\n\nUPCOMING EVENTS:\n- SREday NYC, 12 Mar 2027\n"
	var lines []Line
	for i := 0; i < 40; i++ {
		lines = append(lines, Line{At: now.Add(-50 * time.Minute), Speaker: "Jane Doe", Text: "We would love a booth at SREday NYC, I need to check the budget with my VP and send you the numbers next week"})
	}
	s.store.Update("c1", func(c *Call) {
		*c = Call{ID: "c1", Title: "Acme x SREday", Start: now.Add(-time.Hour), End: now.Add(-30 * time.Minute), EventID: "ev1", Owned: true,
			TranscriptDone: true, APILines: lines, Calendly: true, CalendlyEvent: "Sponsor SREday", CalendlyAnswers: map[string]string{"your company": "Acme"},
			APINotes: &Notes{Summary: "Acme wants a booth.", NextSteps: []string{"Send the prospectus"}, By: "Gemini in Meet"}, Ticked: map[int]bool{0: true}}
	})
	h := s.Handler()
	get := func(path, auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := get("/api/claude/queue", "wrong"); rec.Code != 401 {
		t.Fatalf("no token: %d", rec.Code)
	}
	rec := get("/api/claude/queue", s.claude.token)
	var q struct {
		Instructions string       `json:"instructions"`
		Calls        []claudeItem `json:"calls"`
	}
	json.Unmarshal(rec.Body.Bytes(), &q)
	if len(q.Calls) != 1 || !strings.Contains(q.Calls[0].Input, "check the budget") || !strings.Contains(q.Calls[0].Input, "Already done (Mark ticked these off): Send the prospectus") ||
		!strings.Contains(q.Calls[0].Input, "your company: Acme") || !strings.Contains(q.Instructions, "OUR WORDS") || !strings.Contains(q.Instructions, "SREday NYC, 12 Mar 2027") {
		t.Fatalf("queue: %s", rec.Body)
	}
	ans, _ := json.Marshal(map[string]any{"model": "claude-fable-5-2", "calls": []map[string]any{{"id": "c1", "read": q.Calls[0].Read, "summary": "Acme wants a booth at SREday NYC; Jane checks budget with her VP.",
		"tldr": "Jane Doe runs marketing at Acme.", "links": []map[string]string{{"title": "LinkedIn", "url": "https://www.linkedin.com/in/janedoe"}, {"title": "bad", "url": "javascript:alert(1)"}},
		"ours":   []string{"Push: offer the Gold package at SREday NYC on 12 Mar and ask to lock it by 30 Nov"},
		"theirs": []string{"Jane: send the budget numbers by Fri 20 Nov"},
		"fixes":  []map[string]string{{"wrong": "booth", "right": "stand"}}, "cut": []int{2, 3, 999}, "company_facts": "NYC HQ."}}})
	req := httptest.NewRequest("POST", "/api/claude/answers", strings.NewReader(string(ans)))
	req.Header.Set("Authorization", "Bearer "+s.claude.token)
	out := httptest.NewRecorder()
	h.ServeHTTP(out, req)
	if !strings.Contains(out.Body.String(), `"applied":1`) {
		t.Fatalf("answers: %s", out.Body)
	}
	c, _ := s.store.Get("c1")
	if n := c.Notes(); n == nil || n.By != "Claude (claude-fable-5-2)" || len(n.NextSteps) != 2 || !strings.HasPrefix(n.NextSteps[0], "Push:") || !c.OurStep(n, 0) || c.OurStep(n, 1) || c.CompanyFacts != "NYC HQ." {
		t.Fatalf("notes: %+v", c.Notes())
	}
	if c.TLDR == "" || len(c.Links) != 1 || len(c.Cut) != 2 {
		t.Fatalf("tldr %q, links %+v, cut %v", c.TLDR, c.Links, c.Cut)
	}
	// The transcript Snoop shows: Claude's fix in, the cut lines out.
	if pl := c.ProofLines(); len(pl) != len(c.Lines())-2 || !strings.Contains(pl[0].Text, "love a stand at SREday") {
		t.Fatalf("proofread transcript: %d lines, %q", len(pl), pl[0].Text)
	}
	// Read once: nothing left until the call changes.
	json.Unmarshal(get("/api/claude/queue", s.claude.token).Body.Bytes(), &q)
	if len(q.Calls) != 0 {
		t.Fatalf("read again: %d", len(q.Calls))
	}
	s.store.Update("c1", func(c *Call) {
		c.APILines = append(c.APILines, Line{At: now, Speaker: "Jane Doe", Text: "One more thing"})
	})
	json.Unmarshal(get("/api/claude/queue", s.claude.token).Body.Bytes(), &q)
	if len(q.Calls) != 1 {
		t.Fatal("a changed call isn't queued again")
	}
}

func TestTrimFillerAndSides(t *testing.T) {
	for in, want := range map[string]string{
		"Uh, so, umm, we we want a booth, yyy, in in Berlin": "so, we want a booth, in Berlin",
		"Hmm.":                         "",
		"uhh... eee":                   "",
		"The summer umbrella is yummy": "The summer umbrella is yummy",
		"I evaluated Reliabar and reliabar's docs": "I evaluated Reliaburger and Reliaburger's docs",
	} {
		c := Call{Fixes: cleanFixes([]Fix{{"Reliabar", "Reliaburger"}, {"an", "x"}})}
		if got := trimFiller(c.proofer()(in)); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
	// Gemini's notes aren't sorted: the owner's name puts a step on a side.
	c := Call{Attendees: []Attendee{{Name: "Mark Pawlikowski", Email: "mark@sreday.com"}, {Name: "Dana Example", Email: "dana@example.com"}}}
	n := &Notes{NextSteps: []string{"[Mark Pawlikowski, Aleksandra Los] Discuss the proposal", "[Dana Example] Connect with Sam", "Send the deck"}}
	if !c.OurStep(n, 0) || c.OurStep(n, 1) || !c.OurStep(n, 2) || c.Guest() != "Dana" {
		t.Errorf("sides: %v %v %v, guest %q", c.OurStep(n, 0), c.OurStep(n, 1), c.OurStep(n, 2), c.Guest())
	}
	// Gemini's doc repeats its steps: once is enough.
	if got := ParseGeminiNotes("<h2>Suggested next steps</h2><ul><li>A step</li><li>A step</li></ul>"); len(got.NextSteps) != 1 {
		t.Errorf("repeated steps: %v", got.NextSteps)
	}
}

func TestClaudeFiltersTheCall(t *testing.T) {
	s := newTestServer(t)
	now := s.now()
	os.WriteFile(filepath.Join(s.cfg.DataDir, "snoop", "house.md"), []byte("Nothing off topic.\n"), 0o600) // the real rules live on the server
	s.store.Update("c1", func(c *Call) {
		*c = Call{ID: "c1", Title: "Acme", Start: now.Add(-3 * time.Hour), End: now.Add(-2 * time.Hour), EventID: "e1",
			Attendees: []Attendee{{Name: "Jane Doe", Email: "jane@acme.com"}},
			APINotes:  &Notes{Summary: "Jane talked about OFFTOPICA.", NextSteps: []string{"Send the deck"}, By: "Gemini in Meet"},
			APILines: []Line{{At: now, Speaker: "Jane", Text: "We want the Gold package for SREday London and the floor plan before we sign anything at all this quarter."},
				{At: now, Speaker: "Jane", Text: "Honestly OFFTOPICA is a mess and I can't stand any of it."},
				{At: now, Speaker: "Jane", Text: "The OFFTOPICB demonstrations blocked the roads so we could not reach the venue in time for the opening."}}}
	})
	page := func() string {
		r := httptest.NewRequest("GET", "/admin/call/c1", nil)
		r.SetBasicAuth("", "pw")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		r = httptest.NewRequest("GET", "/admin/call/c1/transcript", nil)
		r.SetBasicAuth("", "pw")
		w2 := httptest.NewRecorder()
		s.Handler().ServeHTTP(w2, r)
		return w.Body.String() + w2.Body.String()
	}
	// Claude is at work (it fetched the queue) and hasn't read the call: nothing raw shows, anywhere.
	req := httptest.NewRequest("GET", "/api/claude/queue", nil)
	req.Header.Set("Authorization", "Bearer "+s.claude.token)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var q struct {
		Instructions string       `json:"instructions"`
		Calls        []claudeItem `json:"calls"`
	}
	json.Unmarshal(rec.Body.Bytes(), &q)
	if len(q.Calls) != 1 || !strings.Contains(q.Calls[0].Input, "OFFTOPICA") || !strings.Contains(q.Instructions, "HOUSE RULES\nNothing off topic.") {
		t.Fatal("Claude should get the raw call and the house rules")
	}
	if p := page(); strings.Contains(p, "OFFTOPICA") || strings.Contains(p, "OFFTOPICB") || !strings.Contains(p, "Waiting for Claude") {
		t.Error("an unread call should show nothing raw, and say it waits for Claude")
	}
	c, _ := s.store.Get("c1")
	if v := s.view(&c, now); v.Summary != "" || len(s.SearchCalls("OFFTOPICA", 5)) != 0 {
		t.Error("an unread call's raw notes or lines reached Snap or the search")
	}
	r := httptest.NewRequest("GET", "/?q=OFFTOPICA", nil)
	r.SetBasicAuth("", "pw")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), "Nothing matches") {
		t.Error("the search found an unread call by its raw text")
	}
	// Claude's read: the off-limits line is cut, the mixed one reworded.
	ans := ClaudeAnswer{Read: q.Calls[0].Read, Summary: "Acme wants Gold at SREday London.", Cut: []int{2}}
	ans.Rewrites = append(ans.Rewrites, struct {
		Line int    `json:"line"`
		Text string `json:"text"`
	}{3, "Demonstrations blocked the roads so we could not reach the venue in time for the opening."})
	s.store.Update("c1", func(c *Call) { applyClaude(c, ans, "Claude", now) })
	p := page()
	if strings.Contains(p, "OFFTOPICA") || strings.Contains(p, "OFFTOPICB") || !strings.Contains(p, "Demonstrations blocked the roads") || !strings.Contains(p, "Gold package") {
		t.Error("after Claude's read: the cut line and the off-limits words should be gone, the rest shown")
	}
	c, _ = s.store.Get("c1")
	if d := DocHTML(&c, s.tz, ""); strings.Contains(d, "OFFTOPICA") || strings.Contains(d, "OFFTOPICB") {
		t.Error("the Drive doc carries off-limits words")
	}
}
