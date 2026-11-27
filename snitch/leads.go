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
// count) is sorted by their LinkedIn headline into top, good, ok or weak,
// by the rules in DATA_DIR/snitch/lead-rules.json. The rules live only on
// the server; without the file there is no lead quality on the pages.

// LeadRule is one named group of words looked for in a headline.
type LeadRule struct {
	Label string
	Words []string
	In    string `json:",omitempty"` // "title" (default) or "any": title and company
}

// LeadRules: Weak is checked first, then Top, then Good; the rest is ok.
// A profile with fewer followers or connections than the minimum (when
// LinkedIn showed the number) counts as weak under ThinLabel.
type LeadRules struct {
	Weak, Top, Good []LeadRule
	MinFollowers    int
	MinConnections  int
	ThinLabel       string
	re              map[*LeadRule]*regexp.Regexp
}

// Leads is one account's week of people reached.
type Leads struct {
	Top, Good, OK, Weak int
	Unknown             int                 // no LinkedIn profile read for them
	WeakBy              map[string]int      `json:",omitempty"`
	Examples            map[string][]string `json:",omitempty"` // weak label -> a few headlines
}

func (l *Leads) Total() int   { return l.Top + l.Good + l.OK + l.Weak }
func (l *Leads) WeakPct() int { return pct(l.Weak, l.Total()) }
func (l *Leads) TopPct() int  { return pct(l.Top+l.Good, l.Total()) }

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

func (r *LeadRules) compile() {
	r.re = map[*LeadRule]*regexp.Regexp{}
	for _, set := range [][]LeadRule{r.Weak, r.Top, r.Good} {
		for i := range set {
			var alts []string
			for _, w := range set[i].Words {
				if w = strings.TrimSpace(strings.ToLower(w)); w != "" {
					alts = append(alts, regexp.QuoteMeta(w))
				}
			}
			if len(alts) > 0 {
				r.re[&set[i]] = regexp.MustCompile(`(^|[^\pL\pN])(` + strings.Join(alts, "|") + `)($|[^\pL\pN])`)
			}
		}
	}
}

func (r *LeadRules) match(set []LeadRule, p humanProfile) string {
	title := strings.ToLower(p.title)
	both := title + " | " + strings.ToLower(p.company)
	for i := range set {
		re := r.re[&set[i]]
		if re == nil {
			continue
		}
		text := title
		if set[i].In == "any" {
			text = both
		}
		if re.MatchString(text) {
			return set[i].Label
		}
	}
	return ""
}

// grade sorts one profile: "top", "good", "ok" or "weak" (+ the weak label).
func (r *LeadRules) grade(p humanProfile) (string, string) {
	if l := r.match(r.Weak, p); l != "" {
		return "weak", l
	}
	if (r.MinFollowers > 0 && p.followers >= 0 && p.followers < r.MinFollowers) ||
		(r.MinConnections > 0 && p.connections >= 0 && p.connections < r.MinConnections) ||
		strings.TrimSpace(p.title) == "" {
		label := r.ThinLabel
		if label == "" {
			label = "thin profile"
		}
		return "weak", label
	}
	if r.match(r.Top, p) != "" {
		return "top", ""
	}
	if r.match(r.Good, p) != "" {
		return "good", ""
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
		wk := weekID(dateOf(e.created, loc))
		k := [2]string{wk, strconv.Itoa(e.acct)}
		if seen[k[0]+"|"+k[1]+"|"+e.li] {
			continue
		}
		seen[k[0]+"|"+k[1]+"|"+e.li] = true
		l := out[k]
		if l == nil {
			l = &Leads{WeakBy: map[string]int{}, Examples: map[string][]string{}}
			out[k] = l
		}
		p, ok := profiles[e.li]
		if !ok {
			l.Unknown++
			continue
		}
		g, label := rules.grade(p)
		switch g {
		case "top":
			l.Top++
		case "good":
			l.Good++
		case "ok":
			l.OK++
		default:
			l.Weak++
			l.WeakBy[label]++
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

// leadChip is one weak group on a page.
type leadChip struct {
	Label    string
	N        int
	Examples string
}

func (l *Leads) Chips() []leadChip {
	var out []leadChip
	for k, n := range l.WeakBy {
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

// addLeads puts each account's leads on its card and sums the week.
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
				sum = &Leads{WeakBy: map[string]int{}}
			}
			sum.Top += l.Top
			sum.Good += l.Good
			sum.OK += l.OK
			sum.Weak += l.Weak
			sum.Unknown += l.Unknown
			for k, n := range l.WeakBy {
				sum.WeakBy[k] += n
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
