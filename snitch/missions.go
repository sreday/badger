package snitch

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Mission is one kind of work from the catalogue. From/To are days around
// the event (negative = before the first day; positive = after the last).
type Mission struct {
	ID      string
	Name    string
	Set     string // inperson | conf42 | generic | custom
	Tab     string // custom missions: the tab they were added to (generic or a brand)
	Brand   string // generic missions tied to a brand's page (colour only)
	From    int
	To      int
	Monday  bool   // "This week" post: the Monday of the event's week
	Repeat  string // "" = once per event, daily, weekly
	Minutes int
	How     string
	Links   []string
}

const (
	howGeneral  = "Friendly, energetic, community tone, not corporate. Open with a short question about real incident or on-call life, invite readers to see themselves, list the roles inline with emojis, then the event name and city, date, venue, focus areas, a short call to action and the registration link. Add a video or a catchy meme. Tag the speakers."
	howCarousel = "Use the Canva template with the speakers' headshots. Tag ALL speakers and their companies. Export as PDF from Canva and upload it as a document on LinkedIn. Be creative, don't sound like ChatGPT."
	howSpeaker  = "One post + headshot per speaker, 5-7 posts a day up to the day of the conference. One catchy line (a question, a blunt observation or a short quote, different every time), the talk title written plainly, what the speaker will share, date and time, the link, a short bio line. Tag the speaker and the company."
	howFollow   = "Invite people to follow the page, from the company account and from each private LinkedIn account."
)

