package snitch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Claude reads the logs on Marek's subscription: a routine ("Snitch: read
// the logs") GETs /api/queue with the bearer token, does the jobs it finds
// and POSTs /api/answers. Snitch never calls a model itself. The buttons
// (Summarise now, Auto-fill, Ask) queue a job and start the routine through
// its API trigger; the weekly jobs are queued by the clock.

const fireURL = "https://api.anthropic.com/v1/claude_code/routines/%s/fire"

const jobLife = 7 * 24 * time.Hour // an unanswered job is dropped after this

type Job struct {
	ID       string
	Kind     string // summary lessons autofill ask
	Person   string
	Week     string
	Question string
	Auto     bool // queued by the clock, not a button
	Asked    time.Time
	Done     time.Time
	Answer   string
	Err      string
}

func (j *Job) pending(now time.Time) bool { return j.Done.IsZero() && now.Sub(j.Asked) < jobLife }

// fresh: asked a moment ago, so the page shows Claude at work and waits.
func (j *Job) fresh(now time.Time) bool { return j.pending(now) && now.Sub(j.Asked) < 20*time.Minute }

func (j *Job) label() string {
	switch j.Kind {
	case "summary":
		return "Claude is writing the summary"
	case "lessons":
		return "Claude is reading last week's missions"
	case "autofill":
		return "Claude is filling the week"
	}
	return "Claude is answering"
}

// Summary is Claude's reading of one person's week.
type Summary struct {
	Text  string
	At    time.Time
	Model string
}

// Lessons is what the Monday read learnt: standing lines for the next
// suggestions, and better default times per mission.
type Lessons struct {
	Lines   []string
	Minutes map[string]int
	At      time.Time
}

type claudeSetup struct{ Routine, Token string }

func (s *Server) routineID() string {
	if s.claude.Routine != "" {
		return s.claude.Routine
	}
	return s.cfg.Routine
}

func (s *Server) routineToken() string {
	if s.claude.Token != "" {
		return s.claude.Token
	}
	return s.cfg.RoutineToken
}

func (s *Server) canFire() bool { return s.routineID() != "" && s.routineToken() != "" }

func (s *Server) saveJobs() {
	if len(s.jobs) > 80 {
		s.jobs = s.jobs[len(s.jobs)-80:]
	}
	writeJSON(filepath.Join(s.dir, "jobs.json"), s.jobs)
}

// addJob queues a job (once) and starts the routine when it can be started.
func (s *Server) addJob(j *Job, now time.Time) {
	for _, o := range s.jobs {
		if o.pending(now) && o.Kind == j.Kind && o.Person == j.Person && o.Week == j.Week && o.Question == j.Question {
			return
		}
	}
	j.ID, j.Asked = newID(), now
	s.jobs = append(s.jobs, j)
	s.saveJobs()
	if s.canFire() {
		id, token, base := s.routineID(), s.routineToken(), s.fireBase
		go func() {
			if err := fireTrigger(base, id, token, "SNITCH JOBS\nThere is work in the Snitch queue. Read it and answer it."); err != nil {
				log.Printf("Snitch: starting Claude: %v", err)
			}
		}()
	}
}

func fireTrigger(base, id, token, text string) error {
	body, _ := json.Marshal(map[string]string{"text": text})
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf(base, id), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "experimental-cc-routine-2026-04-01")
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%d %s", resp.StatusCode, strings.TrimSpace(string(out)))
	}
	return nil
}

// dueJobs queues the weekly reads: every person's summary from Friday
// 18:00 UTC, and the lessons from last week's missions from Monday 06:00.
func (s *Server) dueJobs(now time.Time) {
	u := now.UTC()
	has := func(kind, person, week string) bool {
		for _, j := range s.jobs {
			if j.Auto && j.Kind == kind && j.Person == person && j.Week == week {
				return true
			}
		}
		return false
	}
	wid := weekID(u)
	if d := u.Weekday(); (d == time.Friday && u.Hour() >= 18) || d == time.Saturday || d == time.Sunday {
		for _, p := range s.st.people {
			if len(s.st.week(p.ID, wid).Tasks) > 0 && !has("summary", p.ID, wid) {
				s.addJob(&Job{Kind: "summary", Person: p.ID, Week: wid, Auto: true}, now)
			}
		}
	}
	if u.Weekday() == time.Monday && u.Hour() >= 6 {
		last := weekID(u.AddDate(0, 0, -7))
		any := false
		for _, p := range s.st.people {
			any = any || len(s.st.week(p.ID, last).Tasks) > 0
		}
		if any && !has("lessons", "", last) {
			s.addJob(&Job{Kind: "lessons", Week: last, Auto: true}, now)
		}
	}
}

