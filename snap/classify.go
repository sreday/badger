package snap

import (
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"
)

// Snap reads a year back, every kind of thread (Marek 2026-11-12: "ping me
// for the existing threads that have loose ends from months ago"). A loose
// end Claude flagged stays past the year until Marek calls it a dead end.
const longWindow = 365 * 24 * time.Hour

// Threads Snap reads at all: a year of the Primary tab only (Marek
// 2026-11-12). Promotions, Social, Updates and Forums never reach Snap.
// category:primary also finds archived threads that were in Primary.
const searchQuery = "newer_than:365d category:primary -in:chats -in:spam -in:trash -in:drafts"

var (
	// Only words that really mean sponsorship or hosting: "host", "venue",
	// "logo" or "invoice" alone matched Luma host copies and speaker mail and
	// put nearly everything under Sponsors.
	sponsorWords = regexp.MustCompile(`(?i)\b(sponsor\w*|prospectus|booth|exhibit(or|ors|ing)|partnership|hosting|host (an?|the|our|your|next) (event|meetup|conference|edition|sreday|llmday|platformday)|lead list|purchase order)\b`)
	speakerWords = regexp.MustCompile(`(?i)\b(talks?|speak\w*|slides|decks?|cfp|abstract|session|lineup|keynote|headshot|bio|recording)\b`)
	cancelWords  = regexp.MustCompile(`(?i)\b(cancel\w*|withdraw\w*|pull(ing)? out|can'?t make it|cannot make it|won'?t be able|unable to (attend|speak|make)|step(ping)? down|drop(ping)? out)\b`)
)

// Meta is what the rules work out from headers and snippets alone.
type Meta struct {
	People   []Person
	FirstAt  time.Time
	LastAt   time.Time
	LastFrom string
	Internal bool
	Sponsor  bool
	Skip     string
	Waiting  string // "you": someone wrote last; "them": Mark did
	Snippet  string
}

// teamDomains are the organisers' own domains.
func isTeam(addr string, team []string) bool {
	addr = strings.ToLower(addr)
	for _, d := range team {
		if strings.HasSuffix(addr, "@"+d) {
			return true
		}
	}
	return false
}

func domainOf(addr string) string {
	if i := strings.LastIndex(addr, "@"); i >= 0 {
		return strings.ToLower(addr[i+1:])
	}
	return ""
}

// Look works out a thread's Meta. sponsorDomains are the domains of
// sponsors listed on upcoming event pages.
func Look(p *ParsedThread, now time.Time, team []string, sponsorDomains map[string]bool) Meta {
	var m Meta
	if len(p.Mails) == 0 {
		m.Skip = "empty"
		return m
	}
	seen := map[string]bool{}
	add := func(a mail.Address) {
		e := strings.ToLower(a.Address)
		if e == "" || seen[e] {
			return
		}
		seen[e] = true
		m.People = append(m.People, Person{Name: a.Name, Email: e})
	}
	humans, internal := 0, true
	text := p.Subject
	for _, ml := range p.Mails {
		if !ml.FromMe {
			if ml.Automated {
				continue
			}
			humans++
			add(ml.From)
			if !isTeam(ml.From.Address, team) {
				internal = false
			}
		} else {
			for _, a := range ml.To {
				add(a)
				if !isTeam(a.Address, team) {
					internal = false
				}
			}
		}
		text += "\n" + ml.Snippet
	}
	// Drop Mark's own addresses (he's on his own Cc lines now and then).
	var people []Person
	for _, pp := range m.People {
		if !isTeam(pp.Email, team) || internal {
			people = append(people, pp)
		}
	}
	m.People = people
	m.FirstAt = p.Mails[0].At
	last := p.Mails[len(p.Mails)-1]
	// The last word that counts: Mark's, or a person's (not a robot's).
	for i := len(p.Mails) - 1; i >= 0; i-- {
		ml := p.Mails[i]
		if ml.FromMe || !ml.Automated {
			last = ml
			break
		}
	}
	m.LastAt, m.Snippet = last.At, last.Snippet
	if last.FromMe {
		m.LastFrom, m.Waiting = "me", "them"
	} else {
		m.LastFrom, m.Waiting = displayName(last.From), "you"
	}
	m.Internal = internal && humans > 0
	m.Sponsor = sponsorWords.MatchString(text)
	for _, pp := range m.People {
		if sponsorDomains[domainOf(pp.Email)] {
			m.Sponsor = true
		}
	}
	age := now.Sub(m.LastAt)
	switch {
	case humans == 0:
		// Newsletters, notifications, and Mark's own mail nobody answered
		// (cold outreach has its own nudges).
		m.Skip = "no reply from a person"
	case age > longWindow:
		m.Skip = "older than a year"
	}
	return m
}

