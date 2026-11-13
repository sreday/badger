package snap

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestSentReplyParksTheCard(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	threads := map[string][]fakeMsg{
		"deal": {{from: "Jane <jane@acme.com>", to: "mark@sreday.com", subject: "Gold package", at: now.Add(-5 * time.Hour), body: "Can you send the contract?"}},
		"side": {{from: "Jane <jane@acme.com>", to: "mark@sreday.com", subject: "Gold package logo", at: now.Add(-30 * time.Hour), body: "Here is our logo."}},
	}
	history := map[string]string{"deal": "10", "side": "20"}
	gm := fakeGmail(t, threads, history)
	defer gm.Close()
	s := newTestServer(t, now)
	clock := now
	s.now = func() time.Time { return clock }
	s.gmail = NewGmail(func() (string, error) { return "tok", nil })
	s.gmail.API = gm.URL
	s.gmail.Gap = 0
	s.google.tok = googleToken{RefreshToken: "r", Email: "mark@sreday.com"}
	for id, at := range map[string]time.Duration{"deal": 5 * time.Hour, "side": 30 * time.Hour} {
		th := flagged(id, "sponsors", "high", now.Add(-at))
		th.HistoryID, th.ClaudeHistory, th.AnalyzedHistory, th.Topic = history[id], history[id], history[id], "acme / sponsorship / sreday"
		th.Messages = 1
		th.Label, th.Actions = "Acme Gold contract owed", []Action{{Title: "Send the contract", Kind: "reply"}}
		s.store.Update(id, func(x *Thread) { *x = th })
	}
	h := s.Handler()
	page := func(path string) string { return call(t, h, "GET", path, "pw", "").Body.String() }
	if !strings.Contains(page("/"), `data-hot="1"`) {
		t.Fatal("the deal should be hot before Mark answers")
	}
	// A draft is not an answer: the card stays where it is, Claude's read
	// stays on it, and nothing goes back to Claude.
	clock = now.Add(5 * time.Minute)
	threads["deal"] = append(threads["deal"], fakeMsg{from: "Mark <mark@sreday.com>", to: "Jane <jane@acme.com>", subject: "Re: Gold package", at: clock, draft: true, body: "Hey Jane, contract att"})
	history["deal"] = "10b"
	s.Sync(context.Background())
	d0, _ := s.store.Get("deal")
	if d0.Bucket(clock) != "active" || !d0.ByClaude() || !d0.HasDraft || d0.Pending() || d0.Label != "Acme Gold contract owed" {
		t.Fatalf("a draft moved the card: bucket %s by %s draft %v pending %v", d0.Bucket(clock), d0.AnalyzedBy, d0.HasDraft, d0.Pending())
	}
	if body := page("/"); !strings.Contains(body, `data-hot="1"`) || !strings.Contains(body, "Draft ready in Gmail") {
		t.Fatal("with only a draft written, the card should still be on the Hot plate, saying the draft is ready")
	}
	threads["deal"] = threads["deal"][:1] // he sends it: the draft becomes the sent message below

	// Mark answers in the thread (by hand or through Claude's draft: same thing to Gmail).
	clock = now.Add(10 * time.Minute)
	threads["deal"] = append(threads["deal"], fakeMsg{from: "Mark <mark@sreday.com>", to: "Jane <jane@acme.com>", subject: "Re: Gold package", at: clock.Add(-time.Minute), sent: true, body: "Hey Jane, contract attached."})
	history["deal"] = "11"
	s.Sync(context.Background())
	d, _ := s.store.Get("deal")
	if d.Bucket(clock) != "snoozed" || d.Waiting != "them" || !d.ByClaude() || d.Label != "Acme Gold contract owed" {
		t.Fatalf("after Mark's reply the card should sit in Snoozed with Claude's read kept: bucket %s waiting %s by %s label %q", d.Bucket(clock), d.Waiting, d.AnalyzedBy, d.Label)
	}
	if side, _ := s.store.Get("side"); side.Bucket(clock) != "snoozed" {
		t.Fatal("the topic's other email should be parked too")
	}
	if d.Queued(clock) {
		t.Fatal("a just-answered thread shouldn't go to Claude")
	}
	if body := page("/"); !strings.Contains(body, `data-hot="0"`) {
		t.Fatal("still on the Hot plate")
	}
	if !strings.Contains(page("/?view=sponsors&st=snoozed"), "Acme Gold contract owed") || strings.Contains(page("/?view=sponsors"), "Acme Gold contract owed") {
		t.Fatal("the card should be in Sponsors, Snoozed, and not in Active")
	}
	// A week of silence: it wakes, and Claude reads it again for the nudge.
	clock = now.Add(8 * 24 * time.Hour)
	d, _ = s.store.Get("deal")
	if d.Bucket(clock) != "active" || !d.Ripe(clock) {
		t.Fatalf("after the snooze: bucket %s ripe %v", d.Bucket(clock), d.Ripe(clock))
	}
	// Their reply before that brings it straight back.
	clock = now.Add(2 * time.Hour)
	threads["deal"] = append(threads["deal"], fakeMsg{from: "Jane <jane@acme.com>", to: "mark@sreday.com", subject: "Re: Gold package", at: clock.Add(-5 * time.Minute), body: "Thanks! One question on clause 4?"})
	history["deal"] = "12"
	s.Sync(context.Background())
	d, _ = s.store.Get("deal")
	if d.Bucket(clock) != "active" || !d.SentAt.IsZero() || !d.Queued(clock) {
		t.Fatalf("their reply should reopen it and send it to Claude: bucket %s sent %v queued %v", d.Bucket(clock), d.SentAt, d.Queued(clock))
	}
	if side, _ := s.store.Get("side"); side.Bucket(clock) != "active" {
		t.Fatal("the topic's other email should come back too")
	}
}
