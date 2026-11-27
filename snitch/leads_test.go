package snitch

import (
	"testing"
	"time"
)

func testRules() *LeadRules {
	r := &LeadRules{
		Weak:           []LeadRule{{Label: "alpha", Words: []string{"alpha"}}, {Label: "beta", Words: []string{"beta co"}, In: "any"}},
		Top:            []LeadRule{{Label: "t", Words: []string{"gamma"}}},
		Good:           []LeadRule{{Label: "g", Words: []string{"delta"}}},
		MinFollowers:   50,
		MinConnections: 50,
	}
	r.compile()
	return r
}

func TestLeadGrade(t *testing.T) {
	r := testRules()
	for _, c := range []struct {
		p          humanProfile
		grade, why string
	}{
		{humanProfile{title: "Gamma of things", followers: -1, connections: -1}, "top", ""},
		{humanProfile{title: "Delta person", followers: 900, connections: 500}, "good", ""},
		{humanProfile{title: "Plain person", followers: -1, connections: -1}, "ok", ""},
		{humanProfile{title: "Gamma and Alpha", followers: -1, connections: -1}, "weak", "alpha"}, // weak beats top
		{humanProfile{title: "Alphabet maker", followers: -1, connections: -1}, "ok", ""},         // whole words only
		{humanProfile{title: "Gamma", company: "Beta Co", followers: -1, connections: -1}, "weak", "beta"},
		{humanProfile{title: "Gamma", followers: 12, connections: -1}, "weak", "thin profile"},
		{humanProfile{title: "", followers: -1, connections: -1}, "weak", "thin profile"},
	} {
		g, why := r.grade(c.p)
		if g != c.grade || why != c.why {
			t.Errorf("%+v: got %s/%s, want %s/%s", c.p, g, why, c.grade, c.why)
		}
	}
	if linkedInCount("1,234") != 1234 || linkedInCount("500+") != 500 || linkedInCount("12 345") != 12345 || linkedInCount("") != -1 {
		t.Error("linkedInCount")
	}
}

func TestLeadsFor(t *testing.T) {
	at := func(s string) time.Time { v, _ := time.Parse("2006-01-02 15:04", s); return v }
	evs := []humanEvent{
		{acct: 3, created: at("2026-11-23 10:00"), tag: "messaged", li: "a"},
		{acct: 3, created: at("2026-11-24 10:00"), tag: "interested", li: "a"}, // same person, same week: once
		{acct: 3, created: at("2026-11-23 10:00"), tag: "messaged", li: "b"},
		{acct: 3, created: at("2026-11-23 10:00"), tag: "visit", li: "c"},    // a visit: not a lead
		{acct: 3, created: at("2026-11-23 10:00"), tag: "messaged", li: "d"}, // no profile
	}
	profiles := map[string]humanProfile{
		"a": {title: "Gamma", followers: -1, connections: -1},
		"b": {title: "Alpha one", followers: -1, connections: -1},
		"c": {title: "Gamma", followers: -1, connections: -1},
	}
	ls := leadsFor(evs, profiles, testRules(), time.UTC)
	l := ls[[2]string{"2026-W48", "3"}]
	if l == nil || l.Top != 1 || l.Weak != 1 || l.Unknown != 1 || l.WeakBy["alpha"] != 1 || l.Total() != 2 || l.WeakPct() != 50 {
		t.Fatalf("leads: %+v", l)
	}
	if len(leadsFor(evs, profiles, nil, time.UTC)) != 0 {
		t.Fatal("no rules: no leads")
	}
}
