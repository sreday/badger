package snitch

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// The humans app is where the team records who they reached out to. Snitch
// reads it with a login that can only read, and shows each account's 2026
// week by week: time on the app, the tags it set and its best comments.
// There is no clock in the humans app: the hours are worked out from the
// times of the clicks (a pause of more than humansGap ends a stretch).

const (
	humansEvery = 6 * time.Hour
	humansGap   = 15 * time.Minute
	humansTail  = 2 * time.Minute // the last click of a stretch counts this much
	humansTop   = 3               // comments shown per account and week
)

// humansFrom: the first week shown (ISO week 1 of 2026).
var humansFrom, _ = mondayOf("2026-W01")

// HumansAcct is one account's week.
type HumansAcct struct {
	Acct     int
	Name     string // the worker's first name (humans-team.json)
	Minutes  int
	Days     [7]int         // minutes per day, Monday first
	Created  map[string]int // tag -> rows made this week
	Updated  map[string]int // tag -> rows changed this week (made earlier or the same week)
	Comments []Highlight    // the longest notes, at most humansTop, with who they are about
	Free     int            // tickets booked this week by people this account reached last before they booked
	Paid     int
	Leads    *Leads `json:",omitempty"` // the people it reached this week, by quality
}

// HumansWeek is one week of the team in the humans app.
type HumansWeek struct {
	ID, Label string
	Accts     []*HumansAcct
	Free      int    // every ticket booked this week (people with a LinkedIn link), free
	Paid      int    // and paid
	Leads     *Leads `json:",omitempty"` // the whole team's reach this week
}

// HumansData is what the page shows; kept in humans.json between restarts.
type HumansData struct {
	At    time.Time
	Err   string `json:",omitempty"`
	Weeks []*HumansWeek
	Team  []string `json:",omitempty"` // the workers shown, a column each
}

type humanEvent struct {
	acct             int
	created, updated time.Time
	tag, note, li    string
}

// humanReg is one ticket booked (a Luma registration in the humans app).
type humanReg struct {
	li   string
	at   time.Time
	paid bool // a price above 0
}

