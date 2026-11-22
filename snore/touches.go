package snore

import (
	"sort"
	"time"
)

// A touch is one dated contact between us and a person at a company: a
// sponsorship, a mail thread, a talk, a reply to a teammate, a check-in.
// How warm a company is gets worked out from its touches, never set by hand.

// The kinds of touch, from warmest to coldest.
const (
	TouchSponsor    = "sponsor"    // sponsored or hosted one of our events
	TouchThread     = "thread"     // a two-way mail thread or a call with the organizer
	TouchSpoke      = "spoke"      // spoke at one of our events, or a talk accepted
	TouchReplied    = "replied"    // replied to someone on the team
	TouchCheckedIn  = "checkedin"  // came to an event
	TouchConnected  = "connected"  // connected to someone on the team
	TouchRegistered = "registered" // registered for an event
	TouchSubmitted  = "submitted"  // submitted a talk that was not picked
	TouchInvited    = "invited"    // invited by the team, no answer yet
	TouchSaved      = "saved"      // only saved by the team
)

// Organizer is who acts when no teammate holds the contact.
const Organizer = "organizer"

type Touch struct {
	Company string    `json:"company"`          // the company's website domain
	Person  string    `json:"person,omitempty"` // who at the company
	Profile string    `json:"profile,omitempty"`
	Title   string    `json:"title,omitempty"`
	By      string    `json:"by,omitempty"` // who on our side, empty when nobody in particular
	Kind    string    `json:"kind"`
	Event   string    `json:"event,omitempty"`
	At      time.Time `json:"at"`
}

const month = 30 * 24 * time.Hour

// step: a touch of this age or younger counts as this level.
type step struct {
	within time.Duration
	level  int
}

// ladder: what each kind of touch is worth as it ages. Past the last step
// a touch counts for nothing.
var ladder = map[string][]step{
	TouchSponsor:    {{24 * month, 7}, {48 * month, 5}},
	TouchThread:     {{3 * month, 6}, {12 * month, 4}},
	TouchSpoke:      {{18 * month, 5}},
	TouchReplied:    {{6 * month, 4}},
	TouchCheckedIn:  {{12 * month, 3}},
	TouchConnected:  {{6 * month, 2}},
	TouchRegistered: {{6 * month, 2}},
	TouchSubmitted:  {{6 * month, 2}},
	TouchInvited:    {{3 * month, 1}},
	TouchSaved:      {{3 * month, 1}},
}

// levelAt: what one touch is worth now, 0 to 7.
func levelAt(t Touch, now time.Time) int {
	age := now.Sub(t.At)
	if age < 0 {
		age = 0
	}
	for _, s := range ladder[t.Kind] {
		if age <= s.within {
			return s.level
		}
	}
	return 0
}

// Warmth is how warm one company is, with the touch that says so.
type Warmth struct {
	Level  int     `json:"level"`           // 0 to 7, the warmest person's level
	Best   Touch   `json:"best"`            // the touch behind the level
	Owner  string  `json:"owner"`           // who acts: a teammate, or the organizer
	People int     `json:"people"`          // people at the company with a level of 2 or more
	Last   Touch   `json:"last"`            // the newest touch of any kind
	Proof  []Touch `json:"proof,omitempty"` // each person's best touch, warmest first
}

// personKey: one person at a company, by profile when known, else by name.
func personKey(t Touch) string {
	if t.Profile != "" {
		return "p:" + t.Profile
	}
	return "n:" + normName(t.Person)
}

// warmthOf works out a company's warmth from its touches. The level is the
// warmest person's; a tie goes to the newer touch. The owner is the
// teammate behind the best touch, else the teammate with the warmest touch
// at the company, else the organizer. Levels 5 and up are the organizer's.
func warmthOf(touches []Touch, now time.Time) Warmth {
	var w Warmth
	type scored struct {
		t Touch
		l int
	}
	best := map[string]scored{}
	var mate scored // the warmest touch a teammate holds
	for _, t := range touches {
		if t.At.After(w.Last.At) {
			w.Last = t
		}
		l := levelAt(t, now)
		if l == 0 {
			continue
		}
		k := personKey(t)
		if b, ok := best[k]; !ok || l > b.l || (l == b.l && t.At.After(b.t.At)) {
			best[k] = scored{t, l}
		}
		if t.By != "" && t.By != Organizer && (l > mate.l || (l == mate.l && t.At.After(mate.t.At))) {
			mate = scored{t, l}
		}
	}
	var all []scored
	for _, b := range best {
		all = append(all, b)
		if b.l >= 2 {
			w.People++
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].l != all[j].l {
			return all[i].l > all[j].l
		}
		if !all[i].t.At.Equal(all[j].t.At) {
			return all[i].t.At.After(all[j].t.At)
		}
		return personKey(all[i].t) < personKey(all[j].t)
	})
	for _, a := range all {
		w.Proof = append(w.Proof, a.t)
	}
	if len(all) == 0 {
		w.Owner = Organizer
		return w
	}
	w.Level, w.Best = all[0].l, all[0].t
	switch {
	case w.Level >= 5:
		w.Owner = Organizer
	case w.Best.By != "" && w.Best.By != Organizer:
		w.Owner = w.Best.By
	case mate.l >= 2:
		w.Owner = mate.t.By
	default:
		w.Owner = Organizer
	}
	return w
}

// staleAfter: an owned company with no touch for this long gets a flag.
const staleAfter = 14 * 24 * time.Hour

// Stale: a teammate owns the company and nothing happened for two weeks.
func (w Warmth) Stale(now time.Time) bool {
	return w.Owner != Organizer && w.Level >= 2 && now.Sub(w.Last.At) > staleAfter
}
