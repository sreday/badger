package snitch

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Lead quality: each person an account reached (a visit alone doesn't
// count) is graded great, ok or bad from their LinkedIn headline, by the
// rules in DATA_DIR/snitch/lead-rules.json. The rules live only on the
// server; without the file there is no lead quality on the pages. People
// without a profile read count as ok.

// LeadRule is one named group of words looked for in a headline. With
// Also, a word from each list has to be there.
type LeadRule struct {
	Label string
	Words []string
	Also  []string `json:",omitempty"`
	In    string   `json:",omitempty"` // "title" (default) or "any": title and company
}

// LeadRules: Bad is checked first, then a thin profile (fewer followers or
// connections than the minimum, when LinkedIn showed them, or no headline),
// then Great; the rest is ok.
type LeadRules struct {
	Bad, Great     []LeadRule
	MinFollowers   int
	MinConnections int
	ThinLabel      string
	re             map[*LeadRule][2]*regexp.Regexp
}

// Leads is one account's week of people reached.
type Leads struct {
	Great, OK, Bad int
	Unknown        int                 // of the ok: no LinkedIn profile read
	BadBy          map[string]int      `json:",omitempty"`
	GreatBy        map[string]int      `json:",omitempty"`
	Examples       map[string][]string `json:",omitempty"` // bad label -> a few headlines
}

func (l *Leads) Total() int     { return l.Great + l.OK + l.Bad }
func (l *Leads) BadPct() int    { return pct(l.Bad, l.Total()) }
func (l *Leads) GreatPct() int  { return pct(l.Great, l.Total()) }
func (l *Leads) Profiled() bool { return l.Total() > l.Unknown }

func pct(a, b int) int {
	if b == 0 {
		return 0
	}
	return (a*100 + b/2) / b
}

type humanProfile struct {
	title, company         string
	followers, connections int // -1: not shown
}

func readHumanProfiles(ctx context.Context, dsn string) (map[string]humanProfile, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, err
	}
	defer conn.Close(context.Background())
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (lower(trim(linked_in))) lower(trim(linked_in)), coalesce(title,''), coalesce(company,''), coalesce(followers,''), coalesce(connections,'')
		FROM profile_metadata WHERE deleted_at IS NULL AND coalesce(linked_in,'') <> '' ORDER BY lower(trim(linked_in)), observed_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]humanProfile{}
	for rows.Next() {
		var li, f, c string
		var p humanProfile
		if err := rows.Scan(&li, &p.title, &p.company, &f, &c); err != nil {
			return nil, err
		}
		p.followers, p.connections = linkedInCount(f), linkedInCount(c)
		out[li] = p
	}
	return out, rows.Err()
}

// linkedInCount reads "1,234", "500+" or "12 345"; -1 when empty.
func linkedInCount(s string) int {
	d := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
	if d == "" {
		return -1
	}
	n, _ := strconv.Atoi(d)
	return n
}

func wordsRE(ws []string) *regexp.Regexp {
	var alts []string
	for _, w := range ws {
		if w = strings.TrimSpace(strings.ToLower(w)); w != "" {
			alts = append(alts, regexp.QuoteMeta(w))
		}
	}
	if len(alts) == 0 {
		return nil
	}
	return regexp.MustCompile(`(^|[^\pL\pN])(` + strings.Join(alts, "|") + `)($|[^\pL\pN])`)
}

func (r *LeadRules) compile() {
	r.re = map[*LeadRule][2]*regexp.Regexp{}
	for _, set := range [][]LeadRule{r.Bad, r.Great} {
		for i := range set {
			r.re[&set[i]] = [2]*regexp.Regexp{wordsRE(set[i].Words), wordsRE(set[i].Also)}
		}
	}
}

func (r *LeadRules) match(set []LeadRule, p humanProfile) string {
	title := strings.ToLower(p.title)
	both := title + " | " + strings.ToLower(p.company)
	for i := range set {
		re := r.re[&set[i]]
		if re[0] == nil {
			continue
		}
		text := title
		if set[i].In == "any" {
			text = both
		}
		if re[0].MatchString(text) && (re[1] == nil || re[1].MatchString(text)) {
			return set[i].Label
		}
	}
	return ""
}