// catalogue: the in-person set comes from the team's social media deck,
// the Conf42 set from conf42_crm_actions.csv, the rest from the event
// playbook. Only work a remote assistant does.
var catalogue = []Mission{
	// In-person events: SREday, LLMday, PLATFORMday, PEC.
	{ID: "ip-outreach", Name: "Speaker outreach for gaps", Set: "inperson", From: -70, To: -28, Repeat: "daily", Links: []string{"cfp", "fasttrack"},
		How: "Find and invite speakers for the open slots. Invited speakers submit through the fast track page; everyone else through the CFP."},
	{ID: "ip-li-event", Name: "Create the LinkedIn event, publish as a post", Set: "inperson", From: -60, To: -30, Links: []string{"event", "mediakit"},
		How: "Create the event on the conference's LinkedIn page, then publish it as a post."},
	{ID: "ip-general", Name: "General post announcing the conference", Set: "inperson", From: -35, To: -25, Links: []string{"event", "mediakit"}, How: howGeneral},
	{ID: "ip-carousel", Name: "Carousel tagging speakers", Set: "inperson", From: -35, To: -25, Links: []string{"carousel", "event"}, How: howCarousel},
	{ID: "ip-onboard", Name: "Onboard speakers", Set: "inperson", From: -28, To: -14, Links: []string{"onboarding", "event"},
		How: "Send every confirmed speaker the onboarding email and chase the ones who haven't answered."},
	{ID: "ip-teasers", Name: "Share teasers with speakers", Set: "inperson", From: -21, To: -14, Links: []string{"teasers"},
		How: "Send each speaker their teaser card and ask them to post it."},
	{ID: "ip-speaker-posts", Name: "Separate post for each speaker", Set: "inperson", From: -28, To: 0, Repeat: "daily", Links: []string{"thumbs", "event"}, How: howSpeaker},
	{ID: "ip-luma", Name: "Manage Luma guests", Set: "inperson", From: -30, To: 0, Repeat: "daily", Links: []string{"luma"},
		How: "Approve or decline pending guests and answer their questions."},
	{ID: "ip-heroes", Name: "Chase sleeping Community Heroes", Set: "inperson", From: -28, To: 0, Repeat: "weekly", Links: []string{"status", "communityhero"},
		How: "On the status page's Heroes tab: anyone with no form a day after their Luma email needs a nudge."},
	{ID: "ip-luma-blast", Name: "Luma blast + invite past guests", Set: "inperson", From: -9, To: -5, Links: []string{"luma"},
		How: "Blast the invitees who haven't accepted, and invite the past guests of the same city."},
	{ID: "ip-next-week", Name: "\"Next week\" post", Set: "inperson", From: -9, To: -5, Links: []string{"event"}, How: "A short LinkedIn post: the conference is next week."},
	{ID: "ip-this-week", Name: "\"This week\" post", Set: "inperson", Monday: true, Links: []string{"event"}, How: "A short LinkedIn post on the Monday of the conference week."},
	{ID: "ip-tomorrow", Name: "\"Tomorrow\" post", Set: "inperson", From: -1, To: -1, Links: []string{"event"}, How: "A short LinkedIn post the day before the conference."},
	{ID: "ip-approve-heroes", Name: "Approve Community Heroes on Luma", Set: "inperson", From: 1, To: 3, Links: []string{"luma", "status"}},
	{ID: "ip-decks", Name: "Chase missing slide decks", Set: "inperson", From: 1, To: 7, Links: []string{"deckdump", "event"},
		How: "Ask the speakers whose slides are still missing to upload them on the deck dump page."},

	// Conf42 (online): its own set, never mixed with the in-person one.
	{ID: "c42-outreach", Name: "Outreach to additional speakers for gaps", Set: "conf42", From: -45, To: -35, Repeat: "daily", Links: []string{"cfp"}},
	{ID: "c42-review", Name: "Export and review submissions from cfp.ninja", Set: "conf42", From: -33, To: -27, Links: []string{"cfp"}},
	{ID: "c42-publish", Name: "Publish conference", Set: "conf42", From: -27, To: -23, Links: []string{"event"}},
	{ID: "c42-onboard", Name: "Onboard selected speakers", Set: "conf42", From: -27, To: -21, Links: []string{"event", "record"}},
	{ID: "c42-teasers", Name: "Share teasers with speakers", Set: "conf42", From: -23, To: -19, Links: []string{"teasers"}},
	{ID: "c42-posts", Name: "Publish speaker announcements and conference posts", Set: "conf42", From: -21, To: -10, Repeat: "daily", Links: []string{"thumbs", "carousel", "event"}, How: howSpeaker},
	{ID: "c42-factory", Name: "Activate the factory, review uploaded videos", Set: "conf42", From: -7, To: -3, Repeat: "daily", Links: []string{"factory", "record"}},
	{ID: "c42-certs", Name: "Issue speaker certificates, handle corrections", Set: "conf42", From: 5, To: 7, Links: []string{"event"}},

	// Generic: no event.
	{ID: "g-follow-sreday", Name: "Invite people to follow SREday (limit 250)", Set: "generic", Brand: "sreday", Repeat: "weekly", How: howFollow},
	{ID: "g-follow-conf42", Name: "Invite people to follow Conf42 (limit 250)", Set: "generic", Brand: "conf42", Repeat: "weekly", How: howFollow},
	{ID: "g-follow-llmday", Name: "Invite people to follow LLMday (limit 100)", Set: "generic", Brand: "llmday", Repeat: "weekly", How: howFollow},
	{ID: "g-follow-platformday", Name: "Invite people to follow PLATFORMday (limit 100)", Set: "generic", Brand: "platformday", Repeat: "weekly", How: howFollow},
	{ID: "g-outreach", Name: "Speaker outreach", Set: "generic", Repeat: "daily", Minutes: 120},
	{ID: "g-newsletter", Name: "Newsletter", Set: "generic", Repeat: "weekly", Minutes: 90},
	{ID: "g-meetup", Name: "Meetup blast", Set: "generic", Repeat: "weekly", Minutes: 30},
	{ID: "g-community", Name: "Community post (WhatsApp / Discord)", Set: "generic", Repeat: "weekly", Minutes: 30},
	{ID: "g-admin", Name: "Admin / other", Set: "generic", Repeat: "daily", Minutes: 60},
}

var tabs = []struct{ Key, Name string }{
	{"generic", "Generic"}, {"sreday", "SREday"}, {"llmday", "LLMday"}, {"platformday", "PLATFORMday"}, {"conf42", "Conf42"},
}

func validTab(k string) bool {
	for _, t := range tabs {
		if t.Key == k {
			return true
		}
	}
	return false
}

// startMinutes: every mission lands on the board as half an hour; Mark
// sizes it from there (Marek: "all missions start with 30 minutes").
const startMinutes = 30

// missions is the catalogue plus the ones added by hand.
func (s *Server) missions() []Mission {
	out := make([]Mission, 0, len(catalogue)+len(s.st.custom))
	out = append(out, catalogue...)
	out = append(out, s.st.custom...)
	for i := range out {
		out[i].Minutes = startMinutes
	}
	return out
}

func (s *Server) mission(id string) (Mission, bool) {
	for _, m := range s.missions() {
		if m.ID == id {
			return m, true
		}
	}
	return Mission{}, false
}

