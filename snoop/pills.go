package snoop

import (
	"regexp"
	"strings"
)

// Topic pills: 1-5 pale, non-clickable labels that say what a call was
// about at a glance ("Hosting in Toronto", "Sponsor SREday NYC", "Booth",
// "No budget"). Plain rules, no AI: the Calendly answers first, then the
// prospectus vocabulary, brand and city names in the title, Gemini's notes
// and the transcript.

// Calendly booking questions, as they appear in the invite description.
const (
	qCompany = "your company"
	qDiscuss = "we meet to discuss"
	qArea    = "area of interest"
	qIntent  = "looking to sponsor, or host an event?"
	qGoals   = "your main goals?"
	qBudget  = "your budget range?"
)

// ParseCalendlyAnswers reads "Question: answer" lines out of a Calendly
// invite. Answers that run over several lines (multiple choice) are joined
// with ", ".
func ParseCalendlyAnswers(desc string) map[string]string {
	out := map[string]string{}
	key := ""
	for _, l := range strings.Split(strings.ReplaceAll(desc, "\r", ""), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			key = ""
			continue
		}
		if i := strings.Index(l, ":"); i > 0 {
			k := strings.ToLower(strings.TrimSpace(l[:i]))
			switch k {
			case qCompany, qDiscuss, qArea, qIntent, qGoals, qBudget:
				key = k
				if v := strings.TrimSpace(l[i+1:]); v != "" {
					out[k] = v
				}
				continue
			}
		}
		if key != "" { // a further line of a multi-line answer
			if out[key] != "" {
				out[key] += ", "
			}
			out[key] += l
		}
	}
	return out
}

var brands = []struct{ name, re string }{
	{"SREday", `\bsre ?day\b`},
	{"LLMday", `\bllm ?day\b`},
	{"PLATFORMday", `\bplatform ?day\b`},
	{"Conf42", `\bconf ?42\b`},
	{"DevOpsNotDead", `\bdevops ?not ?dead\b`},
}

// Cities we run (or are asked to run) events in, with spoken variants.
var cities = []struct{ name, re string }{
	{"NYC", `\b(nyc|new york)\b`}, {"SF", `\b(san francisco|sf|bay area)\b`}, {"London", `\blondon\b`},
	{"Amsterdam", `\bamsterdam\b`}, {"Austin", `\baustin\b`}, {"Bangalore", `\b(bangalore|bengaluru)\b`},
	{"Barcelona", `\bbarcelona\b`}, {"Campinas", `\bcampinas\b`}, {"Chennai", `\bchennai\b`}, {"Cologne", `\b(cologne|köln|koln)\b`},
	{"Dallas", `\bdallas\b`}, {"Hamburg", `\bhamburg\b`}, {"Hyderabad", `\bhyderabad\b`}, {"Katowice", `\bkatowice\b`},
	{"Lisbon", `\blisbon\b`}, {"Montevideo", `\bmontevideo\b`}, {"Munich", `\bmunich\b`}, {"Paris", `\bparis\b`},
	{"Redmond", `\bredmond\b`}, {"Seattle", `\bseattle\b`}, {"Tel Aviv", `\btel ?aviv\b`}, {"Warsaw", `\bwarsaw\b`},
	{"Toronto", `\btoronto\b`}, {"Chicago", `\bchicago\b`}, {"Boston", `\bboston\b`}, {"Berlin", `\bberlin\b`},
	{"Oakland", `\boakland\b`}, {"Denver", `\bdenver\b`}, {"Dublin", `\bdublin\b`}, {"Singapore", `\bsingapore\b`},
	{"Salt Lake City", `\bsalt lake\b`}, {"Noida", `\bnoida\b`}, {"Gurugram", `\b(gurugram|gurgaon)\b`}, {"Kraków", `\b(krakow|kraków)\b`},
}

