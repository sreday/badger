package snore

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sreday/badger/bridge"
)

// The sponsor leads: companies to act on now, picked by what we saw
// ourselves. Who registered for the next event, who came to the last ones,
// who answered someone on the team, who spoke, which sponsorship talk went
// quiet and who sponsored before. A company gets on a brand's list only
// through a string of that kind, and each lead names the people we know.

const (
	roomWindow  = 24 * month // a registration or a talk older than this says little
	replyWindow = 12 * month
	roomCrowd   = 3 // this many people make a company a lead without anything else
	peopleMax   = 5 // people we know, per lead
	sponsorPast = 48 * month
)

// leadPerson is someone at the company we already know.
type leadPerson struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Profile string `json:"profile,omitempty"`
	Role    string `json:"role,omitempty"`
	Did     string `json:"did"`             // what they did with us, in a few words
	Mail    string `json:"email,omitempty"` // "yes" or "guessed": the address itself stays here
	Email   string `json:"-"`
	Basis   string `json:"-"` // what a guessed address rests on
	rank    int
	last    time.Time
}

type Candidate struct {
	Key     string `json:"key"`
	Domain  string `json:"domain,omitempty"`
	Company string `json:"company"`
	Brand   string `json:"brand"`
	Why     string `json:"why_now"` // our own facts, one line

	Coming  int `json:"registered_for_next,omitempty"` // people registered for an event still ahead
	Seen    int `json:"people_at_events,omitempty"`    // distinct people at this brand's events, two years
	Came    int `json:"came,omitempty"`
	Inside  int `json:"inside_contacts,omitempty"` // of the people we know: marketing, events, devrel, partners, founders
	Replied int `json:"replied,omitempty"`
	Spoke   int `json:"spoke,omitempty"`

	People    []leadPerson `json:"people_we_know,omitempty"`
	TalkWhy   string       `json:"old_talk,omitempty"` // "Talk went quiet", "Said no budget", "Dead end"
	Talk      *bridge.Talk `json:"talk,omitempty"`
	Sponsored []string     `json:"sponsored,omitempty"` // our events they paid for, oldest first
	Score     float64      `json:"-"`
}