// --- What Claude is given ---------------------------------------------------

type qMission struct {
	Mission  string `json:"mission"`
	Name     string `json:"name"`
	Event    string `json:"event,omitempty"`
	Day      string `json:"planned_day"`
	Given    int    `json:"given_min"`
	Spent    int    `json:"spent_min"`
	State    string `json:"state"`
	Note     string `json:"marks_note,omitempty"`
	DoneNote string `json:"workers_note,omitempty"`
	DoneLink string `json:"workers_link,omitempty"`
}

type qWeek struct {
	Person   string     `json:"person"`
	Week     string     `json:"week"`
	Hours    float64    `json:"hours_per_day"`
	ByDay    []string   `json:"logged_per_day"`
	Missions []qMission `json:"missions"`
	Log      []string   `json:"log_uk_time"`
}

func (s *Server) qWeek(p Person, wid string, now time.Time) qWeek {
	mon, _ := mondayOf(wid)
	w := s.st.week(p.ID, wid)
	out := qWeek{Person: p.Name, Week: weekLabel(mon), Hours: s.st.perDay(p.ID, wid)}
	for _, t := range w.Tasks {
		state := t.State()
		if state == "" {
			state = "not started"
		}
		out.Missions = append(out.Missions, qMission{t.Mission, t.Name, t.EventName, dayNames[t.Day], t.Minutes, int(t.Spent(now).Minutes()), state, t.Note, t.DoneNote, t.DoneLink})
	}
	// Time on the clock per calendar day (UK time), next to the day's hours:
	// a short day made up on another one shows here.
	var perDay [7]time.Duration
	for _, t := range w.Tasks {
		for _, sg := range t.Segs {
			to := sg.To
			if to.IsZero() {
				to = now
			}
			if d := int(dateOf(sg.From, s.tz).Sub(mon).Hours() / 24); d >= 0 && d < 7 && to.After(sg.From) {
				perDay[d] += to.Sub(sg.From)
			}
		}
	}
	for d, name := range []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"} {
		if d < 5 || perDay[d] > 0 {
			out.ByDay = append(out.ByDay, name+": "+fmtMin(int(perDay[d].Minutes())))
		}
	}
	from := time.Date(mon.Year(), mon.Month(), mon.Day(), 0, 0, 0, 0, s.tz)
	for _, e := range s.st.readLog(p.ID, from, from.AddDate(0, 0, 7)) {
		out.Log = append(out.Log, e.T.In(s.tz).Format("Mon 15:04")+" "+entryText(e))
	}
	return out
}

type qCandidate struct {
	Mission string `json:"mission"`
	Event   string `json:"event,omitempty"`
	Name    string `json:"name"`
	For     string `json:"for,omitempty"`
	Status  string `json:"status,omitempty"`
	Window  string `json:"window,omitempty"`
	Minutes int    `json:"default_min"`
	Repeat  string `json:"repeat,omitempty"`
}

func (s *Server) candidates(mon, now time.Time) []qCandidate {
	today := dateOf(now, s.tz)
	var out []qCandidate
	for _, m := range s.missions() {
		if m.generic() {
			out = append(out, qCandidate{Mission: m.ID, Name: m.Name, Minutes: m.Minutes, Repeat: m.Repeat})
		}
	}
	for _, ev := range s.events.Get(now) {
		for _, m := range s.missions() {
			if !m.fits(ev.Brand) {
				continue
			}
			st := m.status(ev, mon, today)
			if st != "due" && st != "late" {
				continue
			}
			if m.Repeat == "" && m.Set != "custom" {
				if who, _ := s.doneBy(m.ID, ev.ID); who != "" {
					continue
				}
			}
			from, to := m.window(ev)
			out = append(out, qCandidate{m.ID, ev.ID, m.Name, ev.Name + " on " + ev.Day, st, from.Format(dayFmt) + " to " + to.Format(dayFmt), m.Minutes, m.Repeat})
		}
	}
	return out
}

