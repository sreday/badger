package snitch

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Person is one team member with a page of their own. The link token and
// the PIN are made once and never change.
type Person struct {
	ID    string
	Name  string
	Token string
	PIN   string
	Email string // empty for now: the key for matching the humans app later
	TZ    string // the person's own day ("today" on their page); Europe/London unless set
}

// Link is one chip on a mission pill.
type Link struct{ Label, URL string }

// Seg is one stretch of work on a task; a zero To means the clock runs.
type Seg struct {
	From time.Time
	To   time.Time
}

// Task is a mission placed on a day. What the worker reads (name, how-to,
// links) is copied in when it's placed, so later catalogue changes don't
// rewrite a week already planned.
type Task struct {
	ID        string
	Mission   string
	Name      string
	Brand     string
	Event     string // event id, "" for a generic mission
	EventName string
	Day       int // 0 = Monday ... 4 = Friday
	Start     int // minutes from the left end of the day's track (a place on the timeline, not a clock time)
	Minutes   int
	Note      string
	How       string
	Links     []Link

	Segs     []Seg
	Done     bool
	DoneAt   time.Time
	DoneNote string
	DoneLink string
}

func (t *Task) Running() bool { return len(t.Segs) > 0 && t.Segs[len(t.Segs)-1].To.IsZero() }
func (t *Task) Started() bool { return len(t.Segs) > 0 }

// Spent is the time on the clock so far.
func (t *Task) Spent(now time.Time) time.Duration {
	var d time.Duration
	for _, s := range t.Segs {
		to := s.To
		if to.IsZero() {
			to = now
		}
		if to.After(s.From) {
			d += to.Sub(s.From)
		}
	}
	return d
}

// State: "" (not started), running, paused, done.
func (t *Task) State() string {
	switch {
	case t.Done:
		return "done"
	case t.Running():
		return "running"
	case t.Started():
		return "paused"
	}
	return ""
}

// Week is one person's Monday-Friday plan.
type Week struct {
	Person   string
	ID       string // 2026-W47
	Weekly   float64    // the worker's hours for the week, set on this week; 0 = as the weeks before
	Hours    [5]float64 // no longer used: each day had its own hours
	Tasks    []*Task
	Locked   bool
	LockedAt time.Time
}