var brandWords = []struct{ word, brand string }{
	{"conf42", "conf42"}, {"excels_for_www", "conf42"}, {"platform", "platformday"}, {"llm", "llmday"}, {"prompt", "llmday"},
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

// rankWeights: what each kind of string is worth when the leads are put in
// order. The numbers in use are kept with the data (sponsors-rank.json),
// not here: without that file every string counts the same.
type rankWeights struct {
	Coming       float64 `json:"coming"`
	Came         float64 `json:"came"`
	Seen         float64 `json:"seen"`
	Inside       float64 `json:"inside"`
	Replied      float64 `json:"replied"`
	Spoke        float64 `json:"spoke"`
	Quiet        float64 `json:"quiet"`
	Sponsor      float64 `json:"sponsor"`
	SponsorOther float64 `json:"sponsor_other"`
	Cap          float64 `json:"cap"`   // people past this many add nothing
	Half         float64 `json:"half"`  // what a string older than three months keeps
	Third        float64 `json:"third"` // and one older than a year
}

func flatWeights() rankWeights {
	return rankWeights{1, 1, 1, 1, 1, 1, 1, 1, 1, 5, 1, 1}
}

func (s *Server) rankWeights() rankWeights {
	w := flatWeights()
	readJSON(filepath.Join(s.dir, "sponsors-rank.json"), &w)
	if w.Cap < 1 {
		w.Cap = 1
	}
	return w
}

// What a person did, the most telling first.
const (
	didSpoke = iota
	didReplied
	didComing
	didCame
	didRegistered
	didSubmitted
	didConnected
	didNothing
)

// didOf: what one touch says about the person, and whether it still counts.
func didOf(t Touch, now time.Time) (rank int, words string) {
	age := now.Sub(t.At)
	ev := t.Event
	if ev == "" {
		ev = "one of our events"
	}
	switch t.Kind {
	case TouchSpoke:
		if age <= roomWindow {
			return didSpoke, "spoke at " + ev
		}
	case TouchReplied:
		if age <= replyWindow {
			by := t.By
			if by == "" || by == Organizer {
				by = "us"
			}
			return didReplied, "replied to " + by + " in " + t.At.Format("Jan 2006")
		}
	case TouchRegistered:
		if t.Soon {
			return didComing, "registered for " + ev
		}
		if age <= roomWindow {
			return didRegistered, "registered for " + ev
		}
	case TouchCheckedIn:
		if age <= roomWindow {
			return didCame, "came to " + ev
		}
	case TouchSubmitted:
		if age <= replyWindow {
			return didSubmitted, "sent in a talk for " + ev
		}
	case TouchConnected:
		if age <= replyWindow {
			by := t.By
			if by == "" || by == Organizer {
				by = "us"
			}
			return didConnected, "connected with " + by
		}
	}
	return didNothing, ""
}

func people(n int) string {
	if n == 1 {
		return "1 person"
	}
	return fmt.Sprintf("%d people", n)
}

func times(n int) string {
	switch n {
	case 1:
		return "once"
	case 2:
		return "twice"
	}
	return fmt.Sprintf("%d times", n)
}

// sponsorPool builds the leads of every brand, the best first within each.
// Left out: a company already booked for an event ahead, one in a live
// conversation (a talk younger than a month, or snoozed), and one where all
// we have is an engineer or two who registered.
func (s *Server) sponsorPool(now time.Time) []Candidate {
	s.mu.Lock()
	touches, names, shapes := s.touches, s.companyNames, s.mailShapes
	s.mu.Unlock()
	w := s.rankWeights()
	by := map[string][]Touch{}
	for _, t := range touches {
		by[t.Company] = append(by[t.Company], t)
	}
	paid := map[string][]Touch{}
	for _, t := range s.fp.SponsorTouches() {
		paid[t.Company] = append(paid[t.Company], t)
		if _, ok := by[t.Company]; !ok {
			by[t.Company] = nil
		}
	}
	sponsorName := s.fp.sponsorNames()
	// A live conversation hides the company whatever else we know.
	talking := map[string]bool{}
	if s.cfg.Talks != nil {
		for _, k := range s.cfg.Talks() {
			if d := companyDomain(k.Domain); d != "" && (k.Bucket == "snoozed" || now.Sub(k.LastAt) < quietAfter) {
				talking[d] = true
			}
		}
	}
	quiet := map[string]reviveRow{}
	for _, r := range s.reviveRows(now) {
		if r.Talk == nil {
			continue
		}
		quiet[r.Domain] = r
		if _, ok := by[r.Domain]; !ok {
			by[r.Domain] = nil
		}
	}

	var out []Candidate
	for key, ts := range by {
		if talking[key] || ownDomains[key] {
			continue
		}
		home := topBrand(append(append([]Touch{}, ts...), paid[key]...))
		rv, hasTalk := quiet[key]
		if hasTalk {
			if b := brandOf(rv.Talk.Subject + " " + rv.Talk.Label); b != "" && len(ts) == 0 && len(paid[key]) == 0 {
				home = b
			}
		}
		for _, br := range sponsorBrands {
			b := br.ID
			in := func(t Touch) bool {
				tb := brandOf(t.Event)
				return tb == b || (tb == "" && b == home)
			}
			c := Candidate{Key: key, Brand: b, Company: names[key]}
			if n := sponsorName[key]; n != "" {
				c.Company = n // as our own event pages write it
			}
			if !strings.HasPrefix(key, "name:") {
				c.Domain = key
			}
			// Sponsorships: this brand's count for the list, any brand's for the order.
			booked, other := false, false
			var lastPaid time.Time
			for _, t := range paid[key] {
				if !in(t) {
					other = other || now.Sub(t.At) < sponsorPast
					continue
				}
				if t.At.After(now) {
					booked = true
				}
				if now.Sub(t.At) < sponsorPast && !t.At.After(now) {
					c.Sponsored = append(c.Sponsored, t.Event)
					if t.At.After(lastPaid) {
						lastPaid = t.At
					}
				}
			}
			if booked {
				continue
			}
			// The people, each with the most telling thing they did.
			type acc struct {
				leadPerson
				coming, seen, came, replied, spoke bool
			}
			ppl := map[string]*acc{}
			var last time.Time
			comingAt, lastEvent, lastCame := "", "", time.Time{}
			for _, t := range ts {
				rank, did := didOf(t, now)
				if rank == didNothing {
					continue
				}
				mine := in(t)
				k := personKey(t)
				p := ppl[k]
				if p == nil {
					p = &acc{leadPerson: leadPerson{Name: t.Person, Title: t.Title, Profile: t.Profile, Role: roleOf(t.Title), rank: 2*didNothing + 1}}
					ppl[k] = p
				}
				if p.Email == "" && mailFits(t.Email, t.Person) {
					p.Email = t.Email
				}
				if p.Title == "" && t.Title != "" {
					p.Title, p.Role = t.Title, roleOf(t.Title)
				}
				// What they did for this brand goes before what they did elsewhere.
				r := rank
				if !mine {
					r += didNothing
				}
				if r < p.rank || (r == p.rank && t.At.After(p.last)) {
					p.rank, p.Did, p.last = r, did, t.At
				}
				if !mine {
					continue
				}
				if t.At.After(last) {
					last = t.At
				}
				switch rank {
				case didComing:
					p.coming = true
					comingAt = t.Event
				case didCame:
					p.seen, p.came = true, true
					if t.At.After(lastCame) {
						lastCame, lastEvent = t.At, t.Event
					}
				case didRegistered:
					p.seen = true
				case didSpoke:
					p.seen, p.spoke = true, true
				case didReplied:
					p.replied = true
				}
			}
			var replier, speaker *acc
			for _, p := range ppl {
				if p.rank >= didNothing {
					continue // known only from another brand's events
				}
				if p.coming {
					c.Coming++
				}
				if p.seen {
					c.Seen++
				}
				if p.came {
					c.Came++
				}
				if p.replied {
					c.Replied++
					if replier == nil || p.last.After(replier.last) {
						replier = p
					}
				}
				if p.spoke {
					c.Spoke++
					if speaker == nil || p.last.After(speaker.last) {
						speaker = p
					}
				}
				if insideRole(p.Role) {
					c.Inside++
				}
			}
			if hasTalk && b == home {
				c.TalkWhy, c.Talk = rv.Why, rv.Talk
				if c.Company == "" || (nameFits(key, rv.Company) && !nameFits(key, c.Company)) {
					c.Company = rv.Company
				}
			}
			crowd := c.Seen
			if c.Coming > crowd {
				crowd = c.Coming
			}
			if c.Inside == 0 && crowd < roomCrowd && c.Replied == 0 && c.Spoke == 0 && c.Talk == nil && len(c.Sponsored) == 0 {
				continue
			}
			// The people to show: an inside contact first, then by what they did.
			var list []leadPerson
			for _, p := range ppl {
				if p.Did == "" || (p.Profile == "" && p.Email == "") {
					continue
				}
				q := p.leadPerson
				q.Title = shortTitle(q.Title)
				if q.Email != "" {
					q.Mail = "yes"
				} else if g, basis := guessMail(shapes, c.Domain, q.Name); g != "" {
					q.Email, q.Basis, q.Mail = g, basis, "guessed"
				}
				list = append(list, q)
			}
			sort.Slice(list, func(i, j int) bool {
				a, b := list[i], list[j]
				if insideRole(a.Role) != insideRole(b.Role) {
					return insideRole(a.Role)
				}
				if a.rank != b.rank {
					return a.rank < b.rank
				}
				if !a.last.Equal(b.last) {
					return a.last.After(b.last)
				}
				return a.Name < b.Name
			})
			if len(list) > peopleMax {
				list = list[:peopleMax]
			}
			c.People = list
			// Someone to write to, an old conversation to pick up, or a past
			// sponsor (the daily read finds who to ask): otherwise no lead.
			if len(list) == 0 && c.Talk == nil && len(c.Sponsored) == 0 {
				continue
			}
			if c.Company == "" {
				c.Company = sponsorName[key]
			}
			if c.Company == "" {
				c.Company = strings.TrimPrefix(key, "name:")
			}

			// Why now, in our own facts.
			var why []string
			if n := len(c.Sponsored); n > 0 {
				why = append(why, "Sponsored "+brandTitles[b]+" "+times(n)+", last in "+lastPaid.Format("Jan 2006"))
			}
			if c.Talk != nil {
				why = append(why, c.TalkWhy+" in "+c.Talk.LastAt.Format("Jan 2006"))
			}
			if c.Coming > 0 {
				why = append(why, people(c.Coming)+" registered for "+comingAt)
			}
			switch {
			case c.Came > 0 && lastEvent != "":
				why = append(why, people(c.Came)+" came to our events, last "+lastEvent)
			case c.Came > 0:
				why = append(why, people(c.Came)+" came to our events")
			case c.Seen-c.Spoke > 0:
				why = append(why, people(c.Seen-c.Spoke)+" registered for our events")
			}
			if replier != nil {
				why = append(why, replier.Name+" "+replier.Did)
			}
			if speaker != nil && speaker != replier {
				why = append(why, speaker.Name+" "+speaker.Did)
			}
			if len(why) > 3 {
				why = why[:3]
			}
			c.Why = strings.Join(why, ". ") + "."

			// The order.
			capped := func(n int) float64 {
				if float64(n) > w.Cap {
					return w.Cap
				}
				return float64(n)
			}
			age := 1.0
			switch d := now.Sub(last); {
			case d > 12*month:
				age = w.Third
			case d > 3*month:
				age = w.Half
			}
			c.Score = age * (w.Coming*capped(c.Coming) + w.Came*capped(c.Came) + w.Seen*capped(c.Seen-c.Came) + w.Inside*capped(c.Inside) +
				w.Replied*capped(c.Replied) + w.Spoke*capped(c.Spoke))
			if c.Talk != nil {
				c.Score += w.Quiet
			}
			if len(c.Sponsored) > 0 {
				c.Score += w.Sponsor
			} else if other {
				c.Score += w.SponsorOther
			}
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Brand != b.Brand {
			return a.Brand < b.Brand
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if n, m := len(a.People), len(b.People); n != m {
			return n > m
		}
		return a.Key < b.Key
	})
	return out
}

// sponsorNames: a sponsor's name as our event pages write it, by domain.
func (fp *Footprint) sponsorNames() map[string]string {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	out := map[string]string{}
	for _, f := range fp.F {
		for _, sp := range f.Sponsors {
			if sp.Domain != "" && sp.Name != "" {
				out[sp.Domain] = sp.Name
			}
		}
	}
	return out
}

// folderTitle: an event's name as we would say it.
func folderTitle(f *Folder) string {
	if f.Brand == "conf42" {
		return "Conf42 " + f.Name
	}
	return brandTitles[f.Brand] + " " + f.Name
}

// syncTouches reads the outreach log again, adds who is registered for the
// events still ahead, and learns how each company builds its addresses.
// Called without the lock.
func (s *Server) syncTouches() {
	if s.cfg.HumansDB == "" || s.readTouches == nil {
		return
	}
	d, err := s.readTouches(context.Background(), s.cfg.HumansDB)
	if err != nil {
		log.Printf("Snore: outreach log: %v", strings.ReplaceAll(err.Error(), s.cfg.HumansDB, "<db>"))
		return
	}
	// The lineup sheets, when their feed is set. A failed read keeps the
	// last good one.
	s.mu.Lock()
	sheet := s.sheetRows
	s.mu.Unlock()
	if s.cfg.SpeakerFeed != "" && s.readFeed != nil {
		if rows, err := s.readFeed(s.cfg.SpeakerFeed); err != nil {
			log.Printf("Snore: speaker feed: %v", err)
		} else {
			sheet = rows
		}
	}
	d.sheet = sheet

	// Our events by registration id: a speaker row names its event that
	// way, and the ones still ahead have a guest list worth reading.
	now := s.now()
	type ahead struct{ id, title string }
	var soon []ahead
	title := map[string]string{}
	s.fp.mu.Lock()
	for _, f := range s.fp.F {
		if f.Luma == "" {
			continue
		}
		title[f.Luma] = folderTitle(f)
		if f.Start.After(now.Add(-24 * time.Hour)) {
			soon = append(soon, ahead{f.Luma, folderTitle(f)})
		}
	}
	s.fp.mu.Unlock()
	for i := range d.speakers {
		if t := title[d.speakers[i].event]; t != "" {
			d.speakers[i].event = t
		}
	}
	coming := 0
	if s.cfg.Guests != nil {
		for _, e := range soon {
			for _, g := range s.cfg.Guests(e.id) {
				slug := ""
				if i := strings.Index(g.LinkedIn, "/in/"); i >= 0 {
					slug = strings.ToLower(strings.Trim(g.LinkedIn[i+4:], "/ "))
				}
				d.attendees = append(d.attendees, humanAttendee{slug: slug, email: g.Email, name: g.Name, event: e.title,
					registered: now, title: g.Title, company: g.Company, soon: true})
				coming++
			}
		}
	}

	var known []namedMail
	profName := map[string]string{}
	for _, p := range d.profiles {
		profName[p.slug] = p.name
	}
	for _, a := range d.attendees {
		name := profName[a.slug]
		if name == "" {
			name = a.name
		}
		known = append(known, namedMail{name, a.email})
	}
	for _, sp := range sheet {
		known = append(known, namedMail{sp.Name, sp.Email})
	}
	shapes := learnShapes(known)
	touches, names := buildTouches(d)
	s.mu.Lock()
	s.touches, s.companyNames, s.mailShapes, s.touchesAt, s.sheetRows = touches, names, shapes, s.now(), sheet
	s.mu.Unlock()
	log.Printf("Snore: outreach log read, %d touch(es) at %d compan(ies), %d lineup row(s), %d registration(s) for %d event(s) ahead, address shapes for %d compan(ies)",
		len(touches), len(names), len(sheet), coming, len(soon), len(shapes))
}

// mailFits: the address can be this person's. A work address is taken as
// it is; a private one must carry part of their name, because the outreach
// log now and then ties one person's address to another's profile.
func mailFits(email, name string) bool {
	if email == "" {
		return false
	}
	if workDomain(email) != "" {
		return true
	}
	local := nonWord.ReplaceAllString(strings.ToLower(email[:strings.LastIndexByte(email, '@')]), "")
	for _, w := range strings.Fields(normName(name)) {
		if len(w) >= 3 && strings.Contains(local, w) {
			return true
		}
	}
	return false
}

// shortTitle: a profile headline cut down to the job itself ("SRE | Speaker
// | Helping teams ..." is "SRE").
func shortTitle(t string) string {
	for _, sep := range []string{" | ", " • ", " · ", " || ", " @ "} {
		if i := strings.Index(t, sep); i > 0 {
			t = t[:i]
		}
	}
	return clip(strings.TrimSpace(t), 60)
}
