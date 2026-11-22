package snore

import (
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"
)

// MaxPerTopic is the most cards a topic keeps.
const MaxPerTopic = 30

// invitedFor: an invited speaker stays off the board this long.
const invitedFor = 90 * 24 * time.Hour

// Card is one speaker prospect on one topic.
type Card struct {
	Key        string    `json:"key"` // normalised name
	Name       string    `json:"name"`
	Photo      string    `json:"photo,omitempty"` // where Claude found it
	PhotoBox   string    `json:"photo_file,omitempty"`
	PhotoTried time.Time `json:"photo_tried,omitempty"`
	Humans     Humans    `json:"humans"`
	CFP        CFP       `json:"cfp"`
	Title      string    `json:"title,omitempty"`
	Company    string    `json:"company,omitempty"`
	Base       Base      `json:"base"`
	Cities     []string  `json:"our_cities,omitempty"` // in-person: our cities within reach
	LinkedIn   string    `json:"linkedin_url,omitempty"`
	Reach      Reach     `json:"reach"`
	Talks      []Talk    `json:"talks,omitempty"`
	Topics     []string  `json:"topics,omitempty"`
	Match      int       `json:"match"`
	Parts      Parts     `json:"parts"`
	Why        string    `json:"why,omitempty"`
	Links      []string  `json:"sources,omitempty"`
	First      time.Time `json:"first_seen"`
	Fresh      time.Time `json:"refreshed"`
}

type Base struct {
	City    string `json:"city,omitempty"`
	Country string `json:"country,omitempty"`
	Sure    string `json:"sure,omitempty"` // stated inferred unknown
}

// Reach is the speaker's public karma: every count we could find.
type Reach struct {
	LinkedIn    int        `json:"linkedin,omitempty"`
	LinkedInSrc string     `json:"linkedin_source,omitempty"` // search (a search-engine snippet) or confirmed (typed in by the team)
	LinkedInAt  time.Time  `json:"linkedin_at,omitempty"`
	Audiences   []Audience `json:"talk_audiences,omitempty"`
	X           int        `json:"x,omitempty"`
	Bluesky     int        `json:"bluesky,omitempty"`
	Mastodon    int        `json:"mastodon,omitempty"`
	GitHub      int        `json:"github,omitempty"`
	YouTube     int        `json:"youtube,omitempty"`
}

type Audience struct {
	Event     string `json:"event"`
	Attendees int    `json:"attendees"`
}

func (r Reach) AudienceSum() int {
	n := 0
	for _, a := range r.Audiences {
		n += a.Attendees
	}
	return n
}

type Talk struct {
	Title string `json:"title"`
	Event string `json:"event,omitempty"`
	Date  string `json:"date,omitempty"`
	URL   string `json:"url,omitempty"`
}

// Parts is how the match % is made up, each 0-100.
type Parts struct {
	Reach    int `json:"reach"`
	Topic    int `json:"topic"`
	Local    int `json:"local"`
	Activity int `json:"activity"`
}

// Confirmed is a LinkedIn follower count the team typed in.
type Confirmed struct {
	Count int
	At    time.Time
}

// merge adds Claude's answer to a topic's list. A speaker already on the
// board is skipped, on this tab (the card stays as it is) and on every other
// tab (one person, one tab), as is anyone excluded. Best match first, at most
// MaxPerTopic.
func merge(t Topic, old, got []*Card, ex exclusions, elsewhere map[string]bool, conf map[string]Confirmed, now time.Time) []*Card {
	known := map[string]bool{}
	for _, c := range t.Cities {
		known[normName(c.Name)] = true
	}
	var out []*Card
	seen := map[string]bool{}
	for _, c := range old {
		if ex.hit(t.ID, c.Name, now) {
			continue
		}
		seen[c.Key] = true
		out = append(out, c)
	}
	for _, c := range got {
		c.Name = strings.TrimSpace(c.Name)
		c.Key = normName(c.Name)
		if c.Key == "" || seen[c.Key] || elsewhere[c.Key] || ex.hit(t.ID, c.Name, now) {
			continue // a repeat, or excluded
		}
		if !t.Online { // we don't pay for travel: local speakers only
			if c.Base.City == "" || c.Base.Sure == "unknown" {
				continue
			}
			var mine []string
			for _, city := range c.Cities {
				if known[normName(city)] {
					mine = append(mine, city)
				}
			}
			if len(mine) == 0 {
				continue
			}
			c.Cities = mine
		}
		if c.Match < 0 {
			c.Match = 0
		}
		if c.Match > 100 {
			c.Match = 100
		}
		c.First, c.Fresh = now, now
		if cf, ok := conf[c.Key]; ok {
			c.Reach.LinkedIn, c.Reach.LinkedInSrc, c.Reach.LinkedInAt = cf.Count, "confirmed", cf.At
		} else if c.Reach.LinkedIn > 0 {
			c.Reach.LinkedInSrc = "search"
			if c.Reach.LinkedInAt.IsZero() {
				c.Reach.LinkedInAt = now
			}
		}
		c.Links = httpsOnly(c.Links)
		if !strings.HasPrefix(c.LinkedIn, "https://") {
			c.LinkedIn = ""
		}
		if !strings.HasPrefix(c.Photo, "https://") {
			c.Photo = ""
		}
		seen[c.Key] = true
		out = append(out, c)
	}
	sortCards(out)
	if len(out) > MaxPerTopic {
		out = out[:MaxPerTopic]
	}
	return out
}

