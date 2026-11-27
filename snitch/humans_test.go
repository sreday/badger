package snitch

import (
	"testing"
	"time"
)

func TestHumanWeeks(t *testing.T) {
	loc := time.UTC
	at := func(s string) time.Time { v, _ := time.Parse("2006-01-02 15:04", s); return v }
	evs := []humanEvent{
		// account 3, Monday of 2026-W48: a 30 min stretch, a pause, a 10 min stretch
		{acct: 3, created: at("2026-11-23 10:00"), updated: at("2026-11-23 10:00"), tag: "visit"},
		{acct: 3, created: at("2026-11-23 10:15"), updated: at("2026-11-23 10:15"), tag: "messaged", note: "short"},
		{acct: 3, created: at("2026-11-23 10:30"), updated: at("2026-11-23 10:30"), tag: "visit", note: "a much longer comment"},
		{acct: 3, created: at("2026-11-23 12:00"), updated: at("2026-11-23 12:00"), tag: "visit", note: "Imported from a sheet"},
		{acct: 3, created: at("2026-11-23 12:10"), updated: at("2026-11-23 12:10"), tag: "visit", note: "A MUCH  longer comment"},
		// account 4: a row made the week before, changed this week
		{acct: 4, created: at("2026-11-18 09:00"), updated: at("2026-11-24 09:00"), tag: "interested"},
		// 2025: left out
		{acct: 4, created: at("2025-06-01 09:00"), updated: at("2025-06-01 09:00"), tag: "visit"},
	}
	ws := humanWeeks(evs, loc)
	if len(ws) != 2 || ws[0].ID != "2026-W48" || ws[1].ID != "2026-W47" {
		t.Fatalf("weeks: %+v", ws)
	}
	a := ws[0].Accts[0]
	if a.Acct != 3 || a.Minutes != 30+2+10+2 || a.Days[0] != a.Minutes {
		t.Fatalf("account 3: %+v", a)
	}
	if a.Created["visit"] != 4 || a.Created["messaged"] != 1 {
		t.Fatalf("tags: %+v", a.Created)
	}
	if len(a.Comments) != 2 || a.Comments[0] != "a much longer comment" {
		t.Fatalf("comments: %q", a.Comments)
	}
	b := ws[0].Accts[1]
	if b.Acct != 4 || b.Updated["interested"] != 1 || b.Minutes != 2 {
		t.Fatalf("account 4: %+v", b)
	}
	if ws[1].Accts[0].Created["interested"] != 1 {
		t.Fatalf("last week: %+v", ws[1].Accts[0])
	}
}