func readHumanRegs(ctx context.Context, dsn string) ([]humanReg, error) {
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
	rows, err := tx.Query(ctx, `SELECT lower(trim(coalesce(linked_in,''))), coalesce(registered_at, created_at), coalesce(ticket_amount, 0) > 0
		FROM attendees WHERE deleted_at IS NULL AND coalesce(registered_at, created_at) >= $1`, humansFrom.AddDate(0, 0, -1))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []humanReg
	for rows.Next() {
		var r humanReg
		if err := rows.Scan(&r.li, &r.at, &r.paid); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type humansState struct {
	mu    sync.Mutex
	dsn   string
	path  string
	team  string // humans-team.json: who is shown (only on the server)
	rules string // lead-rules.json: how leads are graded (only on the server)
	data  HumansData
}

// HumansTeam says which humans app accounts are shown and as whom. Keys:
// the start of a worker's API key -> their first name; it wins once the
// login may read the keys' prefixes. Accounts: account number -> first
// name, used until then. An account in neither is not shown at all.
type HumansTeam struct {
	Keys     map[string]string
	Accounts map[string]string
}

func humansDSN() string {
	if v := os.Getenv("SNITCH_HUMANS_DB"); v != "" {
		return v
	}
	return os.Getenv("SNORE_HUMANS_DB") // the same read-only login
}

func newHumans(dir, dsn string) *humansState {
	h := &humansState{dsn: dsn, path: filepath.Join(dir, "humans.json"), team: filepath.Join(dir, "humans-team.json"), rules: filepath.Join(dir, "lead-rules.json")}
	readJSON(h.path, &h.data)
	return h
}

// teamNames: account -> first name, for the accounts that are shown.
func teamNames(team HumansTeam, prefixes map[int]string) map[int]string {
	out := map[int]string{}
	if prefixes != nil && len(team.Keys) > 0 {
		for acct, have := range prefixes {
			have = strings.ToLower(strings.TrimSpace(have))
			for key, name := range team.Keys {
				key = strings.ToLower(strings.TrimSpace(key))
				if len(key) >= 4 && len(have) >= 4 && (strings.HasPrefix(have, key) || strings.HasPrefix(key, have)) {
					out[acct] = name
				}
			}
		}
		return out
	}
	for k, name := range team.Accounts {
		if acct, err := strconv.Atoi(k); err == nil && name != "" {
			out[acct] = name
		}
	}
	return out
}

// onlyTeam keeps the team's accounts, each with its name.
func onlyTeam(weeks []*HumansWeek, names map[int]string) []*HumansWeek {
	var out []*HumansWeek
	for _, w := range weeks {
		var keep []*HumansAcct
		for _, a := range w.Accts {
			if n := names[a.Acct]; n != "" {
				a.Name = n
				keep = append(keep, a)
			}
		}
		if w.Accts = keep; len(keep) > 0 || w.Free+w.Paid > 0 {
			out = append(out, w)
		}
	}
	return out
}

func (h *humansState) refresh(loc *time.Location) {
	if h.dsn == "" {
		return
	}
	evs, err := readHumanEvents(context.Background(), h.dsn)
	var regs []humanReg
	if err == nil {
		regs, err = readHumanRegs(context.Background(), h.dsn)
	}
	var team HumansTeam
	readJSON(h.team, &team)
	names := teamNames(team, readHumanKeys(context.Background(), h.dsn))
	people := readPeopleNames(context.Background(), h.dsn)
	var rules *LeadRules
	var profiles map[string]humanProfile
	if err == nil {
		var r LeadRules
		if readJSON(h.rules, &r) {
			r.compile()
			rules = &r
			profiles, err = readHumanProfiles(context.Background(), h.dsn)
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err != nil {
		log.Printf("Snitch: humans app not read: %v", err)
		h.data.Err = "The humans app couldn't be read just now."
		return
	}
	h.data = HumansData{At: time.Now().UTC(), Weeks: onlyTeam(humanWeeks(evs, regs, loc), names), Team: teamList(names)}
	addLeads(h.data.Weeks, leadsFor(evs, profiles, rules, loc))
	whoIs(h.data.Weeks, people)
	writeJSON(h.path, h.data)
	log.Printf("Snitch: humans app read, %d weeks, %d accounts shown", len(h.data.Weeks), len(names))
}

func readHumanEvents(ctx context.Context, dsn string) ([]humanEvent, error) {
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
	rows, err := tx.Query(ctx, `SELECT created_by_id, created_at, coalesce(updated_at, created_at), coalesce(event_type,''), coalesce(metadata,''), lower(trim(coalesce(linked_in,'')))
		FROM user_events WHERE deleted_at IS NULL AND created_by_id IS NOT NULL AND (created_at >= $1 OR updated_at >= $1)`, humansFrom.AddDate(0, -6, 0)) // earlier touches still credit 2026 bookings
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []humanEvent
	for rows.Next() {
		var e humanEvent
		var id int64
		if err := rows.Scan(&id, &e.created, &e.updated, &e.tag, &e.note, &e.li); err != nil {
			return nil, err
		}
		e.acct = int(id)
		out = append(out, e)
	}
	return out, rows.Err()
}

// humanWeeks turns the rows into weeks, newest first. Weeks are cut in loc.
func humanWeeks(evs []humanEvent, regs []humanReg, loc *time.Location) []*HumansWeek {
	type key struct {
		week string
		acct int
	}
	accts := map[key]*HumansAcct{}
	get := func(t time.Time, acct int) *HumansAcct {
		k := key{weekID(dateOf(t, loc)), acct}
		a := accts[k]
		if a == nil {
			a = &HumansAcct{Acct: acct, Created: map[string]int{}, Updated: map[string]int{}}
			accts[k] = a
		}
		return a
	}
	clicks := map[key][]time.Time{}
	notes := map[key][]Highlight{}
	for _, e := range evs {
		if !dateOf(e.created, loc).Before(humansFrom) {
			a := get(e.created, e.acct)
			a.Created[e.tag]++
			k := key{weekID(dateOf(e.created, loc)), e.acct}
			clicks[k] = append(clicks[k], e.created)
			if n := strings.TrimSpace(e.note); n != "" && !strings.HasPrefix(n, "Imported from") {
				notes[k] = append(notes[k], Highlight{Text: n, LI: e.li})
			}
		}
		if e.updated.Sub(e.created) > time.Minute && !dateOf(e.updated, loc).Before(humansFrom) {
			get(e.updated, e.acct).Updated[e.tag]++
			k := key{weekID(dateOf(e.updated, loc)), e.acct}
			clicks[k] = append(clicks[k], e.updated)
		}
	}
	// Tickets: each booking goes to the account that reached the person
	// last before they booked (a visit alone doesn't count).
	type touch struct {
		at   time.Time
		acct int
	}
	touches := map[string][]touch{}
	for _, e := range evs {
		if e.li != "" && e.tag != "visit" {
			touches[e.li] = append(touches[e.li], touch{e.created, e.acct})
		}
	}
	for _, ts := range touches {
		sort.Slice(ts, func(i, j int) bool { return ts[i].at.Before(ts[j].at) })
	}
	type tally struct{ free, paid int }
	tot := map[string]*tally{}
	for _, r := range regs {
		if dateOf(r.at, loc).Before(humansFrom) {
			continue
		}
		wk := weekID(dateOf(r.at, loc))
		if tot[wk] == nil {
			tot[wk] = &tally{}
		}
		if r.paid {
			tot[wk].paid++
		} else {
			tot[wk].free++
		}
		if r.li == "" {
			continue // no LinkedIn link: in the week's total only
		}
		ts := touches[r.li]
		i := sort.Search(len(ts), func(i int) bool { return ts[i].at.After(r.at) }) - 1
		if i < 0 {
			continue
		}
		a := get(r.at, ts[i].acct)
		if r.paid {
			a.Paid++
		} else {
			a.Free++
		}
	}
	weeks := map[string]*HumansWeek{}
	for wk, t := range tot {
		mon, _ := mondayOf(wk)
		weeks[wk] = &HumansWeek{ID: wk, Label: weekLabel(mon), Free: t.free, Paid: t.paid}
	}
	for k, a := range accts {
		a.Days, a.Minutes = humanMinutes(clicks[k], loc)
		a.Comments = topNotes(notes[k], humansTop)
		w := weeks[k.week]
		if w == nil {
			mon, _ := mondayOf(k.week)
			w = &HumansWeek{ID: k.week, Label: weekLabel(mon)}
			weeks[k.week] = w
		}
		w.Accts = append(w.Accts, a)
	}
	var out []*HumansWeek
	for _, w := range weeks {
		sort.Slice(w.Accts, func(i, j int) bool {
			if w.Accts[i].Minutes != w.Accts[j].Minutes {
				return w.Accts[i].Minutes > w.Accts[j].Minutes
			}
			return w.Accts[i].Acct < w.Accts[j].Acct
		})
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

// humanMinutes: time on the app from the click times, per day (Monday first).
func humanMinutes(ts []time.Time, loc *time.Location) (days [7]int, total int) {
	sort.Slice(ts, func(i, j int) bool { return ts[i].Before(ts[j]) })
	var day [7]time.Duration
	for i := 0; i < len(ts); {
		start, last := ts[i], ts[i]
		j := i + 1
		for j < len(ts) && ts[j].Sub(last) <= humansGap && dateOf(ts[j], loc).Equal(dateOf(start, loc)) {
			last = ts[j]
			j++
		}
		day[(int(dateOf(start, loc).Weekday())+6)%7] += last.Sub(start) + humansTail
		i = j
	}
	for d, v := range day {
		days[d] = int(v.Minutes() + .5)
		total += days[d]
	}
	return days, total
}

// Highlight is a note a worker left on a person, and who that person is.
type Highlight struct {
	Text string
	LI   string // their LinkedIn (a profile slug or a link)
	Who  string `json:",omitempty"` // their name, when the humans app has it
}

// URL: the person's LinkedIn profile.
func (h Highlight) URL() string {
	li := strings.TrimSpace(h.LI)
	switch {
	case li == "":
		return ""
	case strings.Contains(li, "linkedin.com"):
		if !strings.HasPrefix(li, "http") {
			li = "https://" + li
		}
		return li
	}
	return "https://www.linkedin.com/in/" + strings.Trim(li, "/") + "/"
}

// topNotes: the n longest different notes (the longer, the more said).
func topNotes(ns []Highlight, n int) []Highlight {
	seen := map[string]bool{}
	var u []Highlight
	for _, s := range ns {
		k := s.LI + "|" + strings.ToLower(strings.Join(strings.Fields(s.Text), " "))
		if !seen[k] {
			seen[k] = true
			u = append(u, s)
		}
	}
	sort.SliceStable(u, func(i, j int) bool { return len(u[i].Text) > len(u[j].Text) })
	if len(u) > n {
		u = u[:n]
	}
	return u
}

// readPeopleNames: LinkedIn -> name, from the profiles read and the Luma
// registrations in the humans app.
func readPeopleNames(ctx context.Context, dsn string) map[string]string {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil
	}
	defer conn.Close(context.Background())
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil
	}
	defer tx.Rollback(context.Background())
	out := map[string]string{}
	for _, q := range []string{
		`SELECT lower(trim(linked_in)), name FROM attendees WHERE deleted_at IS NULL AND coalesce(linked_in,'') <> '' AND coalesce(name,'') <> ''`,
		`SELECT lower(trim(linked_in)), name FROM profile_metadata WHERE deleted_at IS NULL AND coalesce(linked_in,'') <> '' AND coalesce(name,'') <> '' ORDER BY observed_at`,
	} {
		rows, err := tx.Query(ctx, q)
		if err != nil {
			log.Printf("Snitch: humans app names of people not read (%v)", err)
			return out
		}
		for rows.Next() {
			var li, name string
			if rows.Scan(&li, &name) == nil {
				out[li] = strings.TrimSpace(name) // profiles last: they win
			}
		}
		rows.Close()
	}
	return out
}

// whoIs puts the person's name on every highlight.
func whoIs(weeks []*HumansWeek, names map[string]string) {
	for _, w := range weeks {
		for _, a := range w.Accts {
			for i := range a.Comments {
				a.Comments[i].Who = names[strings.ToLower(strings.TrimSpace(a.Comments[i].LI))]
			}
		}
	}
}

// The page.

// readHumanKeys: the start of each account's API key (users.api_key_prefix;
// nothing else of users is asked for). Nil when the login may not read it.
func readHumanKeys(ctx context.Context, dsn string) map[int]string {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil
	}
	defer conn.Close(context.Background())
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil
	}
	defer tx.Rollback(context.Background())
	rows, err := tx.Query(ctx, `SELECT id, coalesce(api_key_prefix,'') FROM users WHERE deleted_at IS NULL`)
	if err != nil {
		log.Printf("Snitch: humans app key prefixes not readable, using the account list (%v)", err)
		return nil
	}
	defer rows.Close()
	out := map[int]string{}
	for rows.Next() {
		var id int64
		var p string
		if rows.Scan(&id, &p) == nil && p != "" {
			out[int(id)] = p
		}
	}
	if rows.Err() != nil {
		log.Printf("Snitch: humans app key prefixes not readable, using the account list (%v)", rows.Err())
		return nil
	}
	return out
}
