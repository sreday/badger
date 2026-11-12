package snap

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestTopics(t *testing.T) {
	s, err := New(Config{Base: "/snap", DataDir: t.TempDir(), AdminPassword: "pw", Account: "mark@sreday.com", Every: time.Hour, TZ: "Europe/London",
		TeamDomains: []string{"sreday.com"}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	read := now.Add(-48 * time.Hour)
	ing := []Person{{Name: "Ola", Email: "ola@ing.pl"}, {Name: "Mark", Email: "mark@sreday.com"}}
	// A: ING asked for an offer; Claude flagged it ("ball with you") two days ago.
	s.store.Update("a", func(x *Thread) {
		*x = Thread{ID: "a", Subject: "Oferta SREday Warsaw", HistoryID: "1", ClaudeHistory: "1", AnalyzedHistory: "1", AnalyzedBy: "claude", AnalyzedAt: read,
			LastAt: now.Add(-72 * time.Hour), LastFrom: "Ola", People: ing, Category: "sponsors", Loose: true, Waiting: "you", Label: "ING offer requested",
			Topic: "ing poland / sponsorship / sreday warsaw 2026", Company: "ING Poland"}
	})
	// B: Mark sent the offer yesterday, in a new thread nobody flagged.
	s.store.Update("b", func(x *Thread) {
		*x = Thread{ID: "b", Subject: "SREday Warsaw - official offer", HistoryID: "7", LastAt: now.Add(-24 * time.Hour), LastFrom: "me", People: ing, Category: "sponsors"}
	})
	// C: same company, different matter (false friend): hosting.
	s.store.Update("c", func(x *Thread) {
		*x = Thread{ID: "c", Subject: "Hosting a meetup at ING", HistoryID: "3", ClaudeHistory: "3", AnalyzedHistory: "3", AnalyzedBy: "claude", AnalyzedAt: now,
			LastAt: now.Add(-10 * 24 * time.Hour), LastFrom: "Ola", People: ing, Category: "events", Loose: true, Waiting: "you", Label: "ING meetup hosting",
			Topic: "ing poland / hosting / meetup", Company: "ING Poland"}
	})
	s.markRelated()
	a, _ := s.store.Get("a")
	if !a.Pending() {
		t.Fatal("A should go back to Claude: Mark acted in a related thread after its last read")
	}
	all := s.store.All()
	if blk := s.relatedBlock(a, all); !strings.Contains(blk, "SREday Warsaw - official offer") || !strings.Contains(blk, "last message from Mark") {
		t.Fatalf("related block: %s", blk)
	}
	h := s.Handler()
	// Claude reads A again: the topic moved on; B gets the same topic.
	ans, _ := json.Marshal(map[string]any{"model": "claude", "threads": []map[string]any{
		{"id": "a", "history": "1", "category": "sponsors", "loose_end": true, "topic": "ing poland / sponsorship / sreday warsaw 2026", "waiting_on": "them",
			"label": "ING Warsaw offer sent", "why": "Offer sent 11 Nov in a separate thread; waiting on ING.", "actions": []map[string]any{{"title": "Nudge Ola on the offer", "kind": "reply"}}},
	}})
	if rec := call(t, h, "POST", "/api/answers", "Bearer "+s.token, string(ans)); rec.Code != 200 {
		t.Fatalf("answers: %d %s", rec.Code, rec.Body)
	}
	s.store.Update("b", func(x *Thread) {
		x.Topic, x.AnalyzedBy, x.AnalyzedAt, x.ClaudeHistory, x.AnalyzedHistory = "ing poland / sponsorship / sreday warsaw 2026", "claude", now.Add(-time.Hour), "7", "7"
	})
	body := call(t, h, "GET", "/?view=sponsors", "pw", "").Body.String()
	if strings.Count(body, `class="th `) != 1 || !strings.Contains(body, "ING Warsaw offer sent") || !strings.Contains(body, "1 more email") || !strings.Contains(body, "SREday Warsaw - official offer") {
		t.Fatalf("one card for the topic, with its other email:\n%s", body)
	}
	// Done closes the whole topic, not the false friend.
	if rec := call(t, h, "POST", "/admin/thread/a/done", "pw", "done=1"); rec.Code != 204 {
		t.Fatalf("done: %d", rec.Code)
	}
	b, _ := s.store.Get("b")
	c, _ := s.store.Get("c")
	if !b.Done || c.Done {
		t.Fatalf("done spread wrong: b %v, c %v", b.Done, c.Done)
	}
	if body := call(t, h, "GET", "/?view=events", "pw", "").Body.String(); !strings.Contains(body, "ING meetup hosting") {
		t.Fatal("the hosting topic must stay its own card")
	}
	// An older flagged read is hidden once the topic's newest read says it's settled.
	s.store.Update("a", func(x *Thread) { x.Done = false })
	s.store.Update("b", func(x *Thread) { x.Done, x.Loose, x.AnalyzedAt = false, false, now.Add(time.Minute) })
	if body := call(t, h, "GET", "/?view=sponsors", "pw", "").Body.String(); strings.Contains(body, "ING Warsaw offer sent") {
		t.Fatal("a settled topic still shows its older card")
	}
	if !strings.Contains(Instructions, "JUDGE THE TOPIC") || !strings.Contains(Instructions, "false friends") {
		t.Fatal("instructions miss the topic rules")
	}
}
