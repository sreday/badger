package snitch

import (
	"context"
	"log"
	"net/http"
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
	Minutes  int
	Days     [7]int         // minutes per day, Monday first
	Created  map[string]int // tag -> rows made this week
	Updated  map[string]int // tag -> rows changed this week (made earlier or the same week)
	Comments []string       // the longest ones, at most humansTop
	Free     int            // tickets booked this week by people this account reached last before they booked
	Paid     int
}

// HumansWeek is one week of the team in the humans app.
type HumansWeek struct {
	ID, Label string
	Accts     []*HumansAcct
	Free      int // every ticket booked this week (people with a LinkedIn link), free
	Paid      int // and paid
}

// HumansData is what the page shows; kept in humans.json between restarts.
type HumansData struct {
	At    time.Time
	Err   string `json:",omitempty"`
	Weeks []*HumansWeek
	Names map[int]string `json:",omitempty"` // account -> first name, from the humans app's users (when the login may read them)
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
	rows, err := tx.Query(ctx, `SELECT lower(trim(linked_in)), coalesce(registered_at, created_at), coalesce(ticket_amount, 0) > 0
		FROM attendees WHERE deleted_at IS NULL AND coalesce(linked_in,'') <> '' AND coalesce(registered_at, created_at) >= $1`, humansFrom.AddDate(0, 0, -1))
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
	names string // humans-names.json: account -> person id
	data  HumansData
	who   map[string]string
}

func humansDSN() string {
	if v := os.Getenv("SNITCH_HUMANS_DB"); v != "" {
		return v
	}
	return os.Getenv("SNORE_HUMANS_DB") // the same read-only login
}

