package snore

import (
	"testing"
	"time"
)

func TestLadderLevels(t *testing.T) {
	now := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
	ago := func(days int) time.Time { return now.Add(-time.Duration(days) * 24 * time.Hour) }
	for _, c := range []struct {
		kind string
		days int
		want int
	}{
		{TouchSponsor, 100, 7},
		{TouchSponsor, 800, 5},  // past two years: still known, no longer a partner
		{TouchSponsor, 1500, 0}, // past four years
		{TouchThread, 30, 6},
		{TouchThread, 200, 4}, // went quiet
		{TouchThread, 400, 0},
		{TouchSpoke, 500, 5},
		{TouchSpoke, 600, 0},
		{TouchReplied, 100, 4},
		{TouchReplied, 200, 0},
		{TouchCheckedIn, 300, 3},
		{TouchConnected, 100, 2},
		{TouchRegistered, 100, 2},
		{TouchSubmitted, 100, 2},
		{TouchInvited, 60, 1},
		{TouchInvited, 120, 0},
		{TouchSaved, 10, 1},
		{"unknown", 1, 0},
	} {
		if got := levelAt(Touch{Kind: c.kind, At: ago(c.days)}, now); got != c.want {
			t.Errorf("%s after %d days = level %d, want %d", c.kind, c.days, got, c.want)
		}
	}
}

func TestWarmthOf(t *testing.T) {
	now := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
	ago := func(days int) time.Time { return now.Add(-time.Duration(days) * 24 * time.Hour) }

	// Nothing on record: cold, the organizer's.
	if w := warmthOf(nil, now); w.Level != 0 || w.Owner != Organizer {
		t.Fatalf("no touches = %+v", w)
	}

	// A teammate's reply beats a registration; the teammate owns it.
	w := warmthOf([]Touch{
		{Company: "example.com", Person: "Ada Example", By: "teammate-a", Kind: TouchReplied, At: ago(20)},
		{Company: "example.com", Person: "Ben Example", Kind: TouchRegistered, At: ago(5)},
		{Company: "example.com", Person: "Cy Example", By: "teammate-b", Kind: TouchSaved, At: ago(3)},
	}, now)
	if w.Level != 4 || w.Owner != "teammate-a" || w.Best.Person != "Ada Example" {
		t.Fatalf("reply should lead: %+v", w)
	}
	if w.People != 2 { // the saved one is below level 2
		t.Fatalf("people = %d, want 2", w.People)
	}
	if w.Last.Person != "Cy Example" {
		t.Fatalf("last touch = %q", w.Last.Person)
	}
	if len(w.Proof) != 3 || w.Proof[0].Person != "Ada Example" {
		t.Fatalf("proof order = %+v", w.Proof)
	}

	// One person, several touches: the warmest one counts once.
	w = warmthOf([]Touch{
		{Company: "example.com", Person: "Ada Example", Profile: "ada", By: "teammate-a", Kind: TouchConnected, At: ago(40)},
		{Company: "example.com", Person: "Ada  EXAMPLE", Profile: "ada", By: "teammate-a", Kind: TouchReplied, At: ago(30)},
	}, now)
	if w.Level != 4 || w.People != 1 {
		t.Fatalf("one person twice: %+v", w)
	}

	// A registrant with no teammate: the teammate who knows someone else
	// there acts; with nobody, the organizer.
	w = warmthOf([]Touch{
		{Company: "example.com", Person: "Ben Example", Kind: TouchCheckedIn, At: ago(10)},
		{Company: "example.com", Person: "Dee Example", By: "teammate-b", Kind: TouchConnected, At: ago(50)},
	}, now)
	if w.Level != 3 || w.Owner != "teammate-b" {
		t.Fatalf("closest teammate should act: %+v", w)
	}
	w = warmthOf([]Touch{{Company: "example.com", Person: "Ben Example", Kind: TouchCheckedIn, At: ago(10)}}, now)
	if w.Owner != Organizer {
		t.Fatalf("nobody on the team: owner = %q", w.Owner)
	}

	// A past sponsor is the organizer's, whoever else knows people there.
	w = warmthOf([]Touch{
		{Company: "example.com", Kind: TouchSponsor, Event: "2026-city", At: ago(200)},
		{Company: "example.com", Person: "Ada Example", By: "teammate-a", Kind: TouchReplied, At: ago(5)},
	}, now)
	if w.Level != 7 || w.Owner != Organizer {
		t.Fatalf("past sponsor: %+v", w)
	}

	// Everything faded: cold again.
	w = warmthOf([]Touch{{Company: "example.com", Person: "Ada Example", By: "teammate-a", Kind: TouchInvited, At: ago(200)}}, now)
	if w.Level != 0 || w.Owner != Organizer || len(w.Proof) != 0 {
		t.Fatalf("faded: %+v", w)
	}
}

func TestWarmthStale(t *testing.T) {
	now := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
	ago := func(days int) time.Time { return now.Add(-time.Duration(days) * 24 * time.Hour) }
	owned := func(days int) Warmth {
		return warmthOf([]Touch{{Company: "example.com", Person: "Ada Example", By: "teammate-a", Kind: TouchReplied, At: ago(days)}}, now)
	}
	if owned(10).Stale(now) {
		t.Fatal("ten days is not stale")
	}
	if !owned(20).Stale(now) {
		t.Fatal("twenty days without a touch is stale")
	}
	org := warmthOf([]Touch{{Company: "example.com", Kind: TouchSponsor, At: ago(60)}}, now)
	if org.Stale(now) {
		t.Fatal("the organizer's own companies carry no stale flag")
	}
}
