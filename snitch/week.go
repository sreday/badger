package snitch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type taskView struct {
	*Task
	Time     string
	Spent    string
	SpentSec int
	State    string
	Verdict  string // the Log's wording
	Class    string
	Rows     string // half hours (a trimmed one may be a fraction): the clip's width on the timeline
	End      int    // the half hour the clip ends in
	Slot     int    // half hours from the left: where the clip starts
	Short    string // the event without its brand
	Full     string // the event's whole name: SREday Campinas 2026 Q4
	Tip      string
	CanCopy  bool // the same stretch is free on the next day: the arrow copies it down
	// CanCopyRight: the stretch right after it, as long again, is free on
	// the same day: the right arrow copies it there.
	CanCopyRight bool
}

type dayView struct {
	I       int
	Name    string
	Date    string
	Hours   string
	Planned string
	Over    bool
	Edit    bool // open for planning: not past, not started by the worker
	Frozen  bool
	Why     string // why a closed day can't change: it has started, or it is over
	Today   bool
	Rows    int // half hours in the day
	Tasks   []taskView
}

type logDay struct {
	Day   string
	Lines []logLine
}

type logLine struct{ At, Text, Kind string }

type adminView struct {
	Base, Version string
	People        []personTab
	P             Person
	W             string
	Label         string
	Prev, Next    string
	View          string
	Days          []dayView
	Locked        bool
	Ready         bool
	Ticker        string
	Shelf         []ShelfTab
	Weekly        string // the worker's hours this week, spread evenly over the five days
	Slots         string // a day in half hours (may be a fraction): the timeline's width
	GridRows      int
	Scale         []tick // the ruler: one tick an hour
	Cal           []calWeek
	Hints         []string // the hint box beside the Tasks tabs
	Timeline      []logDay
	Given, Spent  string
	Summary       *Summary
	Asks          []*Job
	Busy          []string
	Links         []personLink
	ClaudeReady   bool
	Routine       string
	Lessons       Lessons
	EventsN       int
	EventsErr     string
}

// tick is one hour on the ruler; Part is its width in half hours (2,
// less for the last one when the day ends mid-hour).
type tick struct {
	Label string
	Part  string
}

// The full-screen calendar: the week on screen and two either side, every
// known event on its day in its brand's colour.
type calWeek struct {
	Label string
	Href  string
	On    bool
	Past  bool // the whole week is behind us: nothing in it can change
	Days  []calDay
}

type calDay struct {
	Num     string // 16, or "Dec 1" on the first of a month
	Today   bool
	Weekend bool
	Events  []calEvent
}

type calEvent struct{ Name, Brand, Full string }

func (s *Server) calendar(p Person, mon, now time.Time, view string) []calWeek {
	today := dateOf(now, s.tz)
	events := s.events.All()
	var out []calWeek
	for i := -2; i <= 2; i++ {
		m := mon.AddDate(0, 0, 7*i)
		w := calWeek{Label: weekLabel(m), On: i == 0, Past: m.AddDate(0, 0, 6).Before(today), Href: s.cfg.Base + "/?p=" + p.ID + "&w=" + weekID(m) + "&view=" + view}
		for d := 0; d < 7; d++ {
			date := m.AddDate(0, 0, d)
			cd := calDay{Num: date.Format("2"), Today: date.Equal(today), Weekend: d > 4}
			if date.Day() == 1 || (i == -2 && d == 0) {
				cd.Num = date.Format("Jan 2")
			}
			key := date.Format(dayFmt)
			for _, ev := range events {
				if ev.Day <= key && key <= ev.EndDay {
					cd.Events = append(cd.Events, calEvent{ev.Name, ev.Brand, fullEvent(ev.ID, ev.Name)})
				}
			}
			w.Days = append(w.Days, cd)
		}
		out = append(out, w)
	}
	return out
}

type personTab struct {
	ID, Name string
	On       bool
}

type personLink struct{ Name, URL, PIN string }