// The prospectus' packages, in its order.
var packages = []struct{ name, re string }{
	{"Booth", `\bbooths?\b`},
	{"Lead list", `\b(lead list|leads list|attendees? list)\b`},
	{"Keynote", `\bkeynotes?\b`},
	{"Talk slot", `\b(talk slot|speaking slot|regular talk)\b`},
	{"Logo + swag", `\b(swag|logo sponsor)\b`},
	{"Food & drinks", `\b(happy hour|coffee sponsor|lunch sponsor|speakers?'? dinner)\b`},
	{"T-shirts", `\bt-?shirts?\b`},
	{"Ticket pack", `\b(ticket pack|discounted tickets?)\b`},
	{"Meetup", `\bmeetups?\b`},
	{"Hackathon", `\bhackathons?\b`},
	{"Workshop", `\bworkshops?\b`},
}

var (
	hostRe    = regexp.MustCompile(`\b(host|hosting|hosted|venue)\b`)
	sponsorRe = regexp.MustCompile(`\bsponsor(s|ing|ship)?\b`)
	reCache   = map[string]*regexp.Regexp{}
)

func rx(s string) *regexp.Regexp {
	if r, ok := reCache[s]; ok {
		return r
	}
	r := regexp.MustCompile(s)
	reCache[s] = r
	return r
}

// firstMatch is the name mentioned earliest in the first text that
// mentions any of them.
func firstMatch(list []struct{ name, re string }, texts ...string) string {
	for _, t := range texts {
		best, at := "", -1
		for _, x := range list {
			if loc := rx(x.re).FindStringIndex(t); loc != nil && (at < 0 || loc[0] < at) {
				best, at = x.name, loc[0]
			}
		}
		if best != "" {
			return best
		}
	}
	return ""
}

// Pills works out a call's topic pills.
func (c *Call) Pills() []string {
	a := c.CalendlyAnswers
	low := strings.ToLower
	// What was said, most reliable first: the booking, the title, the notes,
	// then the transcript.
	booking := low(a[qDiscuss] + " " + a[qIntent] + " " + a[qArea])
	notes := ""
	if n := c.Notes(); n != nil {
		notes = low(n.Summary + " " + strings.Join(n.NextSteps, " "))
	}
	var tr strings.Builder
	for _, l := range c.Lines() {
		tr.WriteString(low(l.Text))
		tr.WriteByte(' ')
	}
	talk := low(c.Title) + " " + notes + " " + tr.String()

	brand := firstMatch(brands, booking, talk)
	city := firstMatch(cities, low(c.Title), notes, booking, tr.String())
	host := strings.Contains(low(a[qIntent]), "host") || hostRe.MatchString(notes)
	sponsor := strings.Contains(low(a[qIntent]), "sponsor") || sponsorRe.MatchString(low(c.CalendlyEvent)+" "+notes)
	where := city
	if where == "" && a[qArea] != "" {
		where = a[qArea]
	}

	var out []string
	add := func(s string) {
		s = strings.Join(strings.Fields(s), " ")
		if s == "" || len(out) >= 5 {
			return
		}
		for _, o := range out {
			if strings.EqualFold(o, s) {
				return
			}
		}
		out = append(out, s)
	}
	if host {
		if where != "" {
			add("Hosting in " + where)
		} else {
			add("Hosting " + brand)
		}
	}
	if sponsor && !(host && !strings.Contains(low(a[qIntent]), "sponsor") && !sponsorRe.MatchString(notes)) {
		add("Sponsor " + strings.TrimSpace(brand+" "+city))
	}
	if !host && !sponsor && brand != "" {
		add(strings.TrimSpace(brand + " " + city))
	}
	for _, p := range packages {
		if rx(p.re).MatchString(notes) || rx(p.re).MatchString(tr.String()) {
			add(p.name)
		}
	}
	if b := strings.TrimSpace(a[qBudget]); b != "" {
		if strings.EqualFold(b, "no budget") {
			add("No budget")
		} else {
			add("Budget " + b)
		}
	}
	add(a[qCompany])
	if len(out) == 0 {
		if c.internal() {
			add("Internal")
		} else if brand != "" || city != "" {
			add(strings.TrimSpace(brand + " " + city))
		}
	}
	return out
}

// internal: everyone on the invite is one of us.
func (c *Call) internal() bool {
	if len(c.Attendees) == 0 {
		return false
	}
	for _, at := range c.Attendees {
		e := strings.ToLower(at.Email)
		if !strings.HasSuffix(e, "@sreday.com") && !strings.HasSuffix(e, "@conf42.com") {
			return false
		}
	}
	return true
}
