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

func TestRankFreshAndHigh(t *testing.T) {
	now := time.Now()
	day := 24 * time.Hour
	rows := []Row{
		{Thread: Thread{ID: "old-low", Priority: "low", LastAt: now.Add(-155 * day), Waiting: "them"}},
		{Thread: Thread{ID: "old-high-you", Priority: "high", LastAt: now.Add(-60 * day), Waiting: "you"}},
		{Thread: Thread{ID: "fresh-high", Priority: "high", LastAt: now.Add(-1 * day)}},
		{Thread: Thread{ID: "fresh-medium", Priority: "medium", LastAt: now.Add(-3 * day)}},
		{Thread: Thread{ID: "topic-fresh", Priority: "high", LastAt: now.Add(-40 * day)}, Fresh: now.Add(-2 * time.Hour)},
	}
	for i := range rows {
		if rows[i].Fresh.IsZero() {
			rows[i].Fresh = rows[i].LastAt
		}
	}
	best := func(a, b string) {
		var ra, rb int
		var fa, fb time.Time
		for _, r := range rows {
			if r.ID == a {
				ra, fa = rank(r, now), r.Fresh
			}
			if r.ID == b {
				rb, fb = rank(r, now), r.Fresh
			}
		}
		if ra > rb || (ra == rb && !fa.After(fb)) { // the page sorts by rank, then the freshest
			t.Errorf("%s (%d) should rank above %s (%d)", a, ra, b, rb)
		}
	}
	best("fresh-high", "fresh-medium")
	best("topic-fresh", "old-high-you") // a sibling's new mail makes the card fresh
	best("fresh-medium", "old-high-you")
	best("old-high-you", "old-low")
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
