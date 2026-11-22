package snore

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRoleOf(t *testing.T) {
	for title, want := range map[string]string{
		"Field Marketing Manager, EMEA":    RoleMarketing,
		"VP Marketing":                     RoleMarketing,
		"Head of Events":                   RoleMarketing,
		"Senior Developer Advocate":        RoleDevRel,
		"Community Manager":                RoleDevRel,
		"Founder & CTO":                    RoleFounder,
		"Co-Founder":                       RoleFounder,
		"CEO":                              RoleFounder,
		"Geschäftsführer":                  RoleFounder,
		"Account Executive":                RolePartners,
		"Head of Partnerships":             RolePartners,
		"Solutions Engineer":               RolePartners,
		"Technical Recruiter":              RoleTalent,
		"Talent Acquisition Lead":          RoleTalent,
		"Senior Site Reliability Engineer": RoleEngineering,
		"CTO":                              RoleEngineering,
		"Platform Lead":                    RoleEngineering,
		"Inżynier oprogramowania":          RoleEngineering,
		"PhD Student":                      RoleStudent,
		"Software Engineering Intern":      RoleStudent,
		"":                                 RoleOther,
		"Wizard":                           RoleOther,
		"Prevention specialist":            RoleOther, // "event" inside a word is no match
	} {
		if got := roleOf(title); got != want {
			t.Errorf("roleOf(%q) = %q, want %q", title, got, want)
		}
	}
}

func TestRoomRows(t *testing.T) {
	s, h := testServer(t)
	now := s.now()
	ago := func(days int) time.Time { return now.Add(-time.Duration(days) * 24 * time.Hour) }
	d := &humanData{
		profiles: []humanProfile{
			{slug: "ada", name: "Ada Example", title: "Field Marketing Manager", company: "Example Ltd"},
			{slug: "ben", name: "Ben Example", title: "SRE", company: "Example Ltd"},
			{slug: "cy", name: "Cy Solo", title: "Engineer", company: "Soloco"},
			{slug: "d1", name: "Dee One", title: "SRE", company: "Crowdco"},
			{slug: "d2", name: "Dee Two", title: "Platform Engineer", company: "Crowdco"},
			{slug: "d3", name: "Dee Three", title: "Developer", company: "Crowdco"},
			{slug: "old", name: "Old Timer", title: "Founder", company: "Oldco"},
		},
		attendees: []humanAttendee{
			{slug: "ada", email: "ada@example.com", event: "City 2026", registered: ago(40), checkedIn: ago(30)},
			{slug: "ben", email: "ben@example.com", event: "City 2026", registered: ago(40)},
			{slug: "ben", email: "ben@example.com", event: "Town 2026", registered: ago(200)},
			{slug: "cy", email: "cy@solo.example", event: "City 2026", registered: ago(40)},
			{slug: "d1", email: "d1@crowd.example", event: "City 2026", registered: ago(40)},
			{slug: "d2", email: "d2@crowd.example", event: "City 2026", registered: ago(40), checkedIn: ago(30)},
			{slug: "d3", email: "d3@crowd.example", event: "City 2026", registered: ago(40)},
			{slug: "old", email: "old@old.example", event: "City 2023", registered: ago(1000)},
		},
	}
	s.cfg.HumansDB = "x"
	s.readTouches = func(context.Context, string) (*humanData, error) { return d, nil }
	s.syncTouches()

	rows := s.roomRows(now)
	if len(rows) != 2 {
		t.Fatalf("want the company with an inside contact and the crowd, got %+v", rows)
	}
	ex := rows[0]
	if ex.Domain != "example.com" || ex.People != 2 || ex.Came != 1 || ex.Events != 2 || len(ex.Inside) != 1 || ex.Inside[0].Name != "Ada Example" || !ex.Inside[0].Came {
		t.Fatalf("the company with an inside contact leads: %+v", ex)
	}
	if rows[1].Domain != "crowd.example" || rows[1].People != 3 || len(rows[1].Inside) != 0 {
		t.Fatalf("the crowd = %+v", rows[1])
	}

	if w := do(t, h, "GET", "/room", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("no login: %d", w.Code)
	}
	body := do(t, h, "GET", "/room", "", admin).Body.String()
	for _, want := range []string{"Example Ltd", "Ada Example", "Field Marketing Manager", "2 companies", "1 with an inside contact", "none yet"} {
		if !strings.Contains(body, want) {
			t.Errorf("room page misses %q", want)
		}
	}
	if strings.Contains(body, "Cy Solo") || strings.Contains(body, "Old Timer") {
		t.Error("one engineer alone, or a registration from years ago, does not make a row")
	}
}