// grade sorts one profile: "great", "ok" or "bad", and the rule's label.
func (r *LeadRules) grade(p humanProfile) (string, string) {
	if l := r.match(r.Bad, p); l != "" {
		return "bad", l
	}
	if (r.MinFollowers > 0 && p.followers >= 0 && p.followers < r.MinFollowers) ||
		(r.MinConnections > 0 && p.connections >= 0 && p.connections < r.MinConnections) ||
		strings.TrimSpace(p.title) == "" {
		label := r.ThinLabel
		if label == "" {
			label = "thin profile"
		}
		return "bad", label
	}
	if l := r.match(r.Great, p); l != "" {
		return "great", l
	}
	return "ok", ""
}

const leadExamples = 3

// leadsFor counts the people each account reached per week (first reach
// of a person by that account in that week).
func leadsFor(evs []humanEvent, profiles map[string]humanProfile, rules *LeadRules, loc *time.Location) map[[2]string]*Leads {
	out := map[[2]string]*Leads{}
	if rules == nil {
		return out
	}
	seen := map[string]bool{}
	sorted := append([]humanEvent(nil), evs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].created.Before(sorted[j].created) })
	for _, e := range sorted {
		if e.li == "" || e.tag == "visit" || dateOf(e.created, loc).Before(humansFrom) {
			continue
		}
		k := [2]string{weekID(dateOf(e.created, loc)), strconv.Itoa(e.acct)}
		if id := k[0] + "|" + k[1] + "|" + e.li; seen[id] {
			continue
		} else {
			seen[id] = true
		}
		l := out[k]
		if l == nil {
			l = &Leads{BadBy: map[string]int{}, GreatBy: map[string]int{}, Examples: map[string][]string{}}
			out[k] = l
		}
		p, ok := profiles[e.li]
		if !ok {
			l.OK++
			l.Unknown++
			continue
		}
		switch g, label := rules.grade(p); g {
		case "great":
			l.Great++
			l.GreatBy[label]++
		case "ok":
			l.OK++
		default:
			l.Bad++
			l.BadBy[label]++
			if len(l.Examples[label]) < leadExamples && strings.TrimSpace(p.title) != "" {
				l.Examples[label] = append(l.Examples[label], short(p.title, 90))
			}
		}
	}
	return out
}

func short(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}

// addLeads puts each account's leads on its column and sums the week.
func addLeads(weeks []*HumansWeek, leads map[[2]string]*Leads) {
	for _, w := range weeks {
		var sum *Leads
		for _, a := range w.Accts {
			l := leads[[2]string{w.ID, strconv.Itoa(a.Acct)}]
			if l == nil {
				continue
			}
			a.Leads = l
			if sum == nil {
				sum = &Leads{BadBy: map[string]int{}, GreatBy: map[string]int{}}
			}
			sum.Great += l.Great
			sum.OK += l.OK
			sum.Bad += l.Bad
			sum.Unknown += l.Unknown
			for k, n := range l.BadBy {
				sum.BadBy[k] += n
			}
			for k, n := range l.GreatBy {
				sum.GreatBy[k] += n
			}
		}
		w.Leads = sum
	}
}

// myLeads: this week's leads of a worker's humans app account (matched by
// first name), for their own page. Nil when there is nothing to show.
func (s *Server) myLeads(name, week string) *Leads {
	h := s.humans
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, w := range h.data.Weeks {
		if w.ID != week {
			continue
		}
		for _, a := range w.Accts {
			if strings.EqualFold(a.Name, name) && a.Leads != nil && a.Leads.Total() > 0 {
				return a.Leads
			}
		}
	}
	return nil
}

// leadChip is one bad group on a page.
type leadChip struct {
	Label    string
	N        int
	Examples string
}

func (l *Leads) Chips() []leadChip {
	var out []leadChip
	for k, n := range l.BadBy {
		out = append(out, leadChip{Label: k, N: n, Examples: strings.Join(l.Examples[k], "\n")})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		return out[i].Label < out[j].Label
	})
	return out
}
