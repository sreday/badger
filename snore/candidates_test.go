package snore

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
		"conf42/platform2026":              "conf42",
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
	s.fp.F["x/llmday/2026-bengaluru-q4"].Luma = "evt-ahead"

	d := &humanData{
		profiles: []humanProfile{
			{slug: "w1", name: "Wanda Warm", title: "SRE", company: "Warmco"},
			{slug: "c1", name: "Cur One", title: "Field Marketing Manager", company: "Current"},
			{slug: "i1", name: "Ina Inside", title: "Head of Events", company: "Insideco"},
			{slug: "s1", name: "Sol Solo", title: "Engineer", company: "Soloco"},
			{slug: "t1", name: "Tia Talking", title: "Developer Advocate", company: "Talkingco"},
			{slug: "d1", name: "Dee One", title: "SRE", company: "Crowdco"},
			{slug: "d2", name: "Dee Two", title: "Platform Engineer", company: "Crowdco"},
			{slug: "d3", name: "Dee Three", title: "Developer", company: "Crowdco"},
		},
		attendees: []humanAttendee{
			{slug: "w1", email: "w1@warm.example", event: "LLMday City 2026", registered: ago(60)},
			{slug: "c1", email: "c1@current.example", event: "SREday City 2026", registered: ago(60), checkedIn: ago(50)},
			{slug: "i1", email: "i1@inside.example", event: "PLATFORMday City 2026", registered: ago(60), checkedIn: ago(50)},
			{slug: "s1", email: "s1@solo.example", event: "SREday City 2026", registered: ago(60)},
			{slug: "t1", email: "t1@talking.example", event: "SREday City 2026", registered: ago(60), checkedIn: ago(50)},
			{slug: "d1", email: "dee.one@crowd.example", event: "SREday City 2026", registered: ago(60), checkedIn: ago(50)},
			{slug: "d2", email: "dee.two@crowd.example", event: "SREday City 2026", registered: ago(60)},
			{slug: "d3", event: "SREday City 2026", registered: ago(60)},
		},
		events: []humanEvent{{slug: "w1", kind: "interested", by: "teammate-a", at: ago(20)}},
	}
	s.cfg.HumansDB = "x"
	s.readTouches = func(context.Context, string) (*humanData, error) { return d, nil }
	s.cfg.Guests = func(id string) []bridge.Guest {
		if id != "evt-ahead" {
			return nil
		}
		return []bridge.Guest{
			{Name: "Sam Soon", Email: "sam@soon.example", Title: "SRE", Company: "Soonco"},
			{Name: "Sue Soon", Email: "sue@soon.example", Title: "Engineer", Company: "Soonco", LinkedIn: "https://www.linkedin.com/in/Sue-Soon"},
			{Name: "Sid Soon", Email: "sid@soon.example", Title: "Developer", Company: "Soonco"},
		}
	}
	s.syncTouches()
	s.cfg.Talks = func() []bridge.Talk {
		return []bridge.Talk{
			{Domain: "quiet.example", Company: "Quiet Co", LastAt: ago(45), Bucket: "active", URL: "https://mail.example/1"},
			{Domain: "talking.example", Company: "Talkingco", LastAt: ago(3), Bucket: "active"},
		}
	}

	read := func() (map[string]Candidate, map[string][]string) {
		got, order := map[string]Candidate{}, map[string][]string{}
		for _, c := range s.sponsorPool(now) {
			got[c.Brand+" "+c.Key] = c
			order[c.Brand] = append(order[c.Brand], c.Key)
		}
		return got, order
	}
	got, order := read()
	for k := range got {
		for _, bad := range []string{"current.example", "talking.example", "solo.example"} {
			if strings.HasSuffix(k, bad) {
				t.Errorf("%s does not belong on a list (booked for the next event, live talk, one engineer)", k)
			}
		}
	}
	if c := got["llmday warm.example"]; c.Replied != 1 || !strings.Contains(c.Why, "Wanda Warm replied to us in Nov 2026") ||
		len(c.People) != 1 || c.People[0].Email != "w1@warm.example" || c.People[0].Mail != "yes" {
		t.Errorf("a reply to a teammate = %+v", c)
	}
	if c := got["sreday quiet.example"]; c.Talk == nil || c.TalkWhy != "Talk went quiet" || c.Company != "Quiet Co" || !strings.Contains(c.Why, "Talk went quiet in Oct 2026") {
		t.Errorf("quiet talk = %+v", c)
	}
	if c := got["sreday lapsed.example"]; len(c.Sponsored) != 1 || c.Company != "Lapsed" || c.Why != "Sponsored SREday once, last in May 2025." {
		t.Errorf("past sponsor = %+v", c)
	}
	if c := got["platformday inside.example"]; c.Inside != 1 || c.Came != 1 || !strings.Contains(c.Why, "1 person came to our events, last PLATFORMday City 2026") {
		t.Errorf("inside contact = %+v", c)
	}
	// Three people registered for an event ahead, read from the guest list.
	c := got["llmday soon.example"]
	if c.Coming != 3 || c.Company != "Soonco" || c.Why != "3 people registered for LLMday Bengaluru." || len(c.People) != 3 {
		t.Errorf("registered for the next event = %+v", c)
	}
	// A crowd of engineers: the one without an address gets a guessed one.
	c = got["sreday crowd.example"]
	if c.Seen != 3 || c.Came != 1 || len(c.People) != 3 {
		t.Fatalf("crowd = %+v", c)
	}
	var three leadPerson
	for _, p := range c.People {
		if p.Name == "Dee Three" {
			three = p
		}
	}
	if three.Email != "dee.three@crowd.example" || three.Mail != "guessed" || three.Basis != "first.last, from 2 addresses we hold" {
		t.Errorf("guessed address = %+v", three)
	}
	if o := order["sreday"]; len(o) != 3 || o[0] != "crowd.example" {
		t.Errorf("sreday order with every string worth the same = %v", o)
	}
	// The numbers kept with the data change the order.
	if err := os.WriteFile(filepath.Join(s.dir, "sponsors-rank.json"), []byte(`{"sponsor": 50, "quiet": 20}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, order = read(); strings.Join(order["sreday"], " ") != "lapsed.example quiet.example crowd.example" {
		t.Errorf("sreday order by the kept numbers = %v", order["sreday"])
	}
	// The addresses stay with us: what goes to the daily read says only that we hold one.
	b, _ := json.Marshal(got["sreday crowd.example"])
	if strings.Contains(string(b), "@") || !strings.Contains(string(b), `"email":"guessed"`) {
		t.Errorf("a lead as handed out = %s", b)
	}
}