func displayName(a mail.Address) string {
	if a.Name != "" {
		return a.Name
	}
	return a.Address
}

// Excerpt is the thread as Claude sees it: who, when, and only the new text
// of each message, capped. The first message and the last five; the middle
// is counted, not shown.
func Excerpt(p *ParsedThread, m Meta) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<thread id=%q", p.ID)
	if m.Internal {
		b.WriteString(` internal="yes"`)
	}
	if m.Sponsor {
		b.WriteString(` sponsor_hint="yes"`)
	}
	b.WriteString(">\nSubject: " + p.Subject + "\nPeople:")
	for i, pp := range m.People {
		if i == 8 {
			fmt.Fprintf(&b, " and %d more", len(m.People)-8)
			break
		}
		if pp.Name != "" {
			fmt.Fprintf(&b, " %s <%s>;", pp.Name, pp.Email)
		} else {
			fmt.Fprintf(&b, " %s;", pp.Email)
		}
	}
	b.WriteString("\n")
	show := func(ml Mail, limit int) {
		who := "Mark"
		if !ml.FromMe {
			who = displayName(ml.From)
			if ml.From.Name != "" {
				who += " <" + strings.ToLower(ml.From.Address) + ">"
			}
		}
		if ml.Automated && !ml.FromMe {
			fmt.Fprintf(&b, "[%s] (automated) %s: %s\n", ml.At.Format("2006-01-02"), who, cut(ml.Snippet, 160))
			return
		}
		text := ml.Text
		if text == "" {
			text = ml.Snippet
		}
		fmt.Fprintf(&b, "[%s] %s:\n%s\n", ml.At.Format("2006-01-02"), who, cut(text, limit))
	}
	n := len(p.Mails)
	if n <= 6 {
		for _, ml := range p.Mails {
			show(ml, 1200)
		}
	} else {
		show(p.Mails[0], 700)
		fmt.Fprintf(&b, "[... %d messages in between ...]\n", n-6)
		for _, ml := range p.Mails[n-5:] {
			show(ml, 1200)
		}
	}
	b.WriteString("</thread>\n")
	return b.String()
}

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + " [...]"
}

// Rules is Snap without Claude: a category from the words and domains, and
// a loose end wherever someone wrote last and Mark hasn't answered.
func Rules(p *ParsedThread, m Meta) Analysis {
	a := Analysis{ID: p.ID, Waiting: m.Waiting}
	text := p.Subject
	lastHuman := ""
	for _, ml := range p.Mails {
		text += "\n" + ml.Snippet
		if !ml.FromMe && !ml.Automated {
			lastHuman = ml.Text + "\n" + ml.Snippet
		}
	}
	switch {
	case m.Internal:
		a.Category = "events"
	case m.Sponsor:
		a.Category = "sponsors"
	case speakerWords.MatchString(text):
		a.Category = "speakers"
	default:
		a.Category = "others"
	}
	if a.Category == "speakers" && cancelWords.MatchString(lastHuman) {
		for i := len(p.Mails) - 1; i >= 0; i-- {
			if ml := p.Mails[i]; !ml.FromMe && !ml.Automated {
				a.CancelName = ml.From.Name
				break
			}
		}
	}
	if m.Waiting == "you" {
		a.Loose, a.Label = true, "Unanswered"
		a.Why = m.LastFrom + " wrote last and there's no reply yet."
	}
	if a.CancelName != "" {
		a.Loose, a.Label = true, "Speaker cancelled?"
	}
	a.Summary = m.Snippet
	return a
}
