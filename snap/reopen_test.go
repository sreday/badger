package snap

import (
	"strings"
	"testing"
	"time"
)

func TestReplyReopens(t *testing.T) {
	now := time.Now()
	p := &ParsedThread{Subject: "Gold"}
	snoozed := func() *Thread {
		return &Thread{LastAt: now.Add(-48 * time.Hour), SnoozeUntil: now.AddDate(0, 0, 7), FollowUp: "2026-12-01"}
	}
	th := snoozed()
	if !applyMeta(th, p, Meta{LastAt: now, LastFrom: "Jane"}) || th.Bucket(now) != "active" || th.FollowUp != "" {
		t.Fatalf("their reply: %+v", th)
	}
	th = snoozed()
	if applyMeta(th, p, Meta{LastAt: now, LastFrom: "me"}) || th.Bucket(now) != "snoozed" {
		t.Fatalf("Mark's own send reopened it: %+v", th)
	}
	th = &Thread{LastAt: now.Add(-48 * time.Hour), Done: true}
	if !applyMeta(th, p, Meta{LastAt: now, LastFrom: "Jane"}) || th.Bucket(now) != "active" {
		t.Fatalf("closed: %+v", th)
	}
	th = snoozed()
	if applyMeta(th, p, Meta{LastAt: th.LastAt, LastFrom: "Jane"}) || th.Bucket(now) != "snoozed" {
		t.Fatal("no new mail reopened it")
	}
}

func TestBallBetween(t *testing.T) {
	var th Thread
	applyAnalysis(&th, Analysis{Loose: true, Label: "Kyndryl enquiry", Waiting: "between", Between: " Rod and Kyndryl "}, "1", "claude-fable-5-2", time.Now())
	if th.Between != "Rod and Kyndryl" {
		t.Fatalf("between: %q", th.Between)
	}
	applyAnalysis(&th, Analysis{Loose: true, Label: "x", Waiting: "you", Between: "Rod and Kyndryl"}, "2", "claude-fable-5-2", time.Now())
	if th.Between != "" {
		t.Fatal("kept between for ball with you")
	}
	if !strings.Contains(Instructions, `"ball_between"`[1:13]) {
		t.Fatal("instructions don't ask for it")
	}
}
