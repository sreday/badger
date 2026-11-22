package snore

import (
	"sort"
	"strings"
	"time"

	"github.com/sreday/badger/bridge"
)

// The candidate pool: every company we hold a string to, with all of it in
// one place (how warm, who knows whom, a talk that went quiet, who came
// to our events, what they sponsored). Each candidate is a dossier for the
// daily read; whether the company fits at all is judged there, not here.

const (
	LabelWarmedUp = "Warmed up" // a teammate holds a contact who answered
	LabelRevive   = "Revive"    // a sponsorship talk went quiet, or a sponsor did not come back
	LabelWarm     = "Warm"      // their people speak at, come to or register for our events
)

type Candidate struct {
	Key     string `json:"key"`
	Domain  string `json:"domain,omitempty"`
	Company string `json:"company"`
	Brand   string `json:"brand"` // the brand most of the strings point to
	Label   string `json:"label"`
	Warmth
	Stale     bool         `json:"stale,omitempty"`
	ReviveWhy string       `json:"revive_why,omitempty"`
	Quiet     int          `json:"quiet_days,omitempty"`
	Talk      *bridge.Talk `json:"talk,omitempty"`
	Seen      int          `json:"people_at_events,omitempty"` // distinct people at our events, two years
	Came      int          `json:"came,omitempty"`
	Inside    []roomPerson `json:"inside_contacts,omitempty"`
	Sponsored []string     `json:"sponsored,omitempty"` // our events they paid for, oldest first
}

var brandWords = []struct{ word, brand string }{
	{"platform", "platformday"}, {"llm", "llmday"}, {"prompt", "llmday"}, {"conf42", "conf42"}, {"excels_for_www", "conf42"},
	{"online", "conf42"}, {"sre", "sreday"},
}

// brandOf guesses which of our brands an event name belongs to, "" when
// the name does not say.
func brandOf(event string) string {
	e := strings.ToLower(event)
	for _, b := range brandWords {
		if strings.Contains(e, b.word) {
			return b.brand
		}
	}
	return ""
}

// topBrand: the brand most touches point to; our first brand when none says.
func topBrand(touches []Touch) string {
	n := map[string]int{}
	for _, t := range touches {
		if b := brandOf(t.Event); b != "" {
			n[b]++
		}
	}
	best, top := "sreday", 0
	for _, b := range []string{"sreday", "llmday", "platformday", "conf42"} {
		if n[b] > top {
			best, top = b, n[b]
		}
	}
	return best
}

var labelRank = map[string]int{LabelWarmedUp: 0, LabelRevive: 1, LabelWarm: 2}

// sponsorPool builds the pool. Left out: this year's sponsors, companies in
// a live conversation (a talk younger than a month, or snoozed), and
// companies where all we have is one engineer who registered once.
func (s *Server) sponsorPool(now time.Time) []Candidate {
	s.mu.Lock()
	touches, names := s.touches, s.companyNames
	s.mu.Unlock()
	by := map[string][]Touch{}
	for _, t := range touches {
		by[t.Company] = append(by[t.Company], t)
	}
	sponsored := map[string][]string{}
	current := map[string]bool{}
	for _, t := range s.fp.SponsorTouches() {
		by[t.Company] = append(by[t.Company], t)
		sponsored[t.Company] = append(sponsored[t.Company], t.Event)
		if now.Sub(t.At) < currentWithin {
			current[t.Company] = true
		}
	}
	// A live conversation hides the company whatever else we know.
	talking := map[string]bool{}
	if s.cfg.Talks != nil {
		for _, k := range s.cfg.Talks() {
			if d := companyDomain(k.Domain); d != "" && (k.Bucket == "snoozed" || now.Sub(k.LastAt) < quietAfter) {
				talking[d] = true
			}
		}
	}
	revive := map[string]reviveRow{}
	for _, r := range s.reviveRows(now) {
		revive[r.Domain] = r
		if _, ok := by[r.Domain]; !ok {
			by[r.Domain] = nil
		}
	}
	room := map[string]roomRow{}
	for _, r := range s.roomRows(now) {
		room[r.Key] = r
	}

	var out []Candidate
	for key, ts := range by {
		if current[key] || talking[key] {
			continue
		}
		c := Candidate{Key: key, Company: names[key], Warmth: warmthOf(ts, now), Brand: topBrand(ts), Sponsored: sponsored[key]}
		if !strings.HasPrefix(key, "name:") {
			c.Domain = key
		}
		if len(c.Proof) > 5 {
			c.Proof = c.Proof[:5]
		}
		c.Stale = c.Warmth.Stale(now)
		rr, inRoom := room[key]
		if inRoom {
			c.Seen, c.Came, c.Inside = rr.People, rr.Came, rr.Inside
			if len(c.Inside) > 5 {
				c.Inside = c.Inside[:5]
			}
		}
		rv, quiet := revive[key]
		switch {
		case c.Owner != Organizer && c.Level >= 4:
			c.Label = LabelWarmedUp
		case quiet:
			c.Label = LabelRevive
		case c.Level >= 5 || (c.Level >= 2 && (len(c.Inside) > 0 || c.Seen >= roomCrowd)):
			c.Label = LabelWarm
		default:
			continue
		}
		if quiet {
			c.ReviveWhy, c.Quiet, c.Talk = rv.Why, rv.Quiet, rv.Talk
			if c.Company == "" {
				c.Company = rv.Company
			}
		}
		if c.Company == "" {
			c.Company = strings.TrimPrefix(key, "name:")
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if labelRank[a.Label] != labelRank[b.Label] {
			return labelRank[a.Label] < labelRank[b.Label]
		}
		if a.Level != b.Level {
			return a.Level > b.Level
		}
		if len(a.Inside) != len(b.Inside) {
			return len(a.Inside) > len(b.Inside)
		}
		if a.People != b.People {
			return a.People > b.People
		}
		if !a.Last.At.Equal(b.Last.At) {
			return a.Last.At.After(b.Last.At)
		}
		return a.Key < b.Key
	})
	return out
}
