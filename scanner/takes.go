package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// Nobody on a sponsor's team has the final say on a lead. Everyone who
// rates or notes a lead has their own take, named after them ("Scanning as"
// on the phone, "Dashboard" on the dashboard), and can only change their
// own. The lead's Rating is the team's: the rating when everyone agrees,
// "split" when they don't, so the marketing person (or their AI) decides.
// Its Note is everyone's notes with their names. Every scan, rating and
// note is also kept as history, downloadable as a log.

// Take is one person's rating and note on a lead.
type Take struct {
	By     string    `json:"by"`
	Rating string    `json:"rating,omitempty"`
	Note   string    `json:"note,omitempty"`
	At     time.Time `json:"at,omitzero"`
}

// RatingSplit is the team rating when people rated a lead differently.
const RatingSplit = "split"

// noName is the author of a change from a phone without a name.
const noName = "No name"

// Event is one entry of the scanning history.
type Event struct {
	At     time.Time
	By     string
	Action string // scan, rescan, rate, unrate, note, renote, unnote
	ScanID string
	Rating string
	Note   string
}

// firstScanner is who a lead's old single rating and note belong to: the
// first person who scanned it.
func firstScanner(l *Lead) string {
	if n := strings.TrimSpace(strings.Split(l.ScannedBy, ",")[0]); n != "" {
		return n
	}
	return "Team"
}

// applyTake sets one person's take on a lead, updates the team rating and
// notes, and returns what changed for the history. The caller holds s.mu.
func applyTake(l *Lead, by, rating, note string, at time.Time) []Event {
	by = strings.TrimSpace(by)
	if by == "" {
		by = noName
	}
	i := -1
	for j := range l.Takes {
		if strings.EqualFold(l.Takes[j].By, by) {
			i = j
		}
	}
	var prev Take
	if i >= 0 {
		prev = l.Takes[i]
	}
	var evs []Event
	ev := func(action string) {
		evs = append(evs, Event{At: at, By: by, Action: action, ScanID: l.ScanID, Rating: rating, Note: note})
	}
	if rating != prev.Rating {
		if rating == "" {
			ev("unrate")
		} else {
			ev("rate")
		}
	}
	if note != prev.Note {
		switch {
		case note == "":
			ev("unnote")
		case prev.Note == "":
			ev("note")
		default:
			ev("renote")
		}
	}
	t := Take{By: by, Rating: rating, Note: note, At: at}
	if i >= 0 {
		t.By = l.Takes[i].By
		l.Takes[i] = t
	} else {
		l.Takes = append(l.Takes, t)
	}
	l.Rating, l.Note = teamRating(l.Takes), teamNote(l.Takes)
	return evs
}

// teamRating is the rating everyone agrees on, "split" when they don't,
// "" when nobody rated.
func teamRating(takes []Take) string {
	r := ""
	for _, t := range takes {
		switch {
		case t.Rating == "":
		case r == "":
			r = t.Rating
		case r != t.Rating:
			return RatingSplit
		}
	}
	return r
}

// teamNote is everyone's notes with their names: "Mark: wants a demo |
// David: no budget".
func teamNote(takes []Take) string {
	var parts []string
	for _, t := range takes {
		if t.Note != "" {
			parts = append(parts, t.By+": "+t.Note)
		}
	}
	return strings.Join(parts, " | ")
}

// notesLines is everyone's notes, one per line, the dashboard's first:
// "Dashboard: follow up" / "Mark: wants a demo".
func notesLines(takes []Take) string {
	var lines []string
	for _, t := range takes {
		if t.Note != "" && strings.EqualFold(t.By, "Dashboard") {
			lines = append(lines, t.By+": "+t.Note)
		}
	}
	for _, t := range takes {
		if t.Note != "" && !strings.EqualFold(t.By, "Dashboard") {
			lines = append(lines, t.By+": "+t.Note)
		}
	}
	return strings.Join(lines, "\n")
}

// teamRatings is who rated what: "Mark: hot; David: cold".
func teamRatings(takes []Take) string {
	var parts []string
	for _, t := range takes {
		if t.Rating != "" {
			parts = append(parts, t.By+": "+ratingLabel(t.Rating))
		}
	}
	return strings.Join(parts, "; ")
}

