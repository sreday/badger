package snap

// Where an anonymized list (export.go) gets its people: the registrations
// of the events on Luma. Which events, and which answers are no answer, is
// the recipe's to say (exportrecipe.go). Snap reads the events and their approved guests
// with the same Luma key Snout uses, keeps them in memory for a few hours,
// and only ever hands on two things per guest: the job title and the
// company, as typed at registration.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sreday/badger/luma"
)

const (
	exEventsKeep = 30 * time.Minute // the list of events
	exGuestsKeep = 6 * time.Hour    // one event's guests
)

// exEvent is a Luma event as the export sees it.
type exEvent struct {
	ID, Name string
	Start    time.Time
	Brands   []string // sreday, llmday, platformday, pec
	City     string   // lowercased: "san francisco", "nyc"
	Year     string
}

var (
	exYear   = regexp.MustCompile(`\b20\d\d\b`)
	exSpaces = regexp.MustCompile(`\s+`)
	exBrand  = []struct{ key, word string }{
		{"sreday", "sreday"}, {"llmday", "llmday"}, {"platformday", "platformday"},
		{"pec", "prompt engineering"}, {"pec", "pec"},
	}
	exNoise   = regexp.MustCompile(`(?i)\b(sre ?day|llm ?day|platform ?day|prompt engineering conference|pec|conference|q[1-4]|the|edition)\b|[+\-–—:,|()]`)
	cityAlias = map[string]string{"new york": "nyc", "new york city": "nyc", "ny": "nyc", "sf": "san francisco", "bay area": "san francisco", "tlv": "tel aviv"}
)

// exParse reads brand, city and year out of an event's name or out of what
// was asked for ("SREday San Francisco 2026 Q4", "PLATFORMday London").
func exParse(name string) (brands []string, city, year string) {
	low := strings.ToLower(name)
	seen := map[string]bool{}
	for _, b := range exBrand {
		if !seen[b.key] && regexp.MustCompile(`\b`+strings.ReplaceAll(b.word, " ", ` ?`)+`\b`).MatchString(strings.ReplaceAll(low, "sre day", "sreday")) {
			seen[b.key] = true
			brands = append(brands, b.key)
		}
	}
	year = exYear.FindString(low)
	city = exNoise.ReplaceAllString(exYear.ReplaceAllString(low, " "), " ")
	city = strings.TrimSpace(exSpaces.ReplaceAllString(city, " "))
	if a, ok := cityAlias[city]; ok {
		city = a
	}
	return brands, city, year
}

func sameBrand(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

// lumaEvents: the conference events on Luma, newest first.
func (s *Server) lumaEvents() ([]exEvent, error) {
	s.mu.Lock()
	if s.exEvents != nil && s.now().Sub(s.exEventsAt) < exEventsKeep {
		out := s.exEvents
		s.mu.Unlock()
		return out, nil
	}
	s.mu.Unlock()
	list := s.listEvents
	if list == nil {
		list = luma.ListEvents
	}
	raw, err := list(s.cfg.LumaAPIKey)
	if err != nil {
		return nil, fmt.Errorf("Luma events: %w", err)
	}
	var out []exEvent
	for _, e := range raw {
		brands, city, year := exParse(e.Name)
		if len(brands) == 0 || city == "" {
			continue // a meetup, a webinar, a test event
		}
		start, _ := e.Times()
		out = append(out, exEvent{ID: e.APIId, Name: strings.TrimSpace(e.Name), Start: start, Brands: brands, City: city, Year: year})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.After(out[j].Start) })
	s.mu.Lock()
	s.exEvents, s.exEventsAt = out, s.now()
	s.mu.Unlock()
	return out, nil
}

// eventGuests: the approved guests of one event.
func (s *Server) eventGuests(id string) ([]luma.Guest, error) {
	s.mu.Lock()
	if c, ok := s.exGuests[id]; ok && s.now().Sub(c.at) < exGuestsKeep {
		s.mu.Unlock()
		return c.guests, nil
	}
	s.mu.Unlock()
	get := s.getGuests
	if get == nil {
		get = luma.GetGuests
	}
	g, err := get(id, s.cfg.LumaAPIKey)
	if err != nil {
		return nil, fmt.Errorf("Luma guests: %w", err)
	}
	s.mu.Lock()
	if s.exGuests == nil {
		s.exGuests = map[string]exGuestCache{}
	}
	s.exGuests[id] = exGuestCache{guests: g, at: s.now()}
	s.mu.Unlock()
	return g, nil
}

type exGuestCache struct {
	guests []luma.Guest
	at     time.Time
}

