package snore

import (
	"testing"
	"time"
)

func TestWorkDomain(t *testing.T) {
	for in, want := range map[string]string{
		"ada@mail.example.co.uk": "example.co.uk",
		"Ada@Example.com":        "example.com",
		"ada@gmail.com":          "",
		"ada@yahoo.co.uk":        "",
		"ada@uni.ac.uk":          "",
		"ada@school.edu":         "",
		"not an address":         "",
		"ada@":                   "",
	} {
		if got := workDomain(in); got != want {
			t.Errorf("workDomain(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormCompany(t *testing.T) {
	for in, want := range map[string]string{
		"Example Ltd.":       "example",
		"example LIMITED":    "example",
		"The Example Co":     "example",
		"Example Group, Inc": "example group",
		"N/A":                "",
		"Self-employed":      "",
		"  ":                 "",
		"Freelance":          "",
	} {
		if got := normCompany(in); got != want {
			t.Errorf("normCompany(%q) = %q, want %q", in, got, want)
		}
	}
}

func testHumanData(now time.Time) *humanData {
	ago := func(days int) time.Time { return now.Add(-time.Duration(days) * 24 * time.Hour) }
	return &humanData{
		profiles: []humanProfile{
			{slug: "ada", name: "Ada Example", title: "Field Marketing Manager", company: "Example Ltd"},
			{slug: "ben", name: "Ben Example", title: "SRE", company: "example limited"},
			{slug: "cy", name: "Cy Other", title: "Engineer", company: "Otherco"},
			{slug: "dee", name: "Dee Nobody", title: "Engineer", company: "N/A"},
			{slug: "eve", name: "Eve Visitor", title: "CTO", company: "Thirdco"},
		},
		attendees: []humanAttendee{
			{slug: "ada", email: "ada@example.com", name: "Ada Example", event: "City 2026", registered: ago(40), checkedIn: ago(30)},
			{slug: "cy", email: "cy@gmail.com", name: "Cy Other", event: "City 2026", registered: ago(40)},
			{slug: "", email: "walk@in.example.org", name: "Walk In", event: "City 2026", registered: ago(40)},
		},
		events: []humanEvent{
			{slug: "ben", kind: "messaged", by: "teammate-a", at: ago(20)},
			{slug: "ben", kind: "interested", by: "teammate-a", at: ago(18)},
			{slug: "cy", kind: "can't come", by: "teammate-b", at: ago(10)},
			{slug: "dee", kind: "interested", by: "teammate-b", at: ago(10)}, // no company: left out
			{slug: "eve", kind: "unknown word", by: "teammate-b", at: ago(10)},
			{slug: "ada", kind: "connected", by: "teammate-b", at: ago(50)},
		},
		speakers: []humanSpeaker{{slug: "cy", name: "Cy Other", event: "evt-1", at: ago(100)}},
	}
}

func TestBuildTouches(t *testing.T) {
	now := time.Date(2026, 11, 21, 6, 0, 0, 0, time.UTC)
	touches, names := buildTouches(testHumanData(now))

	by := map[string][]Touch{}
	for _, tc := range touches {
		by[tc.Company] = append(by[tc.Company], tc)
	}
	// Ben has no address of his own: his typed company is the one Ada
	// writes from, so both land on the same domain.
	if len(by["example.com"]) != 5 {
		t.Fatalf("example.com touches = %d, want 5: %+v", len(by["example.com"]), by["example.com"])
	}
	if names["example.com"] == "" {
		t.Fatalf("no name for example.com: %v", names)
	}
	// Cy registered from private mail: known by the typed company only.
	if len(by["name:otherco"]) != 3 {
		t.Fatalf("otherco touches = %+v", by["name:otherco"])
	}
	if len(by["example.org"]) != 1 || by["example.org"][0].Person != "Walk In" {
		t.Fatalf("a registrant without a profile counts by work domain: %+v", by["example.org"])
	}
	for _, tc := range touches {
		if tc.Person == "Dee Nobody" || tc.Person == "Eve Visitor" {
			t.Fatalf("should be left out: %+v", tc)
		}
	}

	w := warmthOf(by["example.com"], now)
	if w.Level != 4 || w.Owner != "teammate-a" || w.Best.Person != "Ben Example" || w.Best.Note != "interested" {
		t.Fatalf("example.com warmth = %+v", w)
	}
	if w.People != 2 {
		t.Fatalf("people = %d", w.People)
	}
	// A speaker is the organizer's, even with a teammate's reply on record.
	if w := warmthOf(by["name:otherco"], now); w.Level != 5 || w.Owner != Organizer {
		t.Fatalf("otherco warmth = %+v", w)
	}
}
