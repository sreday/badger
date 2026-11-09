package main

import (
	"strings"

	"github.com/sreday/badger/event"
	"github.com/sreday/badger/luma"
)

// GuestIndex resolves ScanKeys to guests.
type GuestIndex struct {
	bySlug map[string][]*luma.Guest
	byID   map[string]*luma.Guest // gst-, g-, tkt- and lowercased email
	count  int
	inside int // checked in at the door
}

func NewGuestIndex(guests []luma.Guest) *GuestIndex {
	ix := &GuestIndex{
		bySlug: make(map[string][]*luma.Guest),
		byID:   make(map[string]*luma.Guest),
		count:  len(guests),
	}
	for i := range guests {
		g := &guests[i]
		if g.CheckedIn {
			ix.inside++
		}
		if slug := SlugFromURL(g.LinkedIn); slug != "" {
			ix.bySlug[slug] = append(ix.bySlug[slug], g)
		}
		for _, id := range append([]string{g.ID, g.Key, strings.ToLower(g.Email)}, g.Tickets...) {
			if id != "" {
				ix.byID[id] = g
			}
		}
	}
	return ix
}

// testBadge is the one demo badge: Mark's printed badge, shown on every
// sponsor dashboard. It resolves to these details at every event and in
// every phase, without the Luma guest list.
var testBadge = luma.Guest{
	ID:        "test-badge",
	FirstName: "Mark",
	LastName:  "Pawlikowski",
	Name:      "Mark Pawlikowski",
	Email:     "mark@sreday.com",
	Title:     "Organizer",
	Company:   "SREday", // the event's own conference: see lookupFor
	LinkedIn:  "https://www.linkedin.com/in/marek-pawlikowski",
}

// testBadgeQR is what the test badge's QR code holds, the same form as every
// printed badge (assets/badges/).
const testBadgeQR = "linkedin://in/marek-pawlikowski"

// lookupFor is Lookup at one event: the test badge's company is that
// event's conference (SREday at SREday, LLMday at LLMday).
func lookupFor(ix *GuestIndex, k ScanKey, eventName string) (*luma.Guest, bool) {
	g, ok := ix.Lookup(k)
	if ok && g.ID == testBadge.ID {
		g.Company, _ = event.Conference(eventName)
	}
	return g, ok
}

// Lookup returns the guest for a key. Ambiguous LinkedIn slugs (two guests
// typed the same profile) are treated as no match rather than a guess.
func (ix *GuestIndex) Lookup(k ScanKey) (*luma.Guest, bool) {
	if k.Kind == "linkedin" && k.Value == SlugFromURL(testBadge.LinkedIn) {
		g := testBadge
		return &g, true
	}
	if ix == nil {
		return nil, false
	}
	switch k.Kind {
	case "linkedin":
		if gs := ix.bySlug[k.Value]; len(gs) == 1 {
			return gs[0], true
		}
	case "luma", "email":
		if g, ok := ix.byID[k.Value]; ok {
			return g, true
		}
	}
	return nil, false
}

// Guests returns every guest in the index once.
func (ix *GuestIndex) Guests() []luma.Guest {
	if ix == nil {
		return nil
	}
	seen := map[*luma.Guest]bool{}
	var out []luma.Guest
	for _, g := range ix.byID {
		if !seen[g] {
			seen[g] = true
			out = append(out, *g)
		}
	}
	for _, gs := range ix.bySlug {
		for _, g := range gs {
			if !seen[g] {
				seen[g] = true
				out = append(out, *g)
			}
		}
	}
	return out
}

// CheckedIn returns how many guests were checked in at the door.
func (ix *GuestIndex) CheckedIn() int {
	if ix == nil {
		return 0
	}
	return ix.inside
}

// Len returns the number of guests indexed.
func (ix *GuestIndex) Len() int {
	if ix == nil {
		return 0
	}
	return ix.count
}
