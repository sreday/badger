package snoop

import (
	"regexp"
	"strconv"
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
	{"PEC", `\b(pec|prompt engineering conf(erence)?)\b`},
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

// Pills works out a call's topic pills, most important first.
func (c *Call) Pills() []Pill {
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

	// The event brand colours the pills about it (Snout's brand accents).
	tint := ""
	switch brand {
	case "SREday", "LLMday", "PLATFORMday", "PEC":
		tint = strings.ToLower(brand)
	}
	var out []Pill
	add := func(s, b string) {
		s = strings.Join(strings.Fields(s), " ")
		if s == "" || len(out) >= 5 {
			return
		}
		for _, o := range out {
			if strings.EqualFold(o.Text, s) {
				return
			}
		}
		out = append(out, Pill{Text: s, Brand: b})
	}
	// Most important first: what they want (host / sponsor, which event),
	// the budget, then the packages talked about.
	if host {
		if where != "" {
			add("Hosting in "+where, tint)
		} else {
			add("Hosting "+brand, tint)
		}
	}
	if sponsor && !(host && !strings.Contains(low(a[qIntent]), "sponsor") && !sponsorRe.MatchString(notes)) {
		switch {
		case brand != "" || city != "":
			add("Sponsor "+strings.TrimSpace(brand+" "+city), tint)
		case where != "":
			add("Sponsor in "+where, tint)
		default:
			add("Sponsor", tint)
		}
	}
	if !host && !sponsor && brand != "" {
		add(strings.TrimSpace(brand+" "+city), tint)
	}
	if b := strings.TrimSpace(a[qBudget]); b != "" {
		add(budgetPill(b), "")
	}
	for _, p := range packages {
		if rx(p.re).MatchString(notes) || rx(p.re).MatchString(tr.String()) {
			add(p.name, "")
		}
	}
	if len(out) == 0 {
		switch {
		case c.internal():
			add("Internal", "")
		case brand != "" || city != "":
			add(strings.TrimSpace(brand+" "+city), tint)
		}
	}
	// Every call gets at least one pill: what the title says it is, then
	// its size. (Who it's with is the company pill.)
	if len(out) == 0 {
		for _, k := range kinds {
			if rx(k.re).MatchString(low(c.Title)) {
				add(k.name, "")
			}
		}
		if len(out) == 0 {
			if len(c.Attendees) > 2 {
				add("Group call", "")
			} else {
				add("1:1", "")
			}
		}
	}
	return out
}

// Pill is one topic pill; Brand (sreday, llmday, platformday, pec) tints it in
// that event's colour.
type Pill struct{ Text, Brand string }

// Company is who the call is with: the Calendly answer, else the first
// company in the attendees' email domains. Empty for internal calls.
func (c Call) Company() string { // value receiver: templates call it on stored Calls
	if co := strings.Join(strings.Fields(c.CalendlyAnswers[qCompany]), " "); co != "" {
		return co
	}
	if c.internal() {
		return ""
	}
	if cos := c.companies(); len(cos) > 0 {
		return cos[0]
	}
	return ""
}

var (
	money    = regexp.MustCompile(`\$?\s*(\d[\d.,]*)\s*(k|K)?`)
	moreThan = regexp.MustCompile(`(?i)\b(more than|over|above)\b|\+`)
	lessThan = regexp.MustCompile(`(?i)\b(less than|under|below|up to)\b`)
	noBudget = regexp.MustCompile(`(?i)\b(no budget|none|nothing|zero)\b|^\$?0$`)
)

// budgetPill turns the Calendly budget answer into raw numbers:
// "No budget" → "$0", "$5.000 - $10.000" → "$5K-$10K", "More than
// $20.000" → "$20K+", "Less than $1.000" → "<$1K".
func budgetPill(b string) string {
	if noBudget.MatchString(strings.TrimSpace(b)) {
		return "$0"
	}
	var nums []string
	for _, m := range money.FindAllStringSubmatch(b, -1) {
		digits := strings.NewReplacer(".", "", ",", "").Replace(m[1])
		n, err := strconv.Atoi(digits)
		if err != nil {
			continue
		}
		if m[2] != "" {
			n *= 1000
		}
		nums = append(nums, dollars(n))
	}
	switch {
	case len(nums) == 0:
		return b
	case len(nums) >= 2:
		return nums[0] + "-" + nums[1]
	case moreThan.MatchString(b):
		return nums[0] + "+"
	case lessThan.MatchString(b):
		return "<" + nums[0]
	}
	return nums[0]
}

// dollars: 5000 → "$5K", 1500 → "$1.5K", 1000000 → "$1M", 500 → "$500".
func dollars(n int) string {
	trim := func(f float64) string { return strings.TrimSuffix(strconv.FormatFloat(f, 'f', 1, 64), ".0") }
	switch {
	case n >= 1000000:
		return "$" + trim(float64(n)/1e6) + "M"
	case n >= 1000:
		return "$" + trim(float64(n)/1e3) + "K"
	}
	return "$" + strconv.Itoa(n)
}

// What kind of call the title says it is.
var kinds = []struct{ name, re string }{
	{"Ambassadors", `\bambassadors?\b`},
	{"Speaker", `\bspeakers?\b`},
	{"Catch-up", `\b(catch ?ups?|catching up)\b`},
	{"Onboarding", `\bonboarding\b`},
	{"Interview", `\binterview\b`},
	{"Podcast", `\bpodcast\b`},
}

// Free mail never names a company.
var freeMail = map[string]bool{"gmail.com": true, "googlemail.com": true, "hotmail.com": true, "outlook.com": true, "yahoo.com": true,
	"icloud.com": true, "me.com": true, "proton.me": true, "protonmail.com": true, "live.com": true, "aol.com": true, "fastmail.com": true}

// companies: the other side's companies, from their email domains (at
// most two, so a big invite doesn't flood the row).
func (c *Call) companies() []string {
	var out []string
	seen := map[string]bool{}
	for _, at := range c.Attendees {
		e := strings.ToLower(at.Email)
		i := strings.LastIndex(e, "@")
		if i < 0 {
			continue
		}
		d := e[i+1:]
		if freeMail[d] || strings.HasSuffix(d, "sreday.com") || strings.HasSuffix(d, "conf42.com") {
			continue
		}
		name := strings.Split(d, ".")[0]
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, strings.ToUpper(name[:1])+name[1:])
		if len(out) == 2 {
			break
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
