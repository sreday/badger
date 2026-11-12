package snap

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLearning(t *testing.T) {
	dir := t.TempDir()
	s, err := New(Config{Base: "/snap", DataDir: dir, AdminPassword: "pw", Account: "mark@sreday.com", Every: time.Hour, TZ: "Europe/London"})
	if err != nil {
		t.Fatal(err)
	}
	th := Thread{ID: "t1", Subject: "SREday NYC sponsorship", Company: "Acme", Category: "sponsors",
		Actions: []Action{{Title: "Nudge Jane on Gold"}, {Title: "Snooze until 6 Jan"}}}
	s.recordBrief("other", "Offer them the Amsterdam slot and a call next week", &th)
	s.recordBrief("compose", "find the ClickHouse thread and follow up", nil)
	lessons := s.briefLessons()
	for _, want := range []string{"Acme (sponsors), you offered: Nudge Jane on Gold; Snooze until 6 Jan", "Offer them the Amsterdam slot", "from scratch: \"find the ClickHouse thread"} {
		if !strings.Contains(lessons, want) {
			t.Fatalf("lessons lack %q:\n%s", want, lessons)
		}
	}
	// The routine's background check comes back and rides along next time.
	h := s.Handler()
	s.store.Update("t1", func(x *Thread) { *x = th; x.HistoryID = "5" })
	body, _ := json.Marshal(map[string]any{"model": "claude", "threads": []map[string]any{{"id": "t1", "history": "5", "category": "sponsors", "loose_end": true, "company": "Acme", "company_facts": "NYC HQ, one office; 200 people."}}})
	if rec := call(t, h, "POST", "/api/answers", "Bearer "+s.token, string(body)); rec.Code != 200 {
		t.Fatalf("answers: %d %s", rec.Code, rec.Body)
	}
	got, _ := s.store.Get("t1")
	s.mu.Lock()
	ctx := s.threadContextLocked(got)
	s.mu.Unlock()
	if !strings.Contains(ctx, "Known about Acme") || !strings.Contains(ctx, "NYC HQ") || !strings.Contains(ctx, "Mark's own brief on this thread") {
		t.Fatalf("context: %s", ctx)
	}
	// Both survive a restart.
	s2, err := New(Config{Base: "/snap", DataDir: dir, AdminPassword: "pw", Account: "mark@sreday.com", Every: time.Hour, TZ: "Europe/London"})
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.briefs) != 2 || s2.facts["acme"].Facts == "" {
		t.Fatalf("after restart: %d briefs, facts %+v", len(s2.briefs), s2.facts)
	}
	if !strings.Contains(Instructions, "ALWAYS exactly 3 options") || !strings.Contains(Instructions, "company_facts") {
		t.Fatal("instructions miss the new rules")
	}
}