func taskViewOf(t *Task, now time.Time) taskView {
	spent := t.Spent(now)
	v := taskView{Task: t, Time: fmtMin(t.Minutes), Spent: fmtMin(int(spent.Minutes())), SpentSec: int(spent.Seconds()), State: t.State(), Full: fullEvent(t.Event, t.EventName)}
	over := int(spent.Minutes()) - t.Minutes
	switch v.State {
	case "":
		v.Verdict, v.Class = "not started", "off"
	case "running":
		v.Verdict, v.Class = "running", "live"
	case "paused":
		v.Verdict, v.Class = "paused", "wait"
	case "done":
		if over > 0 {
			v.Verdict, v.Class = "finished, "+fmtMin(over)+" over", "bad"
		} else {
			v.Verdict, v.Class = "finished on time", "ok"
		}
	}
	return v
}

// weekLabel names a week by its Monday to Friday: "November 16-20 2026";
// across two months "Nov 30 - Dec 4 2026"; across the new year
// "Dec 28 2026 - Jan 1 2027".
func weekLabel(mon time.Time) string {
	fri := mon.AddDate(0, 0, 4)
	switch {
	case mon.Year() != fri.Year():
		return mon.Format("Jan 2 2006") + " - " + fri.Format("Jan 2 2006")
	case mon.Month() != fri.Month():
		return mon.Format("Jan 2") + " - " + fri.Format("Jan 2 2006")
	}
	return mon.Format("January 2") + "-" + fri.Format("2 2006")
}

func (s *Server) days(p Person, w *Week, mon, now time.Time) []dayView {
	today := dateOf(now, s.personTZ(p))
	perDay := s.st.perDay(p.ID, w.ID)
	out := make([]dayView, 5)
	for d := range out {
		date := mon.AddDate(0, 0, d)
		min := 0
		dv := dayView{I: d, Name: dayNames[d], Date: date.Format("2 Jan"), Hours: fmtHours(perDay), Frozen: s.frozen(p, w, d, now), Today: date.Equal(today)}
		dv.Edit = !dv.Frozen
		if dv.Frozen {
			dv.Why = "This day is over, so it's no longer editable."
			if w.dayStarted(d) {
				dv.Why = "This day has started, so it's no longer editable."
			}
		}
		for _, t := range w.Tasks {
			if t.Day == d {
				tv := taskViewOf(t, now)
				tv.Rows, tv.Slot, tv.Short = strconv.FormatFloat(float64(t.Minutes)/30, 'f', -1, 64), t.Start/30, shortEvent(t.Brand, t.EventName)
				tv.End = (t.Start + t.Minutes + 29) / 30
				tv.Tip = t.Name + ", " + tv.Time
				if tv.Full != "" {
					tv.Tip = t.Name + " (" + tv.Full + "), " + tv.Time
				}
				if t.Note != "" {
					tv.Tip += ". Note: " + t.Note
				}
				tv.CanCopy = d < 4 && !s.frozen(p, w, d+1, now) && w.free(d+1, t.Start, t.Minutes, s.st.dayCap(p.ID, w.ID))
				tv.CanCopyRight = !dv.Frozen && w.free(d, rightOf(t), t.Minutes, s.st.dayCap(p.ID, w.ID))
				dv.Tasks = append(dv.Tasks, tv)
				min += t.Minutes
			}
		}
		dv.Planned = fmtHours(float64(min) / 60)
		dv.Over = min > int(perDay*60)
		dv.Rows = int(perDay * 2)
		out[d] = dv
	}
	return out
}

// ready: every day still open has something on it (a day already past or
// started counts as it is). The ticker says how far.
func (s *Server) ready(p Person, w *Week, now time.Time) (bool, string) {
	days, min, hours := 0, 0, 0.0
	for d := 0; d < 5; d++ {
		n := 0
		for _, t := range w.Tasks {
			if t.Day == d {
				n++
				min += t.Minutes
			}
		}
		if n > 0 || s.frozen(p, w, d, now) {
			days++
		}
		hours += s.st.perDay(p.ID, w.ID)
	}
	return days == 5 && len(w.Tasks) > 0, fmt.Sprintf("%s / %s h", fmtHours(float64(min)/60), fmtHours(hours))
}

