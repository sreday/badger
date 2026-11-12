package snoop

import (
	"encoding/json"
	"net/http/httptest"
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
		!strings.Contains(q.Calls[0].Input, "your company: Acme") || !strings.Contains(q.Instructions, "NO LOOSE ENDS") || !strings.Contains(q.Instructions, "SREday NYC, 12 Mar 2027") {
		t.Fatalf("queue: %s", rec.Body)
	}
	ans, _ := json.Marshal(map[string]any{"model": "claude-fable-5-2", "calls": []map[string]any{{"id": "c1", "read": q.Calls[0].Read, "summary": "Acme wants a booth at SREday NYC; Jane checks budget with her VP.",
		"next_steps": []string{"Jane (Acme): send the budget numbers by Fri 20 Nov", "Push: offer the Gold package at SREday NYC on 12 Mar and ask to lock it by 30 Nov"}, "company_facts": "NYC HQ."}}})
	req := httptest.NewRequest("POST", "/api/claude/answers", strings.NewReader(string(ans)))
	req.Header.Set("Authorization", "Bearer "+s.claude.token)
	out := httptest.NewRecorder()
	h.ServeHTTP(out, req)
	if !strings.Contains(out.Body.String(), `"applied":1`) {
		t.Fatalf("answers: %s", out.Body)
	}
	c, _ := s.store.Get("c1")
	if n := c.Notes(); n == nil || n.By != "Claude (claude-fable-5-2)" || len(n.NextSteps) != 2 || !strings.HasPrefix(n.NextSteps[1], "Push:") || c.CompanyFacts != "NYC HQ." {
		t.Fatalf("notes: %+v", c.Notes())
	}
	// Read once: nothing left until the call changes.
	json.Unmarshal(get("/api/claude/queue", s.claude.token).Body.Bytes(), &q)
	if len(q.Calls) != 0 {
		t.Fatalf("read again: %d", len(q.Calls))
	}
	s.store.Update("c1", func(c *Call) { c.APILines = append(c.APILines, Line{At: now, Speaker: "Jane Doe", Text: "One more thing"}) })
	json.Unmarshal(get("/api/claude/queue", s.claude.token).Body.Bytes(), &q)
	if len(q.Calls) != 1 {
		t.Fatal("a changed call isn't queued again")
	}
}
