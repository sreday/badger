package snore

import (
	"context"
	"testing"
	"time"

	"github.com/sreday/badger/bridge"
)

func TestBrandOf(t *testing.T) {
	for in, want := range map[string]string{
		"SREday London 2026 Q3":            "sreday",
		"LLMday WWW City 2026 Q4":          "llmday",
		"PLATFORMday City":                 "platformday",
		"Prompt Engineering Conference 25": "llmday",
		"Excels_for_WWW SRE_2025":          "conf42",
		"conf42/sre2026":                   "conf42",
		"Some meetup":                      "",
	} {
		if got := brandOf(in); got != want {
			t.Errorf("brandOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCandidates(t *testing.T) {
	s, _ := testServer(t)
	now := s.now()
	ago := func(days int) time.Time { return now.Add(-time.Duration(days) * 24 * time.Hour) }
	old := s.fp.F["x/sreday/past/2025-amsterdam-q2"]
	old.Key, old.Sponsors = "x/sreday/past/2025-amsterdam-q2", []Sponsored{{Name: "Lapsed", Domain: "lapsed.example"}}
	cur := s.fp.F["x/sreday/2026-london-q4"]
	cur.Key, cur.Sponsors = "x/sreday/2026-london-q4", []Sponsored{{Name: "Current", Domain: "current.example"}}

	d := &humanData{
		profiles: []humanProfile{
			{slug: "w1", name: "Wanda Warm", title: "SRE", company: "Warmco"},
			{slug: "c1", name: "Cur One", title: "Field Marketing Manager", company: "Current"},
			{slug: "i1", name: "Ina Inside", title: "Head of Events", company: "Insideco"},
			{slug: "s1", name: "Sol Solo", title: "Engineer", company: "Soloco"},
			{slug: "t1", name: "Tia Talking", title: "Developer Advocate", company: "Talkingco"},
		},
		attendees: []humanAttendee{
			{slug: "w1", email: "w1@warm.example", event: "LLMday City 2026", registered: ago(60)},
			{slug: "c1", email: "c1@current.example", event: "SREday City 2026", registered: ago(60), checkedIn: ago(50)},
			{slug: "i1", email: "i1@inside.example", event: "PLATFORMday City 2026", registered: ago(60), checkedIn: ago(50)},
			{slug: "s1", email: "s1@solo.example", event: "SREday City 2026", registered: ago(60)},
			{slug: "t1", email: "t1@talking.example", event: "SREday City 2026", registered: ago(60), checkedIn: ago(50)},
		},
		events: []humanEvent{{slug: "w1", kind: "interested", by: "teammate-a", at: ago(20)}},
	}
	s.cfg.HumansDB = "x"
	s.readTouches = func(context.Context, string) (*humanData, error) { return d, nil }
	s.syncTouches()
	s.cfg.Talks = func() []bridge.Talk {
		return []bridge.Talk{
			{Domain: "quiet.example", Company: "Quiet Co", LastAt: ago(45), Bucket: "active", URL: "https://mail.example/1"},
			{Domain: "talking.example", Company: "Talkingco", LastAt: ago(3), Bucket: "active"},
		}
	}

	got := map[string]Candidate{}
	var order []string
	for _, c := range s.sponsorPool(now) {
		got[c.Key] = c
		order = append(order, c.Key)
	}
	for _, k := range []string{"current.example", "talking.example", "solo.example"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s does not belong in the pool (this year's sponsor, live talk, one engineer)", k)
		}
	}
	if c := got["warm.example"]; c.Label != LabelWarmedUp || c.Owner != "teammate-a" || c.Brand != "llmday" {
		t.Errorf("warmed up = %+v", c)
	}
	if c := got["quiet.example"]; c.Label != LabelRevive || c.Talk == nil || c.Quiet != 45 || c.Company != "Quiet Co" {
		t.Errorf("quiet talk = %+v", c)
	}
	if c := got["lapsed.example"]; c.Label != LabelRevive || c.ReviveWhy != "Lapsed sponsor" || len(c.Sponsored) != 1 || c.Level != 7 {
		t.Errorf("lapsed sponsor = %+v", c)
	}
	if c := got["inside.example"]; c.Label != LabelWarm || len(c.Inside) != 1 || c.Brand != "platformday" || c.Came != 1 {
		t.Errorf("inside contact = %+v", c)
	}
	if len(order) != 4 || order[0] != "warm.example" || order[3] != "inside.example" {
		t.Errorf("order = %v", order)
	}
}