// currentWeek is the week the board opens on: this week, and from Saturday
// the coming one (the old week has nothing left to plan).
func (s *Server) currentWeek(now time.Time) string {
	t := now.In(s.tz)
	if d := t.Weekday(); d == time.Saturday || d == time.Sunday {
		t = t.AddDate(0, 0, 2)
	}
	return weekID(t)
}

func (s *Server) handleAdmin(rw http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	p, ok := s.st.person(r.FormValue("p"))
	if !ok {
		p = s.st.people[0]
	}
	wid := r.FormValue("w")
	mon, ok := mondayOf(wid)
	if !ok {
		wid = s.currentWeek(now)
		mon, _ = mondayOf(wid)
	}
	w := s.st.week(p.ID, wid)
	v := adminView{
		Base: s.cfg.Base, Version: s.cfg.Version, P: p, W: wid, Label: weekLabel(mon),
		Prev: weekID(mon.AddDate(0, 0, -7)), Next: weekID(mon.AddDate(0, 0, 7)),
		View: "schedule", Days: s.days(p, w, mon, now),
		ClaudeReady: s.canFire(), Routine: s.routineID(), Lessons: s.lessons,
		EventsN: len(s.events.Get(now)),
	}
	if r.FormValue("view") == "log" {
		v.View = "log"
	}
	v.Cal = s.calendar(p, mon, now, v.View)
	s.events.mu.Lock()
	v.EventsErr = s.events.err
	s.events.mu.Unlock()
	v.Ready, v.Ticker = s.ready(p, w, now)
	for _, q := range s.st.people {
		v.People = append(v.People, personTab{q.ID, q.Name, q.ID == p.ID})
		v.Links = append(v.Links, personLink{q.Name, web_base(s.cfg.PublicURL, r) + s.cfg.Base + "/" + q.Token, q.PIN})
	}
	if v.View == "schedule" {
		v.Shelf = s.shelf(mon, now)
		v.Hints = s.hintLines(w, mon, now)
		v.Weekly = fmtHours(s.st.weekly(p.ID, wid))
		slots := s.st.perDay(p.ID, wid) * 2
		v.GridRows = 2
		for i := range v.Days {
			rows := 0
			for j := range v.Days[i].Tasks {
				t := &v.Days[i].Tasks[j]
				rows = max(rows, t.End)
			}
			v.GridRows = max(v.GridRows, rows)
		}
		// The table is exactly the day's hours wide; wider only when a
		// mission already reaches past them.
		slots = max(slots, float64(v.GridRows))
		v.Slots = strconv.FormatFloat(slots, 'f', 2, 64)
		for h := 1; float64(h-1)*2 < slots-0.01; h++ {
			v.Scale = append(v.Scale, tick{fmt.Sprintf("%dh", h), strconv.FormatFloat(min(2, slots-float64(h-1)*2), 'f', 2, 64)})
		}
	} else {
		v.Timeline = s.timeline(p, mon)
		given, spent := 0, 0
		for _, t := range w.Tasks {
			given += t.Minutes
			spent += int(t.Spent(now).Minutes())
		}
		v.Given, v.Spent = fmtMin(given), fmtMin(spent)
		var sum Summary
		if readJSON(filepath.Join(s.dir, "summaries", p.ID, wid+".json"), &sum) {
			v.Summary = &sum
		}
		for i := len(s.jobs) - 1; i >= 0 && len(v.Asks) < 6; i-- {
			if j := s.jobs[i]; j.Kind == "ask" && j.Person == p.ID {
				v.Asks = append(v.Asks, j)
			}
		}
	}
	for _, j := range s.jobs {
		if j.fresh(now) && (j.Person == p.ID || j.Kind == "lessons") {
			v.Busy = append(v.Busy, j.label())
		}
	}
	name := "admin.html"
	if r.FormValue("frag") == "1" {
		name = "board"
	}
	s.render(rw, "admin.html", name, v)
}

