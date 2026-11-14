package snitch

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"time"
)

const (
	cooldown    = 5 * time.Second  // between two clicks on start / pause / finish
	awayAfter   = 10 * time.Minute // no sign of the page for this long = closed
	pinTries    = 5
	pinLockFor  = 10 * time.Minute
	cookieDays  = 30
	openLogEach = 10 * time.Minute
)

type pinFail struct {
	n     int
	until time.Time
}

type meDay struct {
	Name  string
	Date  string
	Today bool
	Past  bool
	Tasks []meTask
}

type meTask struct {
	taskView
	CanStart  bool
	CanPause  bool
	CanFinish bool
	Why       string // why Start is off
}

type meView struct {
	Base    string
	Token   string
	Name    string
	Label   string
	Days    []meDay
	Empty   bool
	Given   string
	Spent   string
	Running bool
	Error   string
	Locked  bool // the PIN page: too many wrong tries
}

func (s *Server) cookieName(p Person) string { return "sn_" + p.Token[:8] }

func (s *Server) cookieValue(p Person) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(p.Token + ":" + p.PIN))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) signedIn(p Person, r *http.Request) bool {
	c, err := r.Cookie(s.cookieName(p))
	return err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.cookieValue(p))) == 1
}

// touch notes a sign of the worker's page. A long silence while a clock
// ran is written down as the page closed and open again.
func (s *Server) touch(p Person, w *Week, now time.Time) {
	last := s.lastSeen[p.ID]
	s.lastSeen[p.ID] = now
	if last.IsZero() || now.Sub(last) <= awayAfter {
		return
	}
	for _, t := range w.Tasks {
		if t.Running() && t.Segs[len(t.Segs)-1].From.Before(last) {
			s.st.appendLog(p.ID, Entry{T: last, Kind: "away", Task: t.ID, Name: t.Name})
			s.st.appendLog(p.ID, Entry{T: now, Kind: "back", Task: t.ID, Name: t.Name})
		}
	}
}

// todayIndex is the person's weekday, 0 = Monday; -1 at the weekend.
func todayIndex(now time.Time, loc *time.Location) int {
	if d := int(now.In(loc).Weekday()) - 1; d >= 0 && d <= 4 {
		return d
	}
	return -1
}

// handleMe: GET /{token}, the worker's own week.
func (s *Server) handleMe(rw http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.st.byToken(r.PathValue("token"))
	if !ok {
		http.NotFound(rw, r)
		return
	}
	now := s.now()
	if !s.signedIn(p, r) {
		f := s.pinFails[p.ID]
		s.render(rw, "pin.html", "pin.html", meView{Base: s.cfg.Base, Token: p.Token, Name: p.Name, Error: r.FormValue("e"), Locked: f != nil && now.Before(f.until)})
		return
	}
	loc := s.personTZ(p)
	wid := weekID(now.In(loc))
	mon, _ := mondayOf(wid)
	w := s.st.week(p.ID, wid)
	if last := s.lastSeen[p.ID]; last.IsZero() || now.Sub(last) > openLogEach {
		s.st.appendLog(p.ID, Entry{T: now, Kind: "open"})
	}
	s.touch(p, w, now)

	v := meView{Base: s.cfg.Base, Token: p.Token, Name: p.Name, Label: weekLabel(mon), Error: r.FormValue("e")}
	today := todayIndex(now, loc)
	var running *Task
	for _, t := range w.Tasks {
		if t.Running() {
			running = t
		}
	}
	given, spent := 0, 0
	for d := 0; d < 5; d++ {
		day := meDay{Name: dayNames[d], Date: mon.AddDate(0, 0, d).Format("2 Jan"), Today: d == today, Past: today == -1 || d < today}
		for _, t := range w.Tasks {
			if t.Day != d {
				continue
			}
			mt := meTask{taskView: taskViewOf(t, now)}
			given += t.Minutes
			spent += int(t.Spent(now).Minutes())
			switch {
			case t.Done:
			case t.Running():
				mt.CanPause, mt.CanFinish = true, true
			default:
				mt.CanFinish = t.Started() && d == today
				switch {
				case d != today:
					mt.Why = "Only today's missions can be started"
				case running != nil:
					mt.Why = "Pause \"" + running.Name + "\" first"
				default:
					mt.CanStart = true
				}
			}
			day.Tasks = append(day.Tasks, mt)
		}
		if len(day.Tasks) > 0 {
			v.Days = append(v.Days, day)
		}
	}
	v.Empty = len(v.Days) == 0
	v.Given, v.Spent, v.Running = fmtMin(given), fmtMin(spent), running != nil
	s.render(rw, "me.html", "me.html", v)
}