// exportEvents picks the events a list is built from. Round 1 is the event
// they asked about; a later round adds what the recipe says.
func (s *Server) exportEvents(asked string, round int) ([]exEvent, error) {
	rc := s.recipe()
	if rc == nil {
		return nil, fmt.Errorf("the lists' recipe is not on the server")
	}
	all, err := s.lumaEvents()
	if err != nil {
		return nil, err
	}
	now := s.now()
	var near []exEvent
	for _, e := range all {
		if e.Start.After(now.Add(-rc.back())) && e.Start.Before(now.Add(rc.ahead())) {
			near = append(near, e)
		}
	}
	// The city is whichever event city the words name ("Send them the London
	// audience" is London), so a loosely worded ask still finds its event.
	brands, _, year := exParse(asked)
	low := " " + strings.ToLower(exSpaces.ReplaceAllString(asked, " ")) + " "
	for from, to := range cityAlias {
		low = strings.ReplaceAll(low, " "+from+" ", " "+to+" ")
	}
	city := ""
	for _, e := range near {
		if len(e.City) > len(city) && strings.Contains(low, e.City) {
			city = e.City
		}
	}
	if city == "" && len(brands) == 0 {
		return nil, fmt.Errorf("can't tell which event %q means", asked)
	}
	pick := func(f func(exEvent) bool) []exEvent {
		var out []exEvent
		for _, e := range near {
			if f(e) {
				out = append(out, e)
			}
		}
		return out
	}
	inCity := func(e exEvent) bool { return city != "" && e.City == city }
	inBrand := func(e exEvent) bool { return len(brands) == 0 || sameBrand(e.Brands, brands) }
	exact := pick(func(e exEvent) bool { return inCity(e) && inBrand(e) && (year == "" || e.Year == year) })
	if len(exact) == 0 {
		exact = pick(func(e exEvent) bool { return inCity(e) && inBrand(e) })
	}
	if len(exact) == 0 {
		exact = pick(inCity)
	}
	if len(exact) == 0 && len(brands) > 0 {
		exact = pick(inBrand)
	}
	if len(exact) == 0 {
		return nil, fmt.Errorf("no event on Luma matches %q", asked)
	}
	first := exact[:1]
	if g, err := s.eventGuests(exact[0].ID); err == nil && len(g) < rc.Thin && len(exact) > 1 {
		first = exact[:2]
	}
	if round <= 1 {
		return first, nil
	}
	out := append([]exEvent{}, first...)
	have := map[string]bool{}
	for _, e := range out {
		have[e.ID] = true
	}
	wider := map[string]func(exEvent) bool{"city": inCity, "brand": func(e exEvent) bool { return len(brands) > 0 && sameBrand(e.Brands, brands) }}
	for _, key := range rc.Wider {
		f, ok := wider[key]
		if !ok {
			continue
		}
		for _, e := range pick(f) {
			if !have[e.ID] && len(out) < rc.Wide {
				have[e.ID] = true
				out = append(out, e)
			}
		}
	}
	return out, nil
}

// exPerson is one registration, reduced to the two things a list may use.
type exPerson struct{ title, company string } // raw, normalized by exKey

var (
	// piiLike: an email address, a link, a phone number: never a title or a company.
	piiLike = regexp.MustCompile(`@|https?:|www\.|\d[\d\s().-]{6,}\d`)
)

// exKey is how a raw answer is looked up: lowercased, spaces collapsed.
func exKey(v string) string {
	v = strings.ToLower(strings.TrimSpace(exSpaces.ReplaceAllString(v, " ")))
	v = strings.TrimRight(v, " .,;:")
	if len(v) > 120 || piiLike.MatchString(v) {
		return ""
	}
	return v
}

// exportPeople: the registrations of these events, one per person. An
// answer the recipe calls no job title, or no company, is dropped here, so
// Claude never sees it.
func (s *Server) exportPeople(ids []string) ([]exPerson, error) {
	rc := s.recipe()
	if rc == nil {
		return nil, fmt.Errorf("the lists' recipe is not on the server")
	}
	var out []exPerson
	seen := map[string]bool{}
	for _, id := range ids {
		guests, err := s.eventGuests(id)
		if err != nil {
			return nil, err
		}
		for _, g := range guests {
			who := strings.ToLower(strings.TrimSpace(g.Email))
			if who == "" {
				who = g.ID
			}
			if seen[who] {
				continue
			}
			seen[who] = true
			p := exPerson{title: exKey(g.Title), company: exKey(g.Company)}
			if rc.dropTitle != nil && rc.dropTitle.MatchString(p.title) {
				p.title = ""
			}
			if rc.dropComp != nil && rc.dropComp.MatchString(p.company) {
				p.company = ""
			}
			if p.title != "" || p.company != "" {
				out = append(out, p)
			}
		}
	}
	return out, nil
}
