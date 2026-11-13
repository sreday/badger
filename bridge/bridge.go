// Package bridge is how Snoop (calls) and Snap (mail) talk to each other
// (Marek 2026-11-13: "think of how Snoop and Snap tools interact").
//
// The Badger tools stay separate: each lives in its own folder and owns its
// own data, tokens and code. Neither imports the other. Each one offers a
// small view of itself through an interface here (Snoop: Calls, Snap: Mail),
// and Snout, which mounts them both, hands each tool the other's view. A
// tool keeps only a link and a short trace of what the other one did.
//
// People are matched by email address, by work domain (not gmail.com and
// the like, not the team's own domains) or by company name. When in doubt
// there is no match: a missing link is better than a wrong one.
package bridge

import (
	"strings"
	"time"
)

// Person is someone on a call or in a thread.
type Person struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
}

// Step is one next step from a call's notes.
type Step struct {
	I    int    `json:"i"`
	Text string `json:"text"`
	Done bool   `json:"done"`
	Mine bool   `json:"mine"` // Mark owns it ("Mark: send the prospectus by Friday")
}

// Call is a call as Snoop shows it to other tools.
type Call struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	URL      string    `json:"url"` // its page in Snoop
	Company  string    `json:"company,omitempty"`
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Updated  time.Time `json:"updated"` // when its notes last changed
	Upcoming bool      `json:"upcoming,omitempty"`
	Sales    bool      `json:"sales,omitempty"`    // booked by the other side through Calendly, a one-off: a real booking, not a standing meeting
	Missed   bool      `json:"missed,omitempty"`   // a sales call nobody showed up to, not followed up
	Resolved bool      `json:"resolved,omitempty"` // that follow-up is done
	Summary  string    `json:"summary,omitempty"`
	Facts    string    `json:"facts,omitempty"` // background on their company
	People   []Person  `json:"people,omitempty"`
	Steps    []Step    `json:"steps,omitempty"`
}

// Open: the steps still to do; mine = only Mark's own.
func (c Call) Open(mine bool) []Step {
	var out []Step
	for _, s := range c.Steps {
		if !s.Done && (s.Mine || !mine) {
			out = append(out, s)
		}
	}
	return out
}

// CallHit is a call found by a search, with the lines that matched.
type CallHit struct {
	Call  Call     `json:"call"`
	Lines []string `json:"lines,omitempty"`
}

// Calls is what Snoop offers.
type Calls interface {
	// Call returns one call.
	Call(id string) (Call, bool)
	// CallsWith: the calls with any of these people or this company, newest
	// first, upcoming ones included.
	CallsWith(emails []string, company string) []Call
	// OpenPromises: past calls that still need something from Mark (an open
	// step, a missed sales call), newest first.
	OpenPromises() []Call
	// SearchCalls looks through titles, notes and transcripts.
	SearchCalls(q string, n int) []CallHit
	// CompanyFacts: what Snoop knows about a company ("" if nothing).
	CompanyFacts(company string) string
}

// DraftRequest asks Snap to draft the email that settles something from a call.
type DraftRequest struct {
	CallID    string
	CallTitle string
	CallURL   string
	CallAt    time.Time
	People    []Person
	Company   string
	Brief     string // what the email is for: the open step, or the missed-call follow-up
	Notes     string // the call's summary and next steps, for the drafter
}

// DraftState is where a requested draft stands.
type DraftState struct {
	Status  string    `json:"status"` // "" (unknown), working, ready, sent, missing
	Subject string    `json:"subject,omitempty"`
	To      string    `json:"to,omitempty"`
	Gmail   string    `json:"gmail,omitempty"` // the draft (or the sent mail) in Gmail
	Snap    string    `json:"snap,omitempty"`  // its place in Snap
	At      time.Time `json:"at,omitempty"`
}

// Card is one of Snap's loose ends.
type Card struct {
	ID       string    `json:"id"`
	Headline string    `json:"headline"`
	Status   string    `json:"status,omitempty"`
	Ball     string    `json:"ball,omitempty"`   // you, them, between
	Bucket   string    `json:"bucket,omitempty"` // active, snoozed, closed
	Event    string    `json:"event,omitempty"`
	LastAt   time.Time `json:"last_at"`
	URL      string    `json:"url"`
}

// Sent is an email Mark sent.
type Sent struct {
	At      time.Time `json:"at"`
	Subject string    `json:"subject"`
	URL     string    `json:"url"`
}

// Mail is what Snap offers.
type Mail interface {
	// Draft starts a draft and returns a reference to ask about later.
	Draft(DraftRequest) (ref string, err error)
	DraftState(ref string) DraftState
	// CardsWith: Snap's cards with any of these people or this company.
	CardsWith(emails []string, company string) []Card
	// SentTo: Mark's own mail to any of these people since a moment.
	SentTo(emails []string, since time.Time) (Sent, bool)
	CompanyFacts(company string) string
}

// freemail domains say nothing about who someone works for.
var freemail = map[string]bool{"gmail.com": true, "googlemail.com": true, "outlook.com": true, "hotmail.com": true, "live.com": true,
	"yahoo.com": true, "icloud.com": true, "me.com": true, "proton.me": true, "protonmail.com": true, "aol.com": true, "gmx.com": true, "wp.pl": true, "o2.pl": true, "onet.pl": true}

// Matcher says whether a person or a company is one of a given set.
type Matcher struct {
	emails  map[string]bool
	domains map[string]bool
	company string
}

// NewMatcher: emails to match, the company (may be ""), and the team's own
// domains, which never count (everyone on the team is on every thread).
func NewMatcher(emails []string, company string, team []string) Matcher {
	own := map[string]bool{}
	for _, d := range team {
		own[strings.ToLower(d)] = true
	}
	m := Matcher{emails: map[string]bool{}, domains: map[string]bool{}, company: Norm(company)}
	for _, e := range emails {
		e = strings.ToLower(strings.TrimSpace(e))
		i := strings.LastIndex(e, "@")
		if i < 0 || own[e[i+1:]] {
			continue
		}
		m.emails[e] = true
		if d := e[i+1:]; !freemail[d] {
			m.domains[d] = true
		}
	}
	return m
}

// Empty: nothing to match on.
func (m Matcher) Empty() bool { return len(m.emails) == 0 && m.company == "" }

// Hit: one of these emails, or this company name, belongs to the set.
func (m Matcher) Hit(emails []string, company string) bool {
	if m.company != "" && Norm(company) == m.company {
		return true
	}
	for _, e := range emails {
		e = strings.ToLower(strings.TrimSpace(e))
		if m.emails[e] {
			return true
		}
		if i := strings.LastIndex(e, "@"); i >= 0 && m.domains[e[i+1:]] {
			return true
		}
	}
	return false
}

// Norm lower-cases a name and squeezes its spaces.
func Norm(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

// Emails lists the addresses of some people.
func Emails(people []Person) []string {
	out := make([]string, 0, len(people))
	for _, p := range people {
		if p.Email != "" {
			out = append(out, p.Email)
		}
	}
	return out
}
