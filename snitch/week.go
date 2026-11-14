package snitch

import (
	"fmt"
	"net/http"
	"path/filepath"
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
	Flag     string
	Tip      string
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
		wid = weekID(now.In(s.tz))
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
		flags := s.flags(w, mon)
		v.Weekly = fmtHours(s.st.weekly(p.ID, wid))
		slots := s.st.perDay(p.ID, wid) * 2
		v.GridRows = 2
		for i := range v.Days {
			rows := 0
			for j := range v.Days[i].Tasks {
				t := &v.Days[i].Tasks[j]
				rows = max(rows, t.End)
				if f := flags[t.ID]; f != "" {
					t.Flag, t.Tip = f, t.Tip+". "+f
				}
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

	switch action {
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
	done()
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