// hasRating reports whether anyone on the team gave the lead this rating.
func (l Lead) hasRating(r string) bool {
	if len(l.Takes) == 0 {
		return l.Rating == r
	}
	for _, t := range l.Takes {
		if t.Rating == r {
			return true
		}
	}
	return false
}

// HotIntro is one lead someone on a sponsor's team rated hot, for the
// organizers to introduce the two after the event.
type HotIntro struct {
	Sponsor string
	Domain  string // the sponsor's website domain, from the event page
	Lead    Lead
}

// Rater is who a phone name ("Peter") most likely is: the one registrant
// from the sponsor with that name, or "" with a reason to double-check.
type Rater struct {
	Name, Email, Check string
}

// FindRater matches a phone's "Scanning as" name to the event's
// registrants from the same sponsor: their company is the sponsor's name,
// or their email is on the sponsor's domain, and their first name (or full
// name, when the phone has one) matches. Peter on the Imply phone at an
// event where Peter Marshall from Imply registered is Peter Marshall.
func FindRater(by, sponsor, domain string, guests []Guest) Rater {
	want := normName(by)
	if want == "" || want == normName(noName) || want == "dashboard" || want == "team" {
		return Rater{Check: by + ": no name to match"}
	}
	var hits []Guest
	for _, g := range guests {
		if !fromSponsor(g, sponsor, domain) {
			continue
		}
		full := normName(fullName(g))
		first := strings.SplitN(full, " ", 2)[0]
		if full == want || (!strings.Contains(want, " ") && first == want) || strings.HasPrefix(full, want+" ") {
			hits = append(hits, g)
		}
	}
	switch len(hits) {
	case 1:
		name := strings.TrimSpace(hits[0].FirstName + " " + hits[0].LastName)
		if name == "" {
			name = hits[0].Name
		}
		return Rater{Name: name, Email: hits[0].Email}
	case 0:
		return Rater{Check: fmt.Sprintf("%s: no %s registrant by that name", by, sponsor)}
	default:
		var emails []string
		for _, g := range hits {
			emails = append(emails, g.Email)
		}
		return Rater{Check: fmt.Sprintf("%s: %d possible (%s)", by, len(hits), strings.Join(emails, ", "))}
	}
}

// fromSponsor reports whether a registrant is from the sponsor: their
// company is the sponsor's name, or their email is on its domain.
func fromSponsor(g Guest, sponsor, domain string) bool {
	sp, co := normWord(sponsor), normWord(g.Company)
	if sp != "" && co != "" && (co == sp || (len(sp) >= 4 && strings.Contains(co, sp)) || (len(co) >= 4 && strings.Contains(sp, co))) {
		return true
	}
	if at := strings.LastIndex(g.Email, "@"); domain != "" && at >= 0 && baseDomain(g.Email[at+1:]) == domain {
		return true
	}
	return false
}

func fullName(g Guest) string {
	if n := strings.TrimSpace(g.FirstName + " " + g.LastName); n != "" {
		return n
	}
	return strings.TrimSpace(g.Name)
}