// fits: the mission is offered for events of this brand.
func (m Mission) fits(brand string) bool {
	switch m.Set {
	case "inperson":
		return brand != "conf42"
	case "conf42":
		return brand == "conf42"
	case "custom":
		return m.Tab == brand
	}
	return false
}

func (m Mission) generic() bool { return m.Set == "generic" || m.Set == "custom" && m.Tab == "generic" }

// window is the mission's days for an event. A custom mission is open for
// as long as the event is listed.
func (m Mission) window(ev Event) (from, to time.Time) {
	day, end := parseDay(ev.Day), parseDay(ev.EndDay)
	switch {
	case m.Set == "custom":
		return day.AddDate(0, 0, -400), end
	case m.Monday:
		mon := day.AddDate(0, 0, -int((day.Weekday()+6)%7))
		return mon, mon
	case m.From > 0:
		return end.AddDate(0, 0, m.From), end.AddDate(0, 0, m.To)
	}
	return day.AddDate(0, 0, m.From), day.AddDate(0, 0, m.To)
}

// status says whether the mission is offered for the event in the week
// starting mon: "due" (its window touches the week), "late" (window gone,
// event still ahead), "later" (window not open yet) or "" (not offered:
// the event is over, or the after-event window has closed).
func (m Mission) status(ev Event, mon, today time.Time) string {
	from, to := m.window(ev)
	sun := mon.AddDate(0, 0, 6)
	after := m.From > 0
	day, end := parseDay(ev.Day), parseDay(ev.EndDay)
	if !after && (day.Before(mon) || end.Before(today)) {
		return "" // the event has started: nothing left to prepare
	}
	if after && to.Before(today) {
		return ""
	}
	switch {
	case to.Before(mon):
		if after {
			return ""
		}
		return "late"
	case from.After(sun):
		return "later"
	}
	return "due"
}

// links builds the mission's chips for an event from the pages the
// websites build for it.
func (m Mission) links(ev Event) []Link {
	var out []Link
	add := func(label, u string) {
		if u != "" {
			out = append(out, Link{label, u})
		}
	}
	c42 := ev.Brand == "conf42"
	teasers := ev.URL + "teasers/"
	if c42 {
		teasers = ev.URL + "/teasers"
	}
	for _, k := range m.Links {
		switch k {
		case "event":
			add("Event page", ev.URL)
		case "teasers":
			add("Teasers", teasers)
		case "carousel":
			add("Carousel", teasers+"#carousel")
		case "thumbs":
			add("Speaker cards", teasers+"#thumbnails")
		case "cfp":
			add("CFP", ev.CFP)
		case "luma":
			if c42 {
				add("Luma", ev.Luma)
			} else if ev.Luma != "" {
				add("Luma", "https://lu.ma/event/manage/"+ev.Luma)
			}
		case "record":
			add("Record page", ev.Base+"record")
		case "factory":
			add("Factory", ev.URL+"/factory")
		}
		if c42 {
			continue
		}
		switch k {
		case "fasttrack":
			add("Fast track", ev.URL+"fasttrack/")
		case "onboarding":
			add("Onboarding", ev.URL+"onboarding/")
		case "communityhero":
			add("Community Hero", ev.URL+"communityhero/")
		case "status":
			add("Status page", ev.Base+"status/")
		case "deckdump":
			add("Deck dump", ev.Base+"deckdump/")
		case "mediakit":
			add("Media kit", ev.Base+"mediakit/")
		}
	}
	return out
}

// --- The shelf --------------------------------------------------------------

type ShelfItem struct {
	Mission string
	Event   string
	Name    string
	Brand   string
	Minutes string
	Status  string // due late later done ""
	Label   string
}

type ShelfEvent struct {
	ID    string
	Name  string
	Short string
	When  string
	On    bool
	Items []ShelfItem
	due   int
}

type ShelfTab struct {
	Key    string
	Name   string
	N      int // missions due or late for the picked event
	Events []ShelfEvent
	Items  []ShelfItem // the generic tab
}

// doneBy finds who finished a once-per-event mission, in any week.
func (s *Server) doneBy(mission, event string) (who string, at time.Time) {
	for _, w := range s.st.weeks {
		for _, t := range w.Tasks {
			if t.Done && t.Mission == mission && t.Event == event && t.DoneAt.After(at) {
				p, _ := s.st.person(w.Person)
				who, at = p.Name, t.DoneAt
			}
		}
	}
	return
}