func newHumans(dir, dsn string) *humansState {
	h := &humansState{dsn: dsn, path: filepath.Join(dir, "humans.json"), names: filepath.Join(dir, "humans-names.json"), who: map[string]string{}}
	readJSON(h.path, &h.data)
	readJSON(h.names, &h.who)
	return h
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
	names := readHumanNames(context.Background(), h.dsn)
	h.mu.Lock()
	defer h.mu.Unlock()
	if err != nil {
		log.Printf("Snitch: humans app not read: %v", err)
		h.data.Err = "The humans app couldn't be read just now."
		return
	}
	h.data = HumansData{At: time.Now().UTC(), Weeks: humanWeeks(evs, regs, loc), Names: names}
	writeJSON(h.path, h.data)
	log.Printf("Snitch: humans app read, %d weeks", len(h.data.Weeks))
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
	notes := map[key][]string{}
	for _, e := range evs {
		if !dateOf(e.created, loc).Before(humansFrom) {
			a := get(e.created, e.acct)
			a.Created[e.tag]++
			k := key{weekID(dateOf(e.created, loc)), e.acct}
			clicks[k] = append(clicks[k], e.created)
			if n := strings.TrimSpace(e.note); n != "" && !strings.HasPrefix(n, "Imported from") {
				notes[k] = append(notes[k], n)
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

// topNotes: the n longest different comments (the longer, the more said).
func topNotes(ns []string, n int) []string {
	seen := map[string]bool{}
	var u []string
	for _, s := range ns {
		k := strings.ToLower(strings.Join(strings.Fields(s), " "))
		if !seen[k] {
			seen[k] = true
			u = append(u, s)
		}
	}
	sort.SliceStable(u, func(i, j int) bool { return len(u[i]) > len(u[j]) })
	if len(u) > n {
		u = u[:n]
	}
	return u
}

// The page.

type humansAcctView struct {
	*HumansAcct
	Who     string // the person id picked for this account, "" if none
	Name    string // the picked person's name
	AppName string // the account's first name in the humans app, "" if not readable
	Hours   string
	DayList []string
	Tags    []humansTag
}

type humansTag struct {
	Tag              string
	Created, Updated int
}

type humansWeekView struct {
	ID, Label  string
	Free, Paid int
	Accts      []humansAcctView
}

type humansView struct {
	Base, Version string
	At            string
	Err           string
	Ready         bool
	People        []Person
	Weeks         []humansWeekView
}

var humanDayNames = [7]string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}

func (s *Server) handleHumans(rw http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	people := append([]Person(nil), s.st.people...)
	s.mu.Unlock()
	h := s.humans
	h.mu.Lock()
	defer h.mu.Unlock()
	v := humansView{Base: s.cfg.Base, Version: s.cfg.Version, Err: h.data.Err, Ready: h.dsn != "", People: people}
	if !h.data.At.IsZero() {
		v.At = h.data.At.In(s.tz).Format("Mon 2 Jan, 15:04")
	}
	names := map[string]string{}
	for _, p := range people {
		names[p.ID] = p.Name
	}
	for _, w := range h.data.Weeks {
		wv := humansWeekView{ID: w.ID, Label: w.Label, Free: w.Free, Paid: w.Paid}
		for _, a := range w.Accts {
			av := humansAcctView{HumansAcct: a, Who: h.who[strconv.Itoa(a.Acct)], Hours: fmtHours(float64(a.Minutes) / 60)}
			av.Name = names[av.Who]
			av.AppName = h.data.Names[a.Acct]
			for d, m := range a.Days {
				if m > 0 {
					av.DayList = append(av.DayList, humanDayNames[d]+" "+fmtHours(float64(m)/60))
				}
			}
			tags := map[string]*humansTag{}
			for t, n := range a.Created {
				if tags[t] == nil {
					tags[t] = &humansTag{Tag: t}
				}
				tags[t].Created = n
			}
			for t, n := range a.Updated {
				if tags[t] == nil {
					tags[t] = &humansTag{Tag: t}
				}
				tags[t].Updated = n
			}
			for _, t := range tags {
				av.Tags = append(av.Tags, *t)
			}
			sort.Slice(av.Tags, func(i, j int) bool {
				ni, nj := av.Tags[i].Created+av.Tags[i].Updated, av.Tags[j].Created+av.Tags[j].Updated
				if ni != nj {
					return ni > nj
				}
				return av.Tags[i].Tag < av.Tags[j].Tag
			})
			wv.Accts = append(wv.Accts, av)
		}
		v.Weeks = append(v.Weeks, wv)
	}
	s.render(rw, "humans.html", "humans.html", v)
}

// handleHumansWho: POST /admin/humans/who with acct and p ("" clears).
func (s *Server) handleHumansWho(rw http.ResponseWriter, r *http.Request) {
	acct, err := strconv.Atoi(r.FormValue("acct"))
	if err != nil || acct < 0 {
		http.Error(rw, "Which account?", http.StatusBadRequest)
		return
	}
	p := r.FormValue("p")
	if p != "" {
		s.mu.Lock()
		_, ok := s.st.person(p)
		s.mu.Unlock()
		if !ok {
			http.Error(rw, "Who?", http.StatusBadRequest)
			return
		}
	}
	h := s.humans
	h.mu.Lock()
	defer h.mu.Unlock()
	if p == "" {
		delete(h.who, strconv.Itoa(acct))
	} else {
		h.who[strconv.Itoa(acct)] = p
	}
	writeJSON(h.names, h.who)
	rw.WriteHeader(http.StatusNoContent)
}

// readHumanNames: the first name of each account, from the humans app's
// users (only id and name are asked for). Nil when the login may not read them.
func readHumanNames(ctx context.Context, dsn string) map[int]string {
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
	rows, err := tx.Query(ctx, `SELECT id, coalesce(name,'') FROM users`)
	if err != nil {
		log.Printf("Snitch: humans app names not readable (%v)", err)
		return nil
	}
	defer rows.Close()
	out := map[int]string{}
	for rows.Next() {
		var id int64
		var name string
		if rows.Scan(&id, &name) == nil {
			if f := strings.Fields(name); len(f) > 0 {
				out[int(id)] = f[0]
			}
		}
	}
	if rows.Err() != nil {
		log.Printf("Snitch: humans app names not readable (%v)", rows.Err())
		return nil
	}
	return out
}