func (s *Server) jobData(j *Job, now time.Time) map[string]any {
	d := map[string]any{"id": j.ID, "kind": j.Kind}
	p, _ := s.st.person(j.Person)
	switch j.Kind {
	case "summary":
		d["week"] = s.qWeek(p, j.Week, now)
	case "lessons":
		var weeks []qWeek
		for _, q := range s.st.people {
			if len(s.st.week(q.ID, j.Week).Tasks) > 0 {
				weeks = append(weeks, s.qWeek(q, j.Week, now))
			}
		}
		d["weeks"], d["lessons_so_far"] = weeks, s.lessons.Lines
	case "ask":
		d["question"] = j.Question
		mon, _ := mondayOf(j.Week)
		var weeks []qWeek
		for i := 0; i < 4; i++ {
			wid := weekID(mon.AddDate(0, 0, -7*i))
			if i == 0 || len(s.st.week(p.ID, wid).Tasks) > 0 {
				weeks = append(weeks, s.qWeek(p, wid, now))
			}
		}
		d["weeks_newest_first"] = weeks
	case "autofill":
		mon, _ := mondayOf(j.Week)
		w := s.st.week(p.ID, j.Week)
		type day struct {
			Day     int      `json:"day"`
			Name    string   `json:"name"`
			Date    string   `json:"date"`
			Hours   float64  `json:"hours"`
			Closed  bool     `json:"closed"`
			Planned []string `json:"already_planned,omitempty"`
		}
		var days []day
		for i := 0; i < 5; i++ {
			dd := day{i, dayNames[i], mon.AddDate(0, 0, i).Format(dayFmt), s.st.perDay(p.ID, j.Week), s.frozen(p, w, i, now), nil}
			for _, t := range w.Tasks {
				if t.Day == i {
					dd.Planned = append(dd.Planned, fmt.Sprintf("%s (%d min)", taskName(t), t.Minutes))
				}
			}
			days = append(days, dd)
		}
		others := map[string][]string{}
		for _, q := range s.st.people {
			if q.ID == p.ID {
				continue
			}
			for _, t := range s.st.week(q.ID, j.Week).Tasks {
				others[q.Name] = append(others[q.Name], dayNames[t.Day]+": "+taskName(t))
			}
		}
		d["person"], d["days"], d["candidates"], d["other_peoples_week"], d["lessons"] = p.Name, days, s.candidates(mon, now), others, s.lessons.Lines
	}
	return d
}

// Instructions is what the routine is told with every queue.
const Instructions = `You are reading the work logs of a small remote team for Mark, who runs the conferences SREday, LLMday, PLATFORMday, the Prompt Engineering Conference (PEC) and Conf42. Mark plans each person's week as missions with a time budget; the person clicks start, pause and finish on their own page and every click is logged. All times are UK time. Mark never opens their pages: he reads your answers. Write plainly and briefly, facts from the data only. Never guess what someone did outside the log, never judge character: describe what the log shows and let Mark decide.

Answer every job in "jobs" by its id, in one POST to /api/answers:
{"model": "<your model>", "answers": [ ... one object per job ... ]}

KIND "summary" -> {"id": "...", "summary": "<text>"}
The text, in this order, plain text with line breaks, no markdown:
1. One line per stretch of work, grouped by day: "Mon 13:00-13:30 Anya did Speaker outreach (SREday London)". Merge start/pause pairs of the same mission that are minutes apart.
2. "Given vs spent": one line per mission: given, spent, finished or not, and the worker's own note.
3. "Worth a look": only what the log really shows, e.g. a clock left running while the page was closed, one unbroken run of many hours, work logged at odd hours, missions never started, a finish seconds after a start, several wrong PINs. Always compare "logged_per_day" with "hours_per_day": when a short or empty day is made up on another day, say it in one plain line, e.g. "Anya skipped working on Monday, to double hours on Tuesday" or "Anya did 1 h less on Wednesday and 1 h more on Thursday"; when the hours are simply missing, say that instead. If nothing stands out, say so in one line.
Keep it under 350 words.

KIND "lessons" -> {"id": "...", "lessons": ["..."], "minutes": {"<mission id>": 90}}
You get last week for everyone plus the lessons so far. Return the full new list of standing lessons (max 25 short lines, drop the ones that no longer hold): which missions take longer or shorter than given and by how much, what slipped, who is quick or reliable at what, what should be planned earlier. "minutes" holds a better default time for a mission only when at least two finished runs agree; multiples of 30, between 30 and 480.

KIND "autofill" -> {"id": "...", "tasks": [{"mission": "<mission id>", "event": "<event id or empty>", "day": 0, "minutes": 60, "note": "<optional short note>"}]}
Fill this person's open days (day 0 = Monday ... 4 = Friday) from "candidates" only. Never touch a day marked closed. Keep each day at or under its hours, counting what is already planned. Order of importance: late missions, then missions whose window closes soonest, then the nearest events, then repeating missions, then generic ones. Don't give this person a once-only mission that is already on someone else's week. Spread daily missions over the days. Every mission starts at 30 minutes; give more only where the lessons or the mission itself clearly call for it (multiples of 30). Use the lessons for who suits what. Leave a day partly empty rather than pad it.

KIND "ask" -> {"id": "...", "answer": "<text>"}
Answer Mark's question about this person from the weeks given, in a few lines, with the times that back it. If the log can't answer it, say so.`