// shelf builds the tabs for the week starting mon.
func (s *Server) shelf(mon, now time.Time) []ShelfTab {
	today := dateOf(now, s.tz)
	all := s.missions()
	events := s.events.Get(now)
	var out []ShelfTab
	for _, tb := range tabs {
		tab := ShelfTab{Key: tb.Key, Name: tb.Name}
		if tb.Key == "generic" {
			for _, m := range all {
				if m.generic() {
					brand := m.Brand
					if brand == "" {
						brand = "generic"
					}
					tab.Items = append(tab.Items, ShelfItem{Mission: m.ID, Name: m.Name, Brand: brand, Minutes: fmtMin(m.Minutes)})
				}
			}
			out = append(out, tab)
			continue
		}
		picked := false
		for _, ev := range events {
			if ev.Brand != tb.Key {
				continue
			}
			se := ShelfEvent{ID: ev.ID, Name: ev.Name, Short: shortEvent(ev.Brand, ev.Name), When: whenLabel(ev, today)}
			for _, m := range all {
				if !m.fits(ev.Brand) {
					continue
				}
				st := m.status(ev, mon, today)
				if st == "" {
					continue
				}
				it := ShelfItem{Mission: m.ID, Event: ev.ID, Name: m.Name, Brand: ev.Brand, Minutes: fmtMin(m.Minutes), Status: st}
				from, to := m.window(ev)
				switch st {
				case "late":
					it.Label = "late, was due " + to.Format("2 Jan")
				case "later":
					it.Label = "from " + from.Format("2 Jan")
				}
				if m.Repeat == "" && m.Set != "custom" {
					if who, at := s.doneBy(m.ID, ev.ID); who != "" {
						it.Status, it.Label = "done", "done by "+who+", "+at.In(s.tz).Format("2 Jan")
					}
				}
				if it.Status == "due" || it.Status == "late" {
					se.due++
				}
				se.Items = append(se.Items, it)
			}
			if len(se.Items) == 0 {
				continue
			}
			order := map[string]int{"late": 0, "due": 1, "later": 2, "done": 3}
			sort.SliceStable(se.Items, func(i, j int) bool { return order[se.Items[i].Status] < order[se.Items[j].Status] })
			// The closest event still ahead of this week is picked.
			if !picked && !parseDay(ev.EndDay).Before(mon) {
				se.On, picked = true, true
				tab.N = se.due
			}
			tab.Events = append(tab.Events, se)
		}
		if !picked && len(tab.Events) > 0 {
			tab.Events[len(tab.Events)-1].On = true
			tab.N = tab.Events[len(tab.Events)-1].due
		}
		out = append(out, tab)
	}
	return out
}

func whenLabel(ev Event, today time.Time) string {
	day, end := parseDay(ev.Day), parseDay(ev.EndDay)
	n := int(day.Sub(today).Hours() / 24)
	switch {
	case end.Before(today):
		return "done " + day.Format("2 Jan")
	case !day.After(today):
		return "today"
	case n == 1:
		return "tomorrow"
	}
	_ = n
	return day.Format("2 Jan 2006")
}

// --- Flags -----------------------------------------------------------------

var dayNames = [5]string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday"}

// flags are the only hints left on the board: a small mark on a pill when
// someone else already has that mission this week, or it was done before.
func (s *Server) flags(w *Week, mon time.Time) map[string]string {
	out := map[string]string{}
	for _, t := range w.Tasks {
		var says []string
		for _, p := range s.st.people {
			if p.ID == w.Person {
				continue
			}
			for _, o := range s.st.week(p.ID, w.ID).Tasks {
				if o.Mission == t.Mission && o.Event == t.Event {
					says = append(says, p.Name+" already has this mission this week")
					break
				}
			}
		}
		if m, ok := s.mission(t.Mission); ok && m.Repeat == "" && t.Event != "" && !t.Done {
			if who, at := s.doneBy(t.Mission, t.Event); who != "" {
				says = append(says, "Already done by "+who+" "+agoWeeks(at, mon))
			}
		}
		if len(says) > 0 {
			out[t.ID] = strings.Join(says, ". ")
		}
	}
	return out
}

func agoWeeks(at, mon time.Time) string {
	n := int(mon.Sub(dateOf(at, time.UTC)).Hours()/24+6) / 7
	switch {
	case n <= 0:
		return "this week"
	case n == 1:
		return "last week"
	}
	return fmt.Sprintf("%d weeks ago", n)
}

// shortEvent drops the brand from an event's name: the colour says it.
func shortEvent(brand, name string) string {
	return strings.TrimPrefix(name, brandNames[brand]+" ")
}
