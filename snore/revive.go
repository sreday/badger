package snore

import (
	"sort"
	"time"

	"github.com/sreday/badger/bridge"
)

// The Revive list: companies that talked to us about sponsoring and went
// quiet without becoming a sponsor, and sponsors who have not been back
// for a year. The mail side comes from Snap through a read-only function.

const (
	quietAfter    = 30 * 24 * time.Hour  // a talk with no mail for this long went quiet
	closedAfter   = 180 * 24 * time.Hour // a dead end or "no budget" comes back only after this
	currentWithin = 12 * month           // sponsored within this: a current sponsor, not a lead
	lapsedWithin  = 36 * month           // a sponsor gone longer than this is cold again
)

type reviveRow struct {
	Domain, Company string
	Why             string // "Talk went quiet", "Said no budget", "Dead end", "Lapsed sponsor"
	Quiet           int    // days since the last mail, or since the last sponsored event
	Talk            *bridge.Talk
	Events          []string // what they sponsored, oldest first
	LastEvent       time.Time
	Level           int // how warm otherwise, from the touches
	Owner           string
}

// reviveRows works the list out from the sponsor talks and our history.
func (s *Server) reviveRows(now time.Time) []reviveRow {
	var talks []bridge.Talk
	if s.cfg.Talks != nil {
		talks = s.cfg.Talks()
	}
	// Our history per company: what they sponsored and when last.
	type past struct {
		events []string
		last   time.Time
	}
	hist := map[string]*past{}
	for _, t := range s.fp.SponsorTouches() {
		p := hist[t.Company]
		if p == nil {
			p = &past{}
			hist[t.Company] = p
		}
		p.events = append(p.events, t.Event)
		if t.At.After(p.last) {
			p.last = t.At
		}
	}
	current := func(d string) bool {
		p := hist[d]
		return p != nil && now.Sub(p.last) < currentWithin
	}
	s.mu.Lock()
	touches, names := s.touches, s.companyNames
	s.mu.Unlock()
	by := map[string][]Touch{}
	for _, t := range touches {
		by[t.Company] = append(by[t.Company], t)
	}

	rows := map[string]*reviveRow{}
	for i := range talks {
		k := talks[i]
		d := companyDomain(k.Domain)
		if d == "" || freeMail[d] || current(d) || k.Bucket == "snoozed" {
			continue
		}
		quiet := now.Sub(k.LastAt)
		why := "Talk went quiet"
		switch {
		case k.NoBudget:
			why = "Said no budget"
		case k.DeadEnd:
			why = "Dead end"
		}
		if quiet < quietAfter || ((k.NoBudget || k.DeadEnd) && quiet < closedAfter) {
			continue
		}
		if r := rows[d]; r != nil && !k.LastAt.After(r.Talk.LastAt) {
			continue
		}
		rows[d] = &reviveRow{Domain: d, Company: k.Company, Why: why, Quiet: int(quiet.Hours() / 24), Talk: &talks[i]}
	}
	for d, p := range hist {
		gone := now.Sub(p.last)
		if gone < currentWithin || gone > lapsedWithin {
			continue
		}
		r := rows[d]
		if r == nil {
			r = &reviveRow{Domain: d, Why: "Lapsed sponsor", Quiet: int(gone.Hours() / 24)}
			rows[d] = r
		}
		r.Events, r.LastEvent = p.events, p.last
	}
	out := make([]reviveRow, 0, len(rows))
	for d, r := range rows {
		if r.Company == "" {
			r.Company = names[d]
		}
		if r.Company == "" {
			r.Company = d
		}
		w := warmthOf(by[d], now)
		r.Level, r.Owner = w.Level, w.Owner
		out = append(out, *r)
	}
	// Past sponsors first, then the talks that went quiet most recently.
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (len(a.Events) > 0) != (len(b.Events) > 0) {
			return len(a.Events) > 0
		}
		if a.Quiet != b.Quiet {
			return a.Quiet < b.Quiet
		}
		return a.Domain < b.Domain
	})
	return out
}
