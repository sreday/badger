package snitch

import (
	"net/http"
	"strings"
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
	ws := humanWeeks(evs, nil, loc)
	if len(ws) != 2 || ws[0].ID != "2026-W48" || ws[1].ID != "2026-W47" {
		t.Fatalf("weeks: %+v", ws)
	}
	a := ws[0].Accts[0]
	if a.Acct != 3 || a.Minutes != 10+10+10+20 || a.Days[0] != a.Minutes {
		t.Fatalf("account 3: %+v", a)
	}
	if a.Created["visit"] != 4 || a.Created["messaged"] != 1 {
		t.Fatalf("tags: %+v", a.Created)
	}
	if len(a.Comments) != 2 || a.Comments[0].Text != "a much longer comment" {
		t.Fatalf("comments: %q", a.Comments)
	}
	b := ws[0].Accts[1]
	if b.Acct != 4 || b.Updated["interested"] != 1 || b.Minutes != 10 {
		t.Fatalf("account 4: %+v", b)
	}
	if ws[1].Accts[0].Created["interested"] != 1 {
		t.Fatalf("last week: %+v", ws[1].Accts[0])
	}
}

func TestHumanTickets(t *testing.T) {
	at := func(s string) time.Time { v, _ := time.Parse("2006-01-02 15:04", s); return v }
	evs := []humanEvent{
		{acct: 3, created: at("2026-11-10 10:00"), updated: at("2026-11-10 10:00"), tag: "messaged", li: "p1"},
		{acct: 4, created: at("2026-11-20 10:00"), updated: at("2026-11-20 10:00"), tag: "messaged", li: "p1"}, // last before booking
		{acct: 3, created: at("2026-11-25 10:00"), updated: at("2026-11-25 10:00"), tag: "messaged", li: "p1"}, // after: no credit
		{acct: 5, created: at("2026-11-20 10:00"), updated: at("2026-11-20 10:00"), tag: "visit", li: "p2"},    // a visit only
	}
	regs := []humanReg{
		{li: "p1", at: at("2026-11-24 09:00"), paid: true},
		{li: "p2", at: at("2026-11-24 09:00")},
		{li: "p3", at: at("2026-11-24 09:00")},
	}
	ws := humanWeeks(evs, regs, time.UTC)
	w := ws[0]
	if w.ID != "2026-W48" || w.Free != 2 || w.Paid != 1 {
		t.Fatalf("week: %+v", w)
	}
	for _, a := range w.Accts {
		switch a.Acct {
		case 4:
			if a.Paid != 1 || a.Free != 0 {
				t.Fatalf("account 4: %+v", a)
			}
		default:
			if a.Paid+a.Free != 0 {
				t.Fatalf("account %d credited: %+v", a.Acct, a)
			}
		}
	}
}

func TestHumansTeamAndPage(t *testing.T) {
	team := HumansTeam{Keys: map[string]string{"hum_ab": "Ann"}, Accounts: map[string]string{"3": "Ann", "4": "Bob"}}
	// key prefixes readable: they decide; account 4 isn't on the list
	if n := teamNames(team, map[int]string{3: "hum_ab12", 4: "hum_cd34"}); len(n) != 1 || n[3] != "Ann" {
		t.Fatalf("by key: %v", n)
	}
	// not readable: the account list
	if n := teamNames(team, nil); len(n) != 2 || n[4] != "Bob" {
		t.Fatalf("by account: %v", n)
	}
	at := func(s string) time.Time { v, _ := time.Parse("2006-01-02 15:04", s); return v }
	evs := []humanEvent{
		{acct: 3, created: at("2026-11-16 10:00"), updated: at("2026-11-16 10:00"), tag: "can't come", note: "hello"},
		{acct: 9, created: at("2026-11-16 10:00"), updated: at("2026-11-16 10:00"), tag: "visit"},
	}
	weeks := onlyTeam(humanWeeks(evs, nil, time.UTC), map[int]string{3: "Ann"})
	if len(weeks) != 1 || len(weeks[0].Accts) != 1 || weeks[0].Accts[0].Name != "Ann" {
		t.Fatalf("only the team: %+v", weeks)
	}
	if c := tagClass("can't come"); c != "t-cant-come" {
		t.Fatalf("class %q", c)
	}

	s, h, _ := testServer(t)
	s.humans.data = HumansData{At: at("2026-11-18 09:00"), Weeks: weeks, Team: []string{"Ann", "Bob"}}
	r := call(t, h, "GET", "/humans", nil, func(r *http.Request) { r.SetBasicAuth("admin", "pw") })
	if r.code != 200 || !strings.Contains(r.body, "Ann") || !strings.Contains(r.body, "t-cant-come") || !strings.Contains(r.body, "Bob") || !strings.Contains(r.body, "paid") || strings.Contains(r.body, "<select") {
		t.Fatalf("page %d: %s", r.code, r.body)
	}
}

func TestHighlightURL(t *testing.T) {
	for in, want := range map[string]string{
		"some-one":                              "https://www.linkedin.com/in/some-one/",
		"linkedin.com/in/some-one":              "https://linkedin.com/in/some-one",
		"https://www.linkedin.com/in/some-one/": "https://www.linkedin.com/in/some-one/",
		"":                                      "",
	} {
		if got := (Highlight{LI: in}).URL(); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestHighlightName(t *testing.T) {
	for _, c := range []struct{ who, li, want string }{
		{"JOHN SMITH", "", "John Smith"},
		{"kavitha kannan", "", "Kavitha Kannan"},
		{"Mary-Jane McDonald", "", "Mary-Jane McDonald"},
		{"", "john-smith-4b5991198", "John Smith"},
		{"", "https://www.linkedin.com/in/ana-lima/", "Ana Lima"},
		{"", "", "someone"},
	} {
		if got := (Highlight{Who: c.who, LI: c.li}).Name(); got != c.want {
			t.Errorf("%q %q: %q, want %q", c.who, c.li, got, c.want)
		}
	}
}