// handleMeAct: POST /{token}/{act}: pin, ping, start, pause, finish.
func (s *Server) handleMeAct(rw http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.st.byToken(r.PathValue("token"))
	if !ok {
		http.NotFound(rw, r)
		return
	}
	now := s.now()
	home := s.cfg.Base + "/" + p.Token
	act := r.PathValue("act")
	if act == "pin" {
		f := s.pinFails[p.ID]
		if f == nil {
			f = &pinFail{}
			s.pinFails[p.ID] = f
		}
		if now.Before(f.until) {
			http.Redirect(rw, r, home, http.StatusSeeOther)
			return
		}
		if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(r.FormValue("pin"))), []byte(p.PIN)) != 1 {
			f.n++
			if f.n >= pinTries {
				f.n, f.until = 0, now.Add(pinLockFor)
			}
			s.st.appendLog(p.ID, Entry{T: now, Kind: "pin_fail"})
			http.Redirect(rw, r, home+"?e=Wrong+PIN", http.StatusSeeOther)
			return
		}
		delete(s.pinFails, p.ID)
		s.st.appendLog(p.ID, Entry{T: now, Kind: "pin_ok"})
		http.SetCookie(rw, &http.Cookie{Name: s.cookieName(p), Value: s.cookieValue(p), Path: home, MaxAge: cookieDays * 86400,
			HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"})
		http.Redirect(rw, r, home, http.StatusSeeOther)
		return
	}
	if !s.signedIn(p, r) {
		http.Error(rw, "Sign in again", http.StatusUnauthorized)
		return
	}
	loc := s.personTZ(p)
	w := s.st.week(p.ID, weekID(now.In(loc)))
	s.touch(p, w, now)
	if act == "ping" {
		rw.WriteHeader(http.StatusNoContent)
		return
	}
	fail := func(code int, msg string) { http.Error(rw, msg, code) }
	if act != "start" && act != "pause" && act != "finish" {
		fail(http.StatusNotFound, "Unknown action")
		return
	}
	if last := s.lastClick[p.ID]; now.Sub(last) < cooldown {
		fail(http.StatusTooManyRequests, "Wait 5 seconds between clicks")
		return
	}
	t := w.task(r.FormValue("task"))
	if t == nil {
		fail(http.StatusNotFound, "That mission is not on your week")
		return
	}
	if t.Done {
		fail(http.StatusConflict, "That mission is finished")
		return
	}
	switch act {
	case "start":
		if t.Running() {
			fail(http.StatusConflict, "Already running")
			return
		}
		if t.Day != todayIndex(now, loc) {
			fail(http.StatusConflict, "Only today's missions can be started")
			return
		}
		for _, o := range w.Tasks {
			if o.Running() {
				fail(http.StatusConflict, "Pause \""+o.Name+"\" first")
				return
			}
		}
		kind := "start"
		if t.Started() {
			kind = "resume"
		}
		t.Segs = append(t.Segs, Seg{From: now})
		s.st.appendLog(p.ID, Entry{T: now, Kind: kind, Task: t.ID, Name: taskName(t)})
	case "pause":
		if !t.Running() {
			fail(http.StatusConflict, "That mission is not running")
			return
		}
		t.Segs[len(t.Segs)-1].To = now
		s.st.appendLog(p.ID, Entry{T: now, Kind: "pause", Task: t.ID, Name: taskName(t)})
	case "finish":
		if !t.Started() {
			fail(http.StatusConflict, "Start the mission first")
			return
		}
		if !t.Running() && t.Day != todayIndex(now, loc) {
			fail(http.StatusConflict, "Only today's missions can be finished")
			return
		}
		note, link := strings.TrimSpace(r.FormValue("note")), strings.TrimSpace(r.FormValue("link"))
		if len(note) < 3 {
			fail(http.StatusBadRequest, "Write a short note on what was done")
			return
		}
		if link != "" && !strings.HasPrefix(link, "https://") && !strings.HasPrefix(link, "http://") {
			fail(http.StatusBadRequest, "The link should start with https://")
			return
		}
		if len(note) > 1000 {
			note = note[:1000]
		}
		if t.Running() {
			t.Segs[len(t.Segs)-1].To = now
		}
		t.Done, t.DoneAt, t.DoneNote, t.DoneLink = true, now, note, link
		s.st.appendLog(p.ID, Entry{T: now, Kind: "finish", Task: t.ID, Name: taskName(t), Note: note, Link: link})
	}
	s.lastClick[p.ID] = now
	if err := s.st.saveWeek(w); err != nil {
		fail(http.StatusInternalServerError, "Couldn't save, try again")
		return
	}
	rw.WriteHeader(http.StatusNoContent)
}

func taskName(t *Task) string {
	if t.EventName != "" {
		return t.Name + " (" + t.EventName + ")"
	}
	return t.Name
}

// closeOldClocks stops clocks still running after their week ended: at the
// last sign of the page when there was one, at the week's end otherwise.
func (s *Server) closeOldClocks(now time.Time) {
	for _, w := range s.st.weeks {
		p, ok := s.st.person(w.Person)
		if !ok {
			continue
		}
		loc := s.personTZ(p)
		if w.ID == weekID(now.In(loc)) {
			continue
		}
		mon, ok := mondayOf(w.ID)
		if !ok || !mon.Before(dateOf(now, loc)) {
			continue
		}
		end := time.Date(mon.Year(), mon.Month(), mon.Day()+7, 0, 0, 0, 0, loc)
		changed := false
		for _, t := range w.Tasks {
			if !t.Running() {
				continue
			}
			seg := &t.Segs[len(t.Segs)-1]
			to := end
			if last := s.lastSeen[p.ID]; last.After(seg.From) && last.Before(end) {
				to = last
			}
			seg.To = to
			s.st.appendLog(p.ID, Entry{T: now, Kind: "auto_pause", Task: t.ID, Name: taskName(t), Note: "the week ended with the clock running, counted until " + to.In(s.tz).Format("Mon 15:04")})
			changed = true
		}
		if changed {
			s.st.saveWeek(w)
		}
	}
}
