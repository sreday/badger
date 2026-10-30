package main

import (
	"strings"
	"unicode"
)

// Some people register twice with two emails (work and personal), so the
// sponsor-ready list would show them twice. Two registrations are the same
// person when the names match (a first name may be just its initial: "J
// Novak" is Jana Novak) and something else does too: the LinkedIn, the
// company or a work email domain. A name alone isn't enough, and neither is
// a LinkedIn alone: people mistype it, or share a partner's.

// dedupeGuests groups the registrations of one person, most complete first.
// Everyone else is a group of one.
func dedupeGuests(guests []Guest) [][]Guest {
	parent := make([]int, len(guests))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	byLast := map[string][]int{}
	for i, g := range guests {
		_, last := guestNames(g)
		if l := normName(last); l != "" {
			byLast[l] = append(byLast[l], i)
		}
	}
	for _, idx := range byLast {
		for a := 0; a < len(idx); a++ {
			for b := a + 1; b < len(idx); b++ {
				if samePerson(guests[idx[a]], guests[idx[b]]) {
					parent[find(idx[a])] = find(idx[b])
				}
			}
		}
	}
	groups := map[int][]Guest{}
	var order []int
	for i, g := range guests {
		r := find(i)
		if _, ok := groups[r]; !ok {
			order = append(order, r)
		}
		groups[r] = append(groups[r], g)
	}
	out := make([][]Guest, 0, len(order))
	for _, r := range order {
		gs := groups[r]
		best := 0
		for i := range gs {
			if completeness(gs[i]) > completeness(gs[best]) {
				best = i
			}
		}
		gs[0], gs[best] = gs[best], gs[0]
		out = append(out, gs)
	}
	return out
}

// guestNames is the first and last name, split from the full name when
// Luma has only that.
func guestNames(g Guest) (first, last string) {
	first, last = g.FirstName, g.LastName
	if first == "" && last == "" {
		first, last, _ = strings.Cut(g.Name, " ")
	}
	return strings.TrimSpace(first), strings.TrimSpace(last)
}

func samePerson(a, b Guest) bool {
	af, al := guestNames(a)
	bf, bl := guestNames(b)
	if normName(al) != normName(bl) || !sameFirstName(normName(af), normName(bf)) {
		return false
	}
	if s := realSlug(a); s != "" && s == realSlug(b) {
		return true
	}
	if c := normWord(a.Company); c != "" && c == normWord(b.Company) {
		return true
	}
	if d := workDomain(a.Email); d != "" && d == workDomain(b.Email) {
		return true
	}
	return false
}

// sameFirstName also takes an initial for the full name.
func sameFirstName(a, b string) bool {
	if a == b {
		return a != ""
	}
	if len([]rune(a)) > len([]rune(b)) {
		a, b = b, a
	}
	return len([]rune(a)) == 1 && strings.HasPrefix(b, a)
}

// realSlug is the guest's LinkedIn slug, or "" for a placeholder like
// "non" or "---".
func realSlug(g Guest) string {
	k, ok := ParseScan(g.LinkedIn)
	if !ok || k.Kind != "linkedin" || len(k.Value) < 4 || strings.IndexFunc(k.Value, unicode.IsLetter) < 0 {
		return ""
	}
	return k.Value
}

var freemail = map[string]bool{
	"gmail.com": true, "googlemail.com": true, "hotmail.com": true, "outlook.com": true, "live.com": true,
	"yahoo.com": true, "icloud.com": true, "me.com": true, "proton.me": true, "protonmail.com": true,
	"gmx.com": true, "gmx.de": true, "aol.com": true, "appleid.com": true,
}

// workDomain is the email's company domain, or "" for a personal mailbox.
func workDomain(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return ""
	}
	d := baseDomain(email[at+1:])
	if freemail[d] {
		return ""
	}
	return d
}

// completeness scores how useful a registration is to a sponsor: filled-in
// fields, a full first name, a real LinkedIn and a work email; longer job
// title and company break ties.
func completeness(g Guest) int {
	first, last := guestNames(g)
	score := 0
	for _, v := range []string{first, last, g.Email, g.Title, g.Company} {
		if v != "" {
			score += 1000
		}
	}
	if len([]rune(first)) > 1 {
		score += 1000
	}
	if realSlug(g) != "" {
		score += 1000
	}
	if workDomain(g.Email) != "" && !strings.Contains(g.Email, "+") {
		score += 1000
	}
	return score + min(len(g.Title)+len(g.Company), 999)
}
