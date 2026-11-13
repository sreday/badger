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
}

type dayView struct {
	I       int
	Name    string
	Date    string
	Hours   string
	Planned string
	Over    bool
	Edit    bool // open for planning: not frozen, week not locked
	Frozen  bool
	Today   bool
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
	Hints         []string
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

type personTab struct {
	ID, Name string
	On       bool
}

type personLink struct{ Name, URL, PIN string }

func taskViewOf(t *Task, now time.Time) taskView {
	spent := t.Spent(now)
	v := taskView{Task: t, Time: fmtMin(t.Minutes), Spent: fmtMin(int(spent.Minutes())), SpentSec: int(spent.Seconds()), State: t.State()}
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

func weekLabel(mon time.Time) string {
	fri := mon.AddDate(0, 0, 4)
	span := mon.Format("2") + "-" + fri.Format("2 Jan")
	if mon.Month() != fri.Month() {
		span = mon.Format("2 Jan") + " - " + fri.Format("2 Jan")
	}
	return fmt.Sprintf("Week %d of %s, %s", (mon.Day()-1)/7+1, mon.Format("January"), span)
}

func (s *Server) days(p Person, w *Week, mon, now time.Time) []dayView {
	today := dateOf(now, s.personTZ(p))
	out := make([]dayView, 5)
	for d := range out {
		date := mon.AddDate(0, 0, d)
		min := 0
		dv := dayView{I: d, Name: dayNames[d], Date: date.Format("2 Jan"), Hours: fmtHours(w.Hours[d]), Frozen: s.frozen(p, w, d, now), Today: date.Equal(today)}
		dv.Edit = !dv.Frozen && !w.Locked
		for _, t := range w.Tasks {
			if t.Day == d {
				dv.Tasks = append(dv.Tasks, taskViewOf(t, now))
				min += t.Minutes
			}
		}
		dv.Planned = fmtHours(float64(min) / 60)
		dv.Over = min > int(w.Hours[d]*60)
		out[d] = dv
	}
	return out
}

// ready: every working day still open has something on it (a day off is
// set to 0 h; a day already past or started counts as it is). The ticker
// says how far.
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
		if n > 0 || w.Hours[d] == 0 || s.frozen(p, w, d, now) {
			days++
		}
		hours += w.Hours[d]
	}
	return days == 5 && len(w.Tasks) > 0, fmt.Sprintf("%d / 5 days · %s / %s h", days, fmtHours(float64(min)/60), fmtHours(hours))
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
		View: "schedule", Days: s.days(p, w, mon, now), Locked: w.Locked,
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
		v.Hints = s.hints(w, mon, now)
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
		return "schedule locked and shipped (" + e.Note + ")"
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
		if min < 30 || min > 480 {
			min = 60
		}
		s.st.custom = append(s.st.custom, Mission{ID: "x-" + newID(), Name: name, Set: "custom", Tab: tab, Minutes: min / 30 * 30, Repeat: "daily"})
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
		if w.Locked {
			fail(http.StatusConflict, "Unlock the schedule first")
			return
		}
		s.addJob(&Job{Kind: "autofill", Person: p.ID, Week: wid}, now)
		done()
		return
	case "lock":
		if ok, _ := s.ready(p, w, now); !ok {
			fail(http.StatusConflict, "Put something on every working day first (set a day off to 0 h)")
			return
		}
		w.Locked, w.LockedAt = true, now
		s.st.appendLog(p.ID, Entry{T: now, Kind: "lock", Note: wid})
		s.st.saveWeek(w)
		done()
		return
	case "unlock":
		w.Locked = false
		s.st.appendLog(p.ID, Entry{T: now, Kind: "unlock", Note: wid})
		s.st.saveWeek(w)
		done()
		return
	}

	// Everything below changes the plan: only while unlocked, and never a
	// day that is past or that the worker has started.
	if w.Locked {
		fail(http.StatusConflict, "The schedule is locked. Unlock it to change it.")
		return
	}
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
	t := w.task(r.FormValue("task"))
	needTask := func() bool {
		if t == nil {
			fail(http.StatusNotFound, "That mission is gone")
			return false
		}
		return open(t.Day)
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
		nt.Day = day
		w.Tasks = append(w.Tasks, nt)
	case "move":
		if !needTask() || !open(day) {
			return
		}
		t.Day = day
		// Dropped pills go to the end of their day.
		for i, o := range w.Tasks {
			if o == t {
				w.Tasks = append(append(w.Tasks[:i:i], w.Tasks[i+1:]...), t)
				break
			}
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
		if m := t.Minutes + delta; (delta == 30 || delta == -30) && m >= 30 && m <= 600 {
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
	case "hours":
		if !open(day) {
			return
		}
		delta, _ := strconv.ParseFloat(r.FormValue("delta"), 64)
		if h := w.Hours[day] + delta; (delta == 0.5 || delta == -0.5) && h >= 0 && h <= 14 {
			w.Hours[day] = h
		}
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