func (w *Week) task(id string) *Task {
	for _, t := range w.Tasks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

// noCap is a day with no end, for weeks read from disk as they were planned.
const noCap = 1 << 30

// fit finds where a mission can sit on a day's track and how long it may
// be there. It starts at start when that spot is free, otherwise right
// after the mission lying there. It never reaches past the next mission or
// past the end of the day (cap, in minutes): a mission too long for the
// gap is trimmed to it. The day's hours are fixed; ok is false when the
// day has no room left from there on. skip is the mission being moved (its
// old place doesn't count).
func (w *Week) fit(day, start, minutes int, skip *Task, cap int) (at, length int, ok bool) {
	if start < 0 {
		start = 0
	}
	start = start / 30 * 30
	for {
		if start >= cap {
			return 0, 0, false
		}
		limit, inside := cap, false
		for _, t := range w.Tasks {
			if t == skip || t.Day != day {
				continue
			}
			switch {
			case t.Start <= start && start < t.Start+t.Minutes:
				start, inside = (t.Start+t.Minutes+29)/30*30, true
			case t.Start > start && t.Start < limit:
				limit = t.Start
			}
		}
		if !inside {
			return start, min(minutes, limit-start), true
		}
	}
}

// room is how long a mission may grow: up to the next one, and never past
// the end of the day.
func (w *Week) room(t *Task, cap int) int {
	room := max(cap-t.Start, t.Minutes) // a mission already past the day's end may shrink, not grow
	for _, o := range w.Tasks {
		if o != t && o.Day == t.Day && o.Start > t.Start && o.Start-t.Start < room {
			room = o.Start - t.Start
		}
	}
	return room
}

// spread gives every mission its own stretch: weeks planned before
// missions had a place had them all at the start of the day.
func (w *Week) spread() {
	var done []*Task
	all := w.Tasks
	for _, t := range all {
		w.Tasks = done
		t.Start, _, _ = w.fit(t.Day, t.Start, t.Minutes, nil, noCap)
		done = append(done, t)
	}
	w.Tasks = all
}

// dayStarted: the worker has touched this day, so it can't change any more.
func (w *Week) dayStarted(day int) bool {
	for _, t := range w.Tasks {
		if t.Day == day && t.Started() {
			return true
		}
	}
	return false
}

// Entry is one line of a person's log.
type Entry struct {
	T    time.Time
	Kind string // open pin_ok pin_fail start resume pause finish away back auto_pause lock unlock edit
	Task string `json:",omitempty"`
	Name string `json:",omitempty"`
	Note string `json:",omitempty"`
	Link string `json:",omitempty"`
}

// Store keeps everything in plain files under dir. The Server's lock guards it.
type Store struct {
	dir    string
	people []Person
	weeks  map[string]*Week // person/weekID
	custom []Mission
}

func OpenStore(dir string) (*Store, error) {
	for _, d := range []string{dir, filepath.Join(dir, "weeks"), filepath.Join(dir, "log"), filepath.Join(dir, "summaries")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	st := &Store{dir: dir, weeks: map[string]*Week{}}
	if !readJSON(filepath.Join(dir, "people.json"), &st.people) || len(st.people) == 0 {
		for _, name := range []string{"Anya", "Magda", "Petras", "Sanjana"} {
			p, err := newPerson(name)
			if err != nil {
				return nil, err
			}
			st.people = append(st.people, p)
		}
		if err := writeJSON(filepath.Join(dir, "people.json"), st.people); err != nil {
			return nil, err
		}
	}
	readJSON(filepath.Join(dir, "missions.json"), &st.custom)
	files, _ := filepath.Glob(filepath.Join(dir, "weeks", "*", "*.json"))
	for _, f := range files {
		var w Week
		if readJSON(f, &w) && w.Person != "" && w.ID != "" {
			w.spread()
			st.weeks[w.Person+"/"+w.ID] = &w
		}
	}
	return st, nil
}

func newPerson(name string) (Person, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return Person{}, err
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return Person{}, err
	}
	return Person{ID: strings.ToLower(name), Name: name, Token: hex.EncodeToString(b), PIN: fmt.Sprintf("%06d", n.Int64()), TZ: "Europe/London"}, nil
}

func (st *Store) person(id string) (Person, bool) {
	for _, p := range st.people {
		if p.ID == id {
			return p, true
		}
	}
	return Person{}, false
}

func (st *Store) byToken(token string) (Person, bool) {
	for _, p := range st.people {
		if p.Token == token {
			return p, true
		}
	}
	return Person{}, false
}

// week returns the person's week, a blank 8-hours-a-day one when it was
// never planned (not saved until something is put on it).
func (st *Store) week(person, id string) *Week {
	if w, ok := st.weeks[person+"/"+id]; ok {
		return w
	}
	return &Week{Person: person, ID: id, Hours: [5]float64{8, 8, 8, 8, 8}}
}

func (st *Store) saveWeek(w *Week) error {
	sort.SliceStable(w.Tasks, func(i, j int) bool {
		if w.Tasks[i].Day != w.Tasks[j].Day {
			return w.Tasks[i].Day < w.Tasks[j].Day
		}
		return w.Tasks[i].Start < w.Tasks[j].Start
	})
	st.weeks[w.Person+"/"+w.ID] = w
	dir := filepath.Join(st.dir, "weeks", w.Person)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeJSON(filepath.Join(dir, w.ID+".json"), w)
}

// defaultWeekly is a worker's week until someone types another value.
const defaultWeekly = 40

// weekly is the worker's hours in a week: the value set on that week, else
// the one from the nearest week before it that has one. So a change
// carries into every later week until it is changed again.
func (st *Store) weekly(person, id string) float64 {
	best, at := float64(defaultWeekly), ""
	for _, w := range st.weeks {
		if w.Person == person && w.Weekly > 0 && w.ID <= id && w.ID > at {
			best, at = w.Weekly, w.ID
		}
	}
	return best
}

// dayCap is the end of the worker's day on the track, in minutes.
func (st *Store) dayCap(person, id string) int { return int(st.perDay(person, id)*60 + .5) }

// perDay spreads the week evenly over Monday to Friday.
func (st *Store) perDay(person, id string) float64 { return st.weekly(person, id) / 5 }

// setWeekly sets the weekly hours from this week on: later weeks follow it
// (a value typed on a later week before is dropped).
func (st *Store) setWeekly(w *Week, hours float64) error {
	for _, o := range st.weeks {
		if o.Person == w.Person && o.ID > w.ID && o.Weekly > 0 {
			o.Weekly = 0
			if err := st.saveWeek(o); err != nil {
				return err
			}
		}
	}
	w.Weekly = hours
	return st.saveWeek(w)
}

func (st *Store) saveCustom() error {
	return writeJSON(filepath.Join(st.dir, "missions.json"), st.custom)
}

// appendLog adds one line to the person's log; lines are never rewritten.
func (st *Store) appendLog(person string, e Entry) {
	data, _ := json.Marshal(e)
	f, err := os.OpenFile(filepath.Join(st.dir, "log", person+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	f.Write(append(data, '\n'))
	f.Sync()
	f.Close()
}

// readLog returns the person's entries from..to (to exclusive), oldest first.
func (st *Store) readLog(person string, from, to time.Time) []Entry {
	f, err := os.Open(filepath.Join(st.dir, "log", person+".jsonl"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.T.Before(from) || !e.T.Before(to) {
			continue
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].T.Before(out[j].T) })
	return out
}

func readJSON(path string, v any) bool {
	data, err := os.ReadFile(path)
	return err == nil && json.Unmarshal(data, v) == nil
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// --- Weeks and days ---------------------------------------------------------

const dayFmt = "2006-01-02"

// weekID is the ISO week of t, as 2026-W47.
func weekID(t time.Time) string {
	y, w := t.ISOWeek()
	return fmt.Sprintf("%d-W%02d", y, w)
}

// mondayOf returns the Monday (UTC midnight) of a week id.
func mondayOf(id string) (time.Time, bool) {
	var y, w int
	if _, err := fmt.Sscanf(id, "%d-W%d", &y, &w); err != nil || w < 1 || w > 53 || y < 2020 || y > 2100 {
		return time.Time{}, false
	}
	jan4 := time.Date(y, 1, 4, 0, 0, 0, 0, time.UTC) // always in week 1
	mon := jan4.AddDate(0, 0, -int((jan4.Weekday()+6)%7))
	return mon.AddDate(0, 0, (w-1)*7), true
}

// dateOf is the calendar day of t in loc, as a UTC midnight (days are
// compared as dates, never as instants).
func dateOf(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func parseDay(s string) time.Time {
	t, _ := time.Parse(dayFmt, s)
	return t
}

// fmtMin: 30 min, 1 h, 1 h 30.
func fmtMin(m int) string {
	if m < 0 {
		m = 0
	}
	h, r := m/60, m%60
	switch {
	case h == 0:
		return fmt.Sprintf("%d min", r)
	case r == 0:
		return fmt.Sprintf("%d h", h)
	}
	return fmt.Sprintf("%d h %02d", h, r)
}

func fmtHours(h float64) string {
	return strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%.1f", h), "0"), ".")
}
