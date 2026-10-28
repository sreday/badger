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
	At       time.Time
	By       string
	Action   string // scan, rescan, rate, unrate, note, renote, unnote, decide
	ScanID   string
	Rating   string
	Note     string
	Decision string
}

// Decisions are the marketing person's call on a lead, made on the
// dashboard. A decision never changes anyone's take: the team's opinions
// stay as they were, next to the outcome.
var decisions = map[string]bool{"follow up": true, "nurture": true, "drop": true, "": true}

// noteTags are the phone's quick chips, counted across the team's notes.
var noteTags = []string{"Agreed to meet", "Wants to try it", "Solving this now", "Decision maker", "Uses a competitor", "Job seeker"}

// Verdict is how much the team agrees on a lead: "agreed" (everyone gave
// the same rating), "leaning" (a clear majority with dissent: Lean is the
// majority's rating), "split" (no majority), or "" (nobody rated).
type Verdict struct {
	Agreement string         `json:"agreement"`
	Lean      string         `json:"lean,omitempty"`
	Votes     map[string]int `json:"votes"`
	Rated     int            `json:"rated"`
}

func verdictOf(takes []Take) Verdict {
	v := Verdict{Votes: map[string]int{}}
	for _, t := range takes {
		if t.Rating != "" {
			v.Votes[t.Rating]++
			v.Rated++
		}
	}
	top, n, tie := "", 0, false
	for _, r := range []string{"hot", "unsure", "cold"} {
		switch c := v.Votes[r]; {
		case c > n:
			top, n, tie = r, c, false
		case c == n && c > 0:
			tie = true
		}
	}
	switch {
	case v.Rated == 0:
	case n == v.Rated:
		v.Agreement, v.Lean = "agreed", top
	case !tie && n*2 > v.Rated:
		v.Agreement, v.Lean = "leaning", top
	default:
		v.Agreement = "split"
	}
	return v
}

// label is the team rating in words: "hot", "leaning hot (2 of 3)", "split".
func (v Verdict) label() string {
	switch v.Agreement {
	case "agreed":
		return ratingLabel(v.Lean)
	case "leaning":
		return fmt.Sprintf("leaning %s (%d of %d)", ratingLabel(v.Lean), v.Votes[v.Lean], v.Rated)
	case "split":
		return RatingSplit
	}
	return ""
}

// votesText is "2 hot, 1 cold".
func (v Verdict) votesText() string {
	var parts []string
	for _, r := range []string{"hot", "unsure", "cold"} {
		if c := v.Votes[r]; c > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c, ratingLabel(r)))
		}
	}
	return strings.Join(parts, ", ")
}

// TagCount is one quick tag and how many people on the team used it.
type TagCount struct {
	Tag string `json:"tag"`
	N   int    `json:"n"`
}

func tagsOf(takes []Take) []TagCount {
	var out []TagCount
	for _, tag := range noteTags {
		n := 0
		for _, t := range takes {
			if strings.Contains(t.Note, tag) {
				n++
			}
		}
		if n > 0 {
			out = append(out, TagCount{tag, n})
		}
	}
	return out
}

// tagsText is "Agreed to meet x2; Decision maker".
func tagsText(tags []TagCount) string {
	parts := make([]string, 0, len(tags))
	for _, t := range tags {
		if t.N > 1 {
			parts = append(parts, fmt.Sprintf("%s x%d", t.Tag, t.N))
		} else {
			parts = append(parts, t.Tag)
		}
	}
	return strings.Join(parts, "; ")
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
	Lead    Lead
}

var hotIntrosHeader = []string{"Sponsor", "Rated hot by", "Team rating", "Decision", "First Name", "Last Name", "Email", "Job Title", "Company", "LinkedIn", "Notes", "Scanned At"}

// WriteHotIntrosCSV lists, across an event's sponsors, every lead someone
// rated hot: who on the sponsor's team thinks so, the lead's details and
// the team's notes. "John at Harness thinks Jamie is hot" is an intro the
// organizers can make after the event.
func WriteHotIntrosCSV(w io.Writer, intros []HotIntro, ix *GuestIndex, tz string) error {
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
		if g, ok := ix.Lookup(l.Key); ok {
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
		rec := []string{in.Sponsor, strings.Join(by, ", "), verdictOf(l.Takes).label(), l.Decision, first, last, email, title, company, linkedin, l.Note,
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
			if g, ok := ix.Lookup(l.Key); ok {
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
		case "decide":
			if e.Decision == "" {
				action, log = "cleared decision", who+" clears the decision on "+lead
			} else {
				action, details = "decided", e.Decision
				log = who + " decides: " + e.Decision + " for " + lead
			}
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