// sortCards: best match first; counted LinkedIn reach before a missing
// count when the match ties.
func sortCards(cs []*Card) {
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].Match != cs[j].Match {
			return cs[i].Match > cs[j].Match
		}
		return cs[i].Reach.LinkedIn > cs[j].Reach.LinkedIn
	})
}

func httpsOnly(list []string) []string {
	var out []string
	for _, l := range list {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "http://") {
			out = append(out, l)
		}
	}
	return out
}

// exclusions: who never shows. Anyone on one of our lineups, anyone marked
// "not a fit" on the topic, anyone invited in the last 90 days.
type exclusions struct {
	lineups  map[string]string
	rejected map[string]map[string]time.Time
	invited  map[string]time.Time
}

func (ex exclusions) hit(topic, name string, now time.Time) bool {
	for _, k := range nameKeys(name) {
		if _, ok := ex.lineups[k]; ok {
			return true
		}
		if _, ok := ex.rejected[topic][k]; ok {
			return true
		}
		if at, ok := ex.invited[k]; ok && now.Sub(at) < invitedFor {
			return true
		}
	}
	return false
}

// nameKeys: the full normalised name, and first + last name when there
// is a middle name.
func nameKeys(name string) []string {
	n := normName(name)
	if n == "" {
		return nil
	}
	f := strings.Fields(n)
	if len(f) > 2 {
		return []string{n, f[0] + " " + f[len(f)-1]}
	}
	return []string{n}
}

var nonLetters = regexp.MustCompile(`[^\p{L}\p{N}]+`)

// normName folds case, accents and punctuation: "José  O'Neil" -> "jose oneil".
func normName(s string) string {
	s = norm.NFD.String(strings.ToLower(s))
	var b strings.Builder
	for _, r := range s {
		if r >= 0x300 && r <= 0x36f {
			continue
		}
		if r == '\'' || r == '’' {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(nonLetters.ReplaceAllString(b.String(), " "))
}

// cardTalks: the conferences a card shows, as "Conference Year", one per
// event, newest first, at most four. A talk without a conference name is
// left out: the card never shows a talk title.
func cardTalks(ts []Talk) []Talk {
	cp := append([]Talk(nil), ts...)
	sort.SliceStable(cp, func(i, j int) bool { return cp[i].Date > cp[j].Date })
	var out []Talk
	seen := map[string]bool{}
	for _, t := range cp {
		t.Event = strings.TrimSpace(t.Event)
		if t.Event == "" || normName(t.Event) == normName(t.Title) {
			continue
		}
		if !hasYear.MatchString(t.Event) {
			y := yearOf(t.Date)
			if y == "" {
				continue // no year: not shown
			}
			t.Event += " " + y
		}
		k := normName(t.Event)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		if !strings.HasPrefix(t.URL, "https://") && !strings.HasPrefix(t.URL, "http://") {
			t.URL = ""
		}
		out = append(out, t)
		if len(out) == 4 {
			break
		}
	}
	return out
}

var hasYear = regexp.MustCompile(`\b(19|20)\d\d\b`)

func yearOf(date string) string {
	if len(date) >= 4 && hasYear.MatchString(date[:4]) {
		return date[:4]
	}
	return ""
}

// cardView is a card as the board draws it: with the tab's brand, for the
// mascot shown when there is no photo.
type cardView struct {
	*Card
	Brand string
}

// LinkedInURL: the profile the humans app knows, else the one a feed or
// Claude gave.
func (v cardView) LinkedInURL() string {
	if v.Humans.Profile != "" {
		return v.Humans.Profile
	}
	if strings.HasPrefix(v.LinkedIn, "https://") && strings.Contains(v.LinkedIn, "linkedin.com/") {
		return v.LinkedIn
	}
	return ""
}