func web_base(public string, r *http.Request) string {
	if public != "" {
		return strings.TrimSuffix(public, "/")
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// timeline is the person's log for the week, in the team's clock.
func (s *Server) timeline(p Person, mon time.Time) []logDay {
	from := time.Date(mon.Year(), mon.Month(), mon.Day(), 0, 0, 0, 0, s.tz)
	var out []logDay
	for _, e := range s.st.readLog(p.ID, from, from.AddDate(0, 0, 7)) {
		day := e.T.In(s.tz).Format("Monday 2 Jan")
		if len(out) == 0 || out[len(out)-1].Day != day {
			out = append(out, logDay{Day: day})
		}
		out[len(out)-1].Lines = append(out[len(out)-1].Lines, logLine{e.T.In(s.tz).Format("15:04"), entryText(e), e.Kind})
	}
	return out
}

func entryText(e Entry) string {
	switch e.Kind {
	case "start":
		return "started " + e.Name
	case "resume":
		return "resumed " + e.Name
	case "pause":
		return "paused " + e.Name
	case "finish":
		t := "finished " + e.Name + ": " + e.Note
		if e.Link != "" {
			t += " (" + e.Link + ")"
		}
		return t
	case "away":
		return "page closed, the clock on " + e.Name + " kept running"
	case "back":
		return "page open again"
	case "auto_pause":
		return "clock on " + e.Name + " stopped by Snitch: " + e.Note
	case "open":
		return "opened the page"
	case "pin_ok":
		return "signed in with the PIN"
	case "pin_fail":
		return "wrong PIN"
	case "lock":
		return "schedule locked (" + e.Note + ")" // weeks from before the lock was dropped
	case "unlock":
		return "schedule unlocked (" + e.Note + ")"
	}
	return e.Kind
}

// handleAction: POST /admin/{action} with p (person) and w (week).
func (s *Server) handleAction(rw http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	action := r.PathValue("action")
	fail := func(code int, msg string) { http.Error(rw, msg, code) }
	done := func() { rw.WriteHeader(http.StatusNoContent) }

	switch action { // the ones that aren't about a week
	case "claude":
		s.claude.Routine = strings.TrimSpace(r.FormValue("routine"))
		if t := strings.TrimSpace(r.FormValue("token")); t != "" {
			s.claude.Token = t
		}
		writeJSON(filepath.Join(s.dir, "claude.json"), s.claude)
		done()
		return
	case "mission":
		name := strings.TrimSpace(r.FormValue("name"))
		tab := r.FormValue("tab")
		min, _ := strconv.Atoi(r.FormValue("minutes"))
		if name == "" || len(name) > 80 || !validTab(tab) {
			fail(http.StatusBadRequest, "Give the mission a name")
			return
		}
		_ = min
		s.st.custom = append(s.st.custom, Mission{ID: "x-" + newID(), Name: name, Set: "custom", Tab: tab, Repeat: "daily"})
		s.st.saveCustom()
		done()
		return
	case "forget":
		id := r.FormValue("mission")
		for i, m := range s.st.custom {
			if m.ID == id {
				s.st.custom = append(s.st.custom[:i], s.st.custom[i+1:]...)
				s.st.saveCustom()
				break
			}
		}
		done()
		return
	}

	p, ok := s.st.person(r.FormValue("p"))
	if !ok {
		fail(http.StatusBadRequest, "Unknown person")
		return
	}
	wid := r.FormValue("w")
	_, ok = mondayOf(wid)
	if !ok {
		fail(http.StatusBadRequest, "Unknown week")
		return
	}
	w := s.st.week(p.ID, wid)

	switch action {
	case "summarise":
		s.addJob(&Job{Kind: "summary", Person: p.ID, Week: wid}, now)
		done()
		return
	case "ask":
		q := strings.TrimSpace(r.FormValue("question"))
		if q == "" {
			fail(http.StatusBadRequest, "Type a question")
			return
		}
		s.addJob(&Job{Kind: "ask", Person: p.ID, Week: wid, Question: q}, now)
		done()
		return
	case "autofill":
		s.addJob(&Job{Kind: "autofill", Person: p.ID, Week: wid}, now)
		done()
		return
	}

	// Everything below changes the plan. There is no lock: the worker sees
	// the week as it stands, and a day closes for good once it is past or
	// the worker has started a mission on it.
	open := func(day int) bool {
		if day < 0 || day > 4 {
			fail(http.StatusBadRequest, "Unknown day")
			return false
		}
		if s.frozen(p, w, day, now) {
			fail(http.StatusConflict, dayNames[day]+" can't change any more: it is past or already started.")
			return false
		}
		return true
	}
	day, _ := strconv.Atoi(r.FormValue("day"))
	start, _ := strconv.Atoi(r.FormValue("start")) // minutes along the day's track
	if start > 20*60 {
		start = 20 * 60
	}
	dayCap := s.st.dayCap(p.ID, wid) // the day ends here: its hours are fixed
	t := w.task(r.FormValue("task"))
	needTask := func() bool {
		if t == nil {
			fail(http.StatusNotFound, "That mission is gone")
			return false
		}
		return open(t.Day)
	}

	// land finds the dropped mission's spot on the day: pushed in where
	// another one starts (dropped on its left side), or at the free spot.
	land := func(minutes int, moving *Task) (int, int, bool) {
		if b := w.task(r.FormValue("before")); b != nil && b != moving && b.Day == day {
			return w.insert(b, minutes, moving, dayCap)
		}
		// Dropped on a mission's right side: right after it. A mission
		// already sitting there is pushed along; free time is simply used.
		if a := w.task(r.FormValue("after")); a != nil && a != moving && a.Day == day {
			at := (a.Start + a.Minutes + 29) / 30 * 30
			for _, o := range w.Tasks {
				if o != moving && o != a && o.Day == day && o.Start == at {
					return w.insert(o, minutes, moving, dayCap)
				}
			}
			return w.fit(day, at, minutes, moving, dayCap)
		}
		return w.fit(day, start, minutes, moving, dayCap)
	}

	// pick: the missions named in tasks (several picked on the board), or
	// the one in task.
	pick := func() []*Task {
		var out []*Task
		if ids := r.FormValue("tasks"); ids != "" {
			for _, id := range strings.Split(ids, ",") {
				if g := w.task(id); g != nil {
					out = append(out, g)
				}
			}
		} else if t != nil {
			out = append(out, t)
		}
		return out
	}

	// Every change below can be undone: the missions as they were go into
	// the week's history once the change is saved.
	before := cloneTasks(w.Tasks)
	history := true

	switch action {
	case "undo", "redo":
		// Back or forward one step. Only the open days change: a day that
		// has closed since (past, or started by the worker) stays as it is,
		// so no clock is ever undone.
		h := s.st.history(p.ID, wid)
		from, to := &h.Undo, &h.Redo
		if action == "redo" {
			from, to = to, from
		}
		if len(*from) == 0 {
			fail(http.StatusConflict, "Nothing to "+action)
			return
		}
		snap := (*from)[len(*from)-1]
		*from = (*from)[:len(*from)-1]
		*to = append(*to, before)
		w.Tasks = s.restore(p, w, snap, now)
		if err := s.st.saveHistory(p.ID, wid, h); err != nil {
			fail(http.StatusInternalServerError, "Couldn't save: "+err.Error())
			return
		}
		history = false
	case "copy", "cut":
		// Copy keeps the picked missions for Paste, in any week of any
		// worker. Cut also takes them off this week.
		group := pick()
		if len(group) == 0 {
			fail(http.StatusNotFound, "Those missions are gone")
			return
		}
		if action == "cut" {
			for _, g := range group {
				if !open(g.Day) {
					return
				}
			}
		}
		s.clip = cloneTasks(group)
		if action == "copy" {
			done()
			return
		}
		in := map[*Task]bool{}
		for _, g := range group {
			in[g] = true
		}
		var keep []*Task
		for _, o := range w.Tasks {
			if !in[o] {
				keep = append(keep, o)
			}
		}
		w.Tasks = keep
	case "paste":
		// The copied missions land from day/start on, keeping their days
		// and gaps between them. One that finds no room, or lands on a
		// closed day, is left out.
		if len(s.clip) == 0 {
			fail(http.StatusConflict, "Nothing copied yet")
			return
		}
		src := cloneTasks(s.clip)
		sort.SliceStable(src, func(i, j int) bool {
			if src[i].Day != src[j].Day {
				return src[i].Day < src[j].Day
			}
			return src[i].Start < src[j].Start
		})
		first, n := src[0], 0
		for _, o := range src {
			d := day + o.Day - first.Day
			if d < 0 || d > 4 || s.frozen(p, w, d, now) {
				continue
			}
			at, length, ok := w.fit(d, start+o.Start-first.Start, o.Minutes, nil, dayCap)
			if !ok {
				continue
			}
			c := copyTask(o)
			c.Day, c.Start, c.Minutes = d, at, length
			w.Tasks = append(w.Tasks, c)
			n++
		}
		if n == 0 {
			fail(http.StatusConflict, "No room to paste here")
			return
		}
	case "copyright":
		// The right arrow: the same mission again right after it, on the
		// same day, as long again. Only where that stretch is free.
		src := pick()
		if len(src) == 0 {
			fail(http.StatusNotFound, "That mission is gone")
			return
		}
		n := 0
		for _, o := range src {
			at := rightOf(o)
			if s.frozen(p, w, o.Day, now) || !w.free(o.Day, at, o.Minutes, dayCap) {
				continue
			}
			c := copyTask(o)
			c.Start = at
			w.Tasks = append(w.Tasks, c)
			n++
		}
		if n == 0 {
			fail(http.StatusConflict, "No room for it on the right")
			return
		}
	case "daysens", "priority":
		// The note tab's ticks: Day-sensitive (it has to be done on its own
		// day) and Prioritize (it goes first).
		if !needTask() {
			return
		}
		if action == "daysens" {
			t.DaySensitive = r.FormValue("on") == "1"
		} else {
			t.Priority = r.FormValue("on") == "1"
		}
	case "place":
		if !open(day) {
			return
		}
		nt, err := s.newTask(r.FormValue("mission"), r.FormValue("event"), now)
		if err != "" {
			fail(http.StatusBadRequest, err)
			return
		}
		at, length, ok := land(nt.Minutes, nil)
		if !ok {
			fail(http.StatusConflict, dayNames[day]+" is full")
			return
		}
		nt.Day, nt.Start, nt.Minutes = day, at, length
		w.Tasks = append(w.Tasks, nt)
	case "move":
		if !needTask() || !open(day) {
			return
		}
		// A mission longer than the gap it lands in is trimmed to it: the
		// day's hours don't stretch.
		at, length, ok := land(t.Minutes, t)
		if !ok {
			fail(http.StatusConflict, dayNames[day]+" is full")
			return
		}
		t.Day, t.Start, t.Minutes = day, at, length
	case "copydown":
		// The arrow on a pill: the same mission, length and note on the
		// next day, at the same spot. Only when that spot is free. With
		// several missions picked, every one of them that has room below
		// is copied.
		src := []*Task{}
		if ids := r.FormValue("tasks"); ids != "" {
			for _, id := range strings.Split(ids, ",") {
				if g := w.task(id); g != nil {
					src = append(src, g)
				}
			}
		} else if t != nil {
			src = append(src, t)
		}
		if len(src) == 0 {
			fail(http.StatusNotFound, "That mission is gone")
			return
		}
		var copies []*Task
		for _, o := range src {
			if o.Day >= 4 || s.frozen(p, w, o.Day+1, now) || !w.free(o.Day+1, o.Start, o.Minutes, dayCap) {
				continue
			}
			c := copyTask(o)
			c.Day = o.Day + 1
			copies = append(copies, c)
		}
		if len(copies) == 0 {
			fail(http.StatusConflict, "No room for it on the next day")
			return
		}
		w.Tasks = append(w.Tasks, copies...)
	case "clear":
		// Start over: everything off the week, except the days already
		// closed (past, or started by the worker).
		var keep []*Task
		for _, o := range w.Tasks {
			if s.frozen(p, w, o.Day, now) {
				keep = append(keep, o)
			}
		}
		w.Tasks = keep
	case "resize":
		// Dragging a clip's edge: its new length. Growing pushes the
		// missions behind it along, up to the end of the day.
		if !needTask() {
			return
		}
		m, _ := strconv.Atoi(r.FormValue("minutes"))
		if r.FormValue("side") == "left" {
			w.growLeft(t, m)
		} else {
			w.grow(t, m, dayCap)
		}
	case "copygroup":
		// Dragged with Cmd (or Alt) held: copies of the picked missions land
		// in the new place, keeping their days and the gaps between them;
		// the originals stay. One that finds no room, or lands on a closed
		// day, is left out.
		src := pick()
		if len(src) == 0 {
			fail(http.StatusNotFound, "Those missions are gone")
			return
		}
		dday, _ := strconv.Atoi(r.FormValue("dday"))
		dstart, _ := strconv.Atoi(r.FormValue("dstart"))
		sort.SliceStable(src, func(i, j int) bool { return src[i].Start < src[j].Start })
		n := 0
		for _, o := range src {
			d := o.Day + dday
			if d < 0 || d > 4 || s.frozen(p, w, d, now) {
				continue
			}
			at, length, ok := w.fit(d, max(0, o.Start+dstart), o.Minutes, nil, dayCap)
			if !ok {
				continue
			}
			c := copyTask(o)
			c.Day, c.Start, c.Minutes = d, at, length
			w.Tasks = append(w.Tasks, c)
			n++
		}
		if n == 0 {
			fail(http.StatusConflict, "No room to copy them there")
			return
		}
	case "movegroup", "removegroup":
		// Several missions picked with the mouse, moved together (each by
		// the same number of days and half hours) or dragged off the week.
		var group []*Task
		for _, id := range strings.Split(r.FormValue("tasks"), ",") {
			if g := w.task(id); g != nil {
				if !open(g.Day) {
					return
				}
				group = append(group, g)
			}
		}
		if len(group) == 0 {
			fail(http.StatusNotFound, "Those missions are gone")
			return
		}
		in := map[*Task]bool{}
		for _, g := range group {
			in[g] = true
		}
		if action == "removegroup" {
			var keep []*Task
			for _, o := range w.Tasks {
				if !in[o] {
					keep = append(keep, o)
				}
			}
			w.Tasks = keep
			break
		}
		dday, _ := strconv.Atoi(r.FormValue("dday"))
		dstart, _ := strconv.Atoi(r.FormValue("dstart"))
		type was struct{ day, start, minutes int }
		old := map[*Task]was{}
		for _, g := range group {
			if nd := g.Day + dday; nd < 0 || nd > 4 {
				fail(http.StatusConflict, "That would push a mission off the week")
				return
			}
			if !open(g.Day + dday) {
				return
			}
			old[g] = was{g.Day, g.Start, g.Minutes}
		}
		// Lift the group off the board, then set each one down in order;
		// if one finds no room, everything goes back.
		sort.SliceStable(group, func(i, j int) bool { return old[group[i]].start < old[group[j]].start })
		for _, g := range group {
			g.Day = -1
		}
		for _, g := range group {
			o := old[g]
			at, length, ok := w.fit(o.day+dday, max(0, o.start+dstart), o.minutes, nil, dayCap)
			if !ok {
				for q, b := range old {
					q.Day, q.Start, q.Minutes = b.day, b.start, b.minutes
				}
				fail(http.StatusConflict, dayNames[o.day+dday]+" has no room for all of them")
				return
			}
			g.Day, g.Start, g.Minutes = o.day+dday, at, length
		}
	case "remove":
		if !needTask() {
			return
		}
		for i, o := range w.Tasks {
			if o == t {
				w.Tasks = append(w.Tasks[:i], w.Tasks[i+1:]...)
				break
			}
		}
	case "minutes":
		if !needTask() {
			return
		}
		delta, _ := strconv.Atoi(r.FormValue("delta"))
		if m := t.Minutes + delta; (delta == 30 || delta == -30) && m >= 30 && m <= w.room(t, dayCap) {
			t.Minutes = m
		}
	case "note":
		if !needTask() {
			return
		}
		note := strings.TrimSpace(r.FormValue("note"))
		if len(note) > 400 {
			note = note[:400]
		}
		t.Note = note
	case "weekly":
		// Weekly hours, typed above the days: this week and the weeks after it.
		h, err := strconv.ParseFloat(strings.Replace(strings.TrimSpace(r.FormValue("hours")), ",", ".", 1), 64)
		h = float64(int(h*2+.5)) / 2
		if err != nil || h < 5 || h > 70 {
			fail(http.StatusBadRequest, "Weekly hours: a number from 5 to 70")
			return
		}
		if err := s.st.setWeekly(w, h); err != nil {
			fail(http.StatusInternalServerError, "Couldn't save: "+err.Error())
			return
		}
		done()
		return
	default:
		fail(http.StatusNotFound, "Unknown action")
		return
	}
	if err := s.st.saveWeek(w); err != nil {
		fail(http.StatusInternalServerError, "Couldn't save: "+err.Error())
		return
	}
	if history && !sameTasks(before, w.Tasks) {
		s.st.remember(p.ID, wid, before)
	}
	done()
}

// rightOf is where the right arrow puts a mission's copy: on the next half
// hour after it ends.
func rightOf(t *Task) int { return (t.Start + t.Minutes + 29) / 30 * 30 }

// restore is the week with a snapshot's missions on the days still open
// and the current ones on the days already closed.
func (s *Server) restore(p Person, w *Week, snap []*Task, now time.Time) []*Task {
	var closed [5]bool
	for d := range closed {
		closed[d] = s.frozen(p, w, d, now)
	}
	shut := func(d int) bool { return d < 0 || d > 4 || closed[d] }
	var out []*Task
	kept := map[string]bool{}
	for _, t := range w.Tasks {
		if shut(t.Day) {
			out = append(out, t)
			kept[t.ID] = true
		}
	}
	for _, t := range cloneTasks(snap) {
		if !shut(t.Day) && !kept[t.ID] { // a mission since moved onto a closed day stays there, once
			out = append(out, t)
		}
	}
	return out
}

// sameTasks: nothing changed, whatever the order.
func sameTasks(a, b []*Task) bool {
	if len(a) != len(b) {
		return false
	}
	key := func(ts []*Task) string {
		c := append([]*Task(nil), ts...)
		sort.Slice(c, func(i, j int) bool { return c[i].ID < c[j].ID })
		data, _ := json.Marshal(c)
		return string(data)
	}
	return key(a) == key(b)
}

// newTask makes a pill from a mission and, for a conference mission, the
// event it is for.
func (s *Server) newTask(missionID, eventID string, now time.Time) (*Task, string) {
	m, ok := s.mission(missionID)
	if !ok {
		return nil, "Unknown mission"
	}
	t := &Task{ID: newID(), Mission: m.ID, Name: m.Name, Minutes: m.Minutes, How: m.How, Brand: "generic"}
	if m.Brand != "" {
		t.Brand = m.Brand
	}
	if m.generic() {
		return t, ""
	}
	for _, ev := range s.events.Get(now) {
		if ev.ID == eventID && m.fits(ev.Brand) {
			t.Event, t.EventName, t.Brand, t.Links = ev.ID, ev.Name, ev.Brand, m.links(ev)
			return t, ""
		}
	}
	return nil, "Pick the event this mission is for"
}
