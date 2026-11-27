package snitch

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// The Humans page: a table per week, a row per number and a column per
// worker (and the team), zeros shown.

// teamList: the workers' names, A to Z, once each.
func teamList(names map[int]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// The humans app's tags in the order they are worked through.
var humanTagOrder = []string{"visit", "messaged", "message", "pending", "connected", "interested", "mark interested", "maybe",
	"follow up", "next-time", "no-response", "not interested", "mark not interested", "can't come", "win", "volunteer", "interaction", "note"}

type hCell struct {
	V    string
	Zero bool
	Sub  string // "+3" changed, on the tag rows
	Team bool   // the team's column
}

type hRow struct {
	Label, Class, Group string
	Cells               []hCell // a cell per worker, then the team
}

type hWeek struct {
	ID, Label string
	Rows      []hRow
	Notes     [][]string // per worker
	Span      int        // columns in a table
	Groups    []hGroup   // the rows split into small tables: Time, Tags, Leads, Tickets
}

// hGroup is one small table of a week.
type hGroup struct {
	Name string
	Rows []hRow
}

type humansPage struct {
	Base, Version, At, Err string
	Ready                  bool
	Team                   []string
	Weeks                  []hWeek
}

var humanDayNames = [7]string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}

func hours(min int) string { return fmtHours(float64(min) / 60) }

// tagClass: "can't come" -> "t-cant-come".
func tagClass(tag string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(tag) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('-')
		}
	}
	return "t-" + b.String()
}

func humansWeekTable(w *HumansWeek, team []string) hWeek {
	by := map[string]*HumansAcct{}
	for _, a := range w.Accts {
		if by[a.Name] == nil {
			by[a.Name] = a
		}
	}
	accts := make([]*HumansAcct, len(team))
	for i, n := range team {
		accts[i] = by[n]
	}
	out := hWeek{ID: w.ID, Label: w.Label, Span: len(team) + 2}
	// row: one number per worker, the team's sum at the end
	row := func(label, class, group string, get func(a *HumansAcct) int, show func(int) string) {
		r := hRow{Label: label, Class: class, Group: group}
		sum := 0
		for _, a := range accts {
			n := 0
			if a != nil {
				n = get(a)
			}
			sum += n
			r.Cells = append(r.Cells, hCell{V: show(n), Zero: n == 0})
		}
		r.Cells = append(r.Cells, hCell{V: show(sum), Zero: sum == 0, Team: true})
		out.Rows = append(out.Rows, r)
	}
	num := strconv.Itoa

	row("Hours", "r-hours", "Time", func(a *HumansAcct) int { return a.Minutes }, hours)
	for d := 0; d < 7; d++ {
		worked := false
		for _, a := range accts {
			if a != nil && a.Days[d] > 0 {
				worked = true
			}
		}
		if d >= 5 && !worked {
			continue
		}
		row(humanDayNames[d], "r-day", "Time", func(a *HumansAcct) int { return a.Days[d] }, hours)
	}

	// tags: every tag any worker set this week, in the app's order
	used := map[string]bool{}
	for _, a := range accts {
		if a == nil {
			continue
		}
		for t := range a.Created {
			used[t] = true
		}
		for t := range a.Updated {
			used[t] = true
		}
	}
	var tags []string
	for _, t := range humanTagOrder {
		if used[t] {
			tags = append(tags, t)
			delete(used, t)
		}
	}
	var rest []string
	for t := range used {
		rest = append(rest, t)
	}
	sort.Strings(rest)
	for _, t := range append(tags, rest...) {
		r := hRow{Label: t, Class: "r-tag " + tagClass(t), Group: "Tags"}
		sum, sumUp := 0, 0
		for _, a := range accts {
			c := hCell{V: "0", Zero: true}
			if a != nil {
				n, up := a.Created[t], a.Updated[t]
				sum, sumUp = sum+n, sumUp+up
				c = hCell{V: num(n), Zero: n == 0 && up == 0}
				if up > 0 {
					c.Sub = "+" + num(up)
				}
			}
			r.Cells = append(r.Cells, c)
		}
		tc := hCell{V: num(sum), Zero: sum == 0 && sumUp == 0, Team: true}
		if sumUp > 0 {
			tc.Sub = "+" + num(sumUp)
		}
		r.Cells = append(r.Cells, tc)
		out.Rows = append(out.Rows, r)
	}

	// leads
	has := false
	weak := map[string]bool{}
	for _, a := range accts {
		if a != nil && a.Leads != nil {
			has = true
			for k := range a.Leads.WeakBy {
				weak[k] = true
			}
		}
	}
	if has {
		lead := func(f func(l *Leads) int) func(a *HumansAcct) int {
			return func(a *HumansAcct) int {
				if a.Leads == nil {
					return 0
				}
				return f(a.Leads)
			}
		}
		row("top", "r-top", "Leads", lead(func(l *Leads) int { return l.Top }), num)
		row("good", "r-good", "Leads", lead(func(l *Leads) int { return l.Good }), num)
		row("ok", "r-ok", "Leads", lead(func(l *Leads) int { return l.OK }), num)
		row("weak", "r-weak", "Leads", lead(func(l *Leads) int { return l.Weak }), num)
		var ws []string
		for k := range weak {
			ws = append(ws, k)
		}
		sort.Strings(ws)
		for _, k := range ws {
			row("· "+k, "r-weakby", "Leads", lead(func(l *Leads) int { return l.WeakBy[k] }), num)
		}
		row("no profile", "r-unknown", "Leads", lead(func(l *Leads) int { return l.Unknown }), num)
	}

	row("paid", "r-paid", "Tickets", func(a *HumansAcct) int { return a.Paid }, num)
	row("free", "r-free", "Tickets", func(a *HumansAcct) int { return a.Free }, num)
	// the week's every booking, people with a LinkedIn link: only the team column
	all := hRow{Label: "all booked (paid / free)", Class: "r-all", Group: "Tickets"}
	for range accts {
		all.Cells = append(all.Cells, hCell{V: "", Zero: true})
	}
	all.Cells = append(all.Cells, hCell{V: num(w.Paid) + " / " + num(w.Free), Zero: w.Paid+w.Free == 0, Team: true})
	out.Rows = append(out.Rows, all)

	for _, a := range accts {
		if a == nil {
			out.Notes = append(out.Notes, nil)
		} else {
			out.Notes = append(out.Notes, a.Comments)
		}
	}
	for _, r := range out.Rows {
		if len(out.Groups) == 0 || out.Groups[len(out.Groups)-1].Name != r.Group {
			out.Groups = append(out.Groups, hGroup{Name: r.Group})
		}
		g := &out.Groups[len(out.Groups)-1]
		g.Rows = append(g.Rows, r)
	}
	return out
}

func (s *Server) handleHumans(rw http.ResponseWriter, r *http.Request) {
	h := s.humans
	h.mu.Lock()
	defer h.mu.Unlock()
	v := humansPage{Base: s.cfg.Base, Version: s.cfg.Version, Err: h.data.Err, Ready: h.dsn != "", Team: h.data.Team}
	if !h.data.At.IsZero() {
		v.At = h.data.At.In(s.tz).Format("Mon 2 Jan, 15:04")
	}
	if len(v.Team) > 0 {
		for _, w := range h.data.Weeks {
			v.Weeks = append(v.Weeks, humansWeekTable(w, v.Team))
		}
	}
	s.render(rw, "humans.html", "humans.html", v)
}