// handleQueue: GET /api/queue.
func (s *Server) handleQueue(rw http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.dueJobs(now)
	jobs := []map[string]any{}
	remaining := 0
	for _, j := range s.jobs {
		if !j.pending(now) {
			continue
		}
		if len(jobs) >= 6 {
			remaining++
			continue
		}
		jobs = append(jobs, s.jobData(j, now))
	}
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(map[string]any{"today": now.In(s.tz).Format("Monday 2 January 2006, 15:04") + " UK time", "instructions": Instructions, "jobs": jobs, "remaining": remaining})
}

type answer struct {
	ID      string         `json:"id"`
	Summary string         `json:"summary"`
	Lessons []string       `json:"lessons"`
	Minutes map[string]int `json:"minutes"`
	Tasks   []struct {
		Mission string `json:"mission"`
		Event   string `json:"event"`
		Day     int    `json:"day"`
		Minutes int    `json:"minutes"`
		Note    string `json:"note"`
	} `json:"tasks"`
	Answer string `json:"answer"`
}

// handleAnswers: POST /api/answers.
func (s *Server) handleAnswers(rw http.ResponseWriter, r *http.Request) {
	var in struct {
		Model   string   `json:"model"`
		Answers []answer `json:"answers"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&in); err != nil {
		http.Error(rw, "Bad JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	taken := 0
	for _, a := range in.Answers {
		var j *Job
		for _, o := range s.jobs {
			if o.ID == a.ID && o.Done.IsZero() {
				j = o
			}
		}
		if j == nil {
			continue
		}
		taken++
		j.Done = now
		p, _ := s.st.person(j.Person)
		switch j.Kind {
		case "summary":
			dir := filepath.Join(s.dir, "summaries", p.ID)
			os.MkdirAll(dir, 0o700)
			writeJSON(filepath.Join(dir, j.Week+".json"), Summary{Text: strings.TrimSpace(a.Summary), At: now, Model: in.Model})
			j.Answer = "written"
		case "lessons":
			if len(a.Lessons) > 25 {
				a.Lessons = a.Lessons[:25]
			}
			min := s.lessons.Minutes
			if min == nil {
				min = map[string]int{}
			}
			for k, v := range a.Minutes {
				if _, ok := s.mission(k); ok && v >= 30 && v <= 480 {
					min[k] = v / 30 * 30
				}
			}
			s.lessons = Lessons{Lines: a.Lessons, Minutes: min, At: now}
			writeJSON(filepath.Join(s.dir, "lessons.json"), s.lessons)
			j.Answer = fmt.Sprintf("%d lessons kept", len(a.Lessons))
		case "ask":
			j.Answer = strings.TrimSpace(a.Answer)
		case "autofill":
			w := s.st.week(p.ID, j.Week)
			n := 0
			for _, at := range a.Tasks {
				if at.Day < 0 || at.Day > 4 || s.frozen(p, w, at.Day, now) {
					continue
				}
				t, bad := s.newTask(at.Mission, at.Event, now)
				if bad != "" {
					continue
				}
				if m := at.Minutes / 30 * 30; m >= 30 && m <= 600 {
					t.Minutes = m
				}
				t.Day, t.Note = at.Day, strings.TrimSpace(at.Note)
				at, length, ok := w.fit(t.Day, 0, t.Minutes, nil, s.st.dayCap(p.ID, j.Week))
				if !ok {
					continue // the day is full
				}
				t.Start, t.Minutes = at, length
				if len(t.Note) > 400 {
					t.Note = t.Note[:400]
				}
				w.Tasks = append(w.Tasks, t)
				n++
			}
			if n > 0 {
				s.st.saveWeek(w)
			}
			j.Answer = fmt.Sprintf("%d missions drafted", n)
		}
	}
	s.saveJobs()
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(map[string]any{"taken": taken})
}

// handleJobs: GET /admin/jobs?p=, how many jobs Claude still owes (the
// page looks every few seconds while there are some).
func (s *Server) handleJobs(rw http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	n := 0
	for _, j := range s.jobs {
		if j.fresh(now) && (j.Person == r.FormValue("p") || j.Kind == "lessons") {
			n++
		}
	}
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(map[string]int{"pending": n})
}