// TeamNames are the full names of the sponsor's registered people, for the
// phone's "Who's scanning?" suggestions: two Marks on one team pick "Mark
// Smith" and "Mark Grunner" and stay two people. Names only.
func TeamNames(guests []Guest, sponsor, domain string) []string {
	seen := map[string]bool{}
	var out []string
	for _, g := range guests {
		n := fullName(g)
		if n == "" || seen[strings.ToLower(n)] || !fromSponsor(g, sponsor, domain) {
			continue
		}
		seen[strings.ToLower(n)] = true
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

var hotIntrosHeader = []string{"Sponsor", "Rated hot by", "Rater name", "Rater email", "Check", "First Name", "Last Name", "Email", "Job Title", "Company", "LinkedIn", "Notes", "Scanned At"}

// WriteHotIntrosCSV lists, across an event's sponsors, every lead someone
// rated hot: who on the sponsor's team thinks so, the lead's details and
// the team's notes. "John at Harness thinks Jamie is hot" is an intro the
// organizers can make after the event.
func WriteHotIntrosCSV(w io.Writer, intros []HotIntro, ix *GuestIndex, guests []Guest, tz, event string) error {
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		loc = time.UTC
	}
	if _, err := io.WriteString(w, "\ufeff"); err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	if err := cw.Write(hotIntrosHeader); err != nil {
		return err
	}
	for _, in := range intros {
		l := in.Lead
		var by []string
		for _, t := range l.Takes {
			if t.Rating == "hot" {
				by = append(by, t.By)
			}
		}
		if len(by) == 0 && len(l.Takes) == 0 && l.Rating == "hot" {
			by = append(by, firstScanner(&l))
		}
		if len(by) == 0 {
			continue
		}
		first, last, email, title, company, linkedin := "", "", "", "", "", ""
		if g, ok := lookupFor(ix, l.Key, event); ok {
			first, last = g.FirstName, g.LastName
			if first == "" && last == "" {
				first, last, _ = strings.Cut(g.Name, " ")
			}
			email, title, company, linkedin = g.Email, g.Title, g.Company, g.LinkedIn
		} else {
			switch l.Key.Kind {
			case "linkedin":
				linkedin = "https://www.linkedin.com/in/" + l.Key.Value
			case "email":
				email = l.Key.Value
			}
		}
		var names, emails, checks []string
		for _, b := range by {
			r := FindRater(b, in.Sponsor, in.Domain, guests)
			if r.Email != "" {
				names, emails = append(names, r.Name), append(emails, r.Email)
			} else {
				checks = append(checks, r.Check)
			}
		}
		rec := []string{in.Sponsor, strings.Join(by, ", "), strings.Join(names, ", "), strings.Join(emails, ", "), strings.Join(checks, "; "),
			first, last, email, title, company, linkedin, notesLines(l.Takes),
			l.ScannedAt.In(loc).Format("2006-01-02 15:04:05")}
		for i := range rec {
			rec[i] = csvSafe(rec[i])
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

var historyHeader = []string{"Time", "Who", "Action", "Lead", "Company", "Details", "Log"}

// WriteHistoryCSV writes the scanning history: every scan, rating and note
// in the order it happened, each with a one-line Log entry ("10:42 Jamie
// marks John Smith (Acme) as hot") for a person or an AI to read.
func WriteHistoryCSV(w io.Writer, sp *Sponsor, events []Event, leads []Lead, ix *GuestIndex) error {
	loc, err := time.LoadLocation(sp.EventTZ)
	if err != nil || sp.EventTZ == "" {
		loc = time.UTC
	}
	byID := make(map[string]Lead, len(leads))
	for _, l := range leads {
		byID[l.ScanID] = l
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].At.Before(events[j].At) })
	if _, err := io.WriteString(w, "\ufeff"); err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	if err := cw.Write(historyHeader); err != nil {
		return err
	}
	for _, e := range events {
		name, company := "someone", ""
		if l, ok := byID[e.ScanID]; ok {
			if g, ok := lookupFor(ix, l.Key, sp.EventName); ok {
				name, company = g.Name, g.Company
			} else {
				switch l.Key.Kind {
				case "linkedin":
					name = "linkedin.com/in/" + l.Key.Value
				case "email":
					name = l.Key.Value
				default:
					name = "Luma guest " + l.Key.Value
				}
			}
		}
		who := e.By
		if who == "" {
			who = noName
		}
		lead := name
		if company != "" {
			lead += " (" + company + ")"
		}
		var action, details, log string
		switch e.Action {
		case "scan":
			action, log = "scanned", who+" scans "+lead
		case "rescan":
			action, log = "scanned again", who+" also scans "+lead
		case "rate":
			action, details = "rated", ratingLabel(e.Rating)
			log = who + " marks " + lead + " as " + details
		case "unrate":
			action, log = "cleared rating", who+" clears their rating of "+lead
		case "note":
			action, details = "added note", e.Note
			log = fmt.Sprintf("%s adds a note about %s: %q", who, lead, e.Note)
		case "renote":
			action, details = "edited note", e.Note
			log = fmt.Sprintf("%s changes their note about %s: %q", who, lead, e.Note)
		case "unnote":
			action, log = "removed note", who+" removes their note about "+lead
		default:
			continue
		}
		when := ""
		if !e.At.IsZero() {
			when = e.At.In(loc).Format("2006-01-02 15:04:05")
			log = e.At.In(loc).Format("15:04") + " " + log
		}
		rec := []string{when, who, action, name, company, details, log}
		for i := range rec {
			rec[i] = csvSafe(rec[i])
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
