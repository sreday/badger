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
}

// GuestIndex resolves ScanKeys to guests.
type GuestIndex struct {
	bySlug map[string][]*Guest
	byID   map[string]*Guest // gst-, g-, tkt- and lowercased email
	count  int
}

func NewGuestIndex(guests []Guest) *GuestIndex {
	ix := &GuestIndex{
		bySlug: make(map[string][]*Guest),
		byID:   make(map[string]*Guest),
		count:  len(guests),
	}
	for i := range guests {
		g := &guests[i]
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

// Lookup returns the guest for a key. Ambiguous LinkedIn slugs (two guests
// typed the same profile) are treated as no match rather than a guess.
func (ix *GuestIndex) Lookup(k ScanKey) (*Guest, bool) {
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

// Len returns the number of guests indexed.
func (ix *GuestIndex) Len() int {
	if ix == nil {
		return 0
	}
	return ix.count
}
