package main

import "strings"

// Guest is an approved Luma guest, reduced to what a lead needs.
type Guest struct {
	ID        string // gst-...
	Key       string // g-... (pk= in the check-in QR)
	Tickets   []string
	FirstName string
	LastName  string
	Name      string
	Email     string
	Title     string
	Company   string
	LinkedIn  string // normalized by pkg.ExtractLinkedIn, same as the badge QR
	Phone     string
	Answers   []Answer // every registration answer, in form order
	CheckedIn bool
}

// Answer is one registration question and the guest's answer.
type Answer struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// ExtraAnswers are the registration answers not already in a lead's own
// columns (job title, company, LinkedIn).
func (g *Guest) ExtraAnswers() []Answer {
	var out []Answer
	for _, a := range g.Answers {
		l := strings.ToLower(a.Label)
		if l == "job title" || l == "company" || strings.Contains(l, "linkedin") {
			continue
		}
		out = append(out, a)
	}
	return out
}

// GuestIndex resolves ScanKeys to guests.
type GuestIndex struct {
	bySlug map[string][]*Guest
	byID   map[string]*Guest // gst-, g-, tkt- and lowercased email
	count  int
	inside int // checked in at the door
}

func NewGuestIndex(guests []Guest) *GuestIndex {
	ix := &GuestIndex{
		bySlug: make(map[string][]*Guest),
		byID:   make(map[string]*Guest),
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

// testBadge is the one demo badge: Mark's LinkedIn QR, shown on every
// sponsor dashboard. It resolves to these details at every event and in
// every phase, without the Luma guest list.
var testBadge = Guest{
	ID:        "test-badge",
	FirstName: "Mark",
	LastName:  "Pawlikowski",
	Name:      "Mark Pawlikowski",
	Email:     "mark@sreday.com",
	Title:     "Organizer",
	Company:   "SREday / LLMday / PLATFORMday / PEC",
	LinkedIn:  "https://www.linkedin.com/in/marek-pawlikowski",
}

// testBadgeQR is what the test badge's QR code holds: the LinkedIn profile,
// so a phone camera opens it too.
const testBadgeQR = "https://www.linkedin.com/in/marek-pawlikowski/"

// Lookup returns the guest for a key. Ambiguous LinkedIn slugs (two guests
// typed the same profile) are treated as no match rather than a guess.
func (ix *GuestIndex) Lookup(k ScanKey) (*Guest, bool) {
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
