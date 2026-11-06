package app

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// The check-in log: every badge Badger prints, kept for good, one
// spreadsheet (CSV) per event. Luma's public API can't check guests in, so
// this is the attendance record: who, when, how (a scanned ticket, a name
// search, Print all, a walk-in, a reprint, the test badge), whether it
// printed, and on which laptop. The page sends each print here as it
// happens; prints made offline wait on the laptop and come in when it's
// back online, and an entry sent twice is kept once (by its id).

type checkin struct {
	ID     string `json:"id"`
	At     string `json:"at"`
	Event  string `json:"event"`
	Email  string `json:"email"`
	Name   string `json:"name"`
	Method string `json:"method"`
	Status string `json:"status"`
	Error  string `json:"error"`
	Laptop string `json:"laptop"`
	// A one-off badge (walk-in) isn't on the guest list: what was on it.
	Title    string `json:"title,omitempty"`
	Company  string `json:"company,omitempty"`
	LinkedIn string `json:"linkedin,omitempty"`
}

var checkinHeader = []string{"Time", "Name", "Email", "Job title", "Company", "How", "Result", "Error", "Laptop", "Event", "Event ID", "Entry ID", "LinkedIn"}

// readLog reads an event's log. Files from before the LinkedIn column have
// 12 columns; the entry id is always the 12th.
func readLog(path string) [][]string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	cr := csv.NewReader(f)
	cr.FieldsPerRecord = -1
	rows, _ := cr.ReadAll()
	out := rows[:0]
	for i, r := range rows {
		if i > 0 && len(r) >= 12 {
			out = append(out, append(r, make([]string, len(checkinHeader)-min(len(r), len(checkinHeader)))...)[:len(checkinHeader)])
		}
	}
	return out
}

var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type checkinLog struct {
	dir  string
	mu   sync.Mutex
	seen map[string]map[string]bool // event -> entry ids already in its file
}

func (l *checkinLog) path(event string) string { return filepath.Join(l.dir, event+".csv") }

// ids reads the entry ids already in an event's file, once.
func (l *checkinLog) ids(event string) map[string]bool {
	if ids, ok := l.seen[event]; ok {
		return ids
	}
	ids := map[string]bool{}
	for _, r := range readLog(l.path(event)) {
		ids[r[11]] = true
	}
	l.seen[event] = ids
	return ids
}

// add appends the entries not in the log yet; it returns how many.
func (l *checkinLog) add(entries []checkin, row func(checkin) []string) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(l.dir, 0o755); err != nil {
		return 0, err
	}
	byEvent := map[string][][]string{}
	for _, c := range entries {
		ids := l.ids(c.Event)
		if ids[c.ID] {
			continue
		}
		ids[c.ID] = true
		byEvent[c.Event] = append(byEvent[c.Event], row(c))
	}
	n := 0
	for event, rows := range byEvent {
		_, err := os.Stat(l.path(event))
		fresh := os.IsNotExist(err)
		f, err := os.OpenFile(l.path(event), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return n, err
		}
		w := csv.NewWriter(f)
		if fresh {
			w.Write(checkinHeader)
		}
		w.WriteAll(rows)
		if err := f.Close(); err != nil {
			return n, err
		}
		n += len(rows)
	}
	return n, nil
}

func (s *Server) handleCheckins(w http.ResponseWriter, r *http.Request) {
	s.saveLog(w, r, s.checkins)
}

// handleScans keeps the scans that went wrong (a ticket not on the list,
// another event's, the wrong keyboard layout, not a ticket at all), one
// spreadsheet per event apart from the check-ins: what the desk struggled
// with, for after the event. How is "scan", Result the problem, Error what
// was read.
func (s *Server) handleScans(w http.ResponseWriter, r *http.Request) {
	s.saveLog(w, r, s.scans)
}

func (s *Server) saveLog(w http.ResponseWriter, r *http.Request, log *checkinLog) {
	var entries []checkin
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&entries); err != nil {
		http.Error(w, "Bad check-ins", http.StatusBadRequest)
		return
	}
	ok := entries[:0]
	for _, c := range entries {
		if safeID.MatchString(c.Event) && c.ID != "" && len(c.ID) <= 64 {
			ok = append(ok, c)
		}
	}
	n, err := log.add(ok, s.checkinRow)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]int{"saved": n, "received": len(ok)})
}

// checkinRow is one line of the spreadsheet: the guest's job title and
// company from the guest list, the event's name.
func (s *Server) checkinRow(c checkin) []string {
	s.mu.RLock()
	g := s.guests[c.Event][c.Email]
	ev, hasEv := s.events[c.Event]
	s.mu.RUnlock()
	name := c.Name
	if name == "" {
		name = g.Name
	}
	evName := c.Event
	if hasEv {
		evName = ev.Event.Name
	}
	title, company, linkedin := g.Title, g.Company, g.LinkedIn
	if c.Email == "" {
		title, company, linkedin = c.Title, c.Company, c.LinkedIn
	}
	return []string{c.At, name, c.Email, blankMissing(title), blankMissing(company), c.Method, c.Status, c.Error, c.Laptop, evName, c.Event, c.ID, linkedin}
}

// blankMissing drops Luma's "403 not found" for a field the guest left empty.
func blankMissing(v string) string {
	if strings.EqualFold(strings.TrimSpace(v), "403 not found") {
		return ""
	}
	return v
}

// handleCheckinsCSV downloads an event's check-in log.
func (s *Server) handleCheckinsCSV(w http.ResponseWriter, r *http.Request) {
	s.sendLog(w, r, s.checkins, "check-ins")
}

// handleScansCSV is the scan problems spreadsheet.
func (s *Server) handleScansCSV(w http.ResponseWriter, r *http.Request) {
	s.sendLog(w, r, s.scans, "scan problems")
}

func (s *Server) sendLog(w http.ResponseWriter, r *http.Request, log *checkinLog, what string) {
	event := r.PathValue("eventID")
	if !safeID.MatchString(event) {
		http.NotFound(w, r)
		return
	}
	log.mu.Lock()
	rows := readLog(log.path(event))
	log.mu.Unlock()
	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	cw.Write(checkinHeader)
	cw.WriteAll(rows)
	data := buf.Bytes()
	name := event
	s.mu.RLock()
	ev, ok := s.events[event]
	s.mu.RUnlock()
	if !ok {
		ev, ok = s.lookupEvent(r, event)
	}
	if ok {
		name = ev.Event.Name
	}
	name = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) {
			return '-'
		}
		return r
	}, name)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s - %s.csv"`, name, what))
	w.Header().Set("Cache-Control", "no-store")
	w.Write(data)
}

// handleCheckinsJSON is the log for the guest page: who's printed, when, and
// who was checked in on Luma from Badger. It survives a reload, a restart
// and a second laptop, where the browser's own record does not.
func (s *Server) handleCheckinsJSON(w http.ResponseWriter, r *http.Request) {
	event := r.PathValue("eventID")
	if !safeID.MatchString(event) {
		http.NotFound(w, r)
		return
	}
	s.checkins.mu.Lock()
	rows := readLog(s.checkins.path(event))
	s.checkins.mu.Unlock()
	out := []checkin{}
	for _, c := range rows {
		e := checkin{At: c[0], Name: c[1], Email: c[2], Method: c[5], Status: c[6], Error: c[7], Laptop: c[8], Event: c[10], ID: c[11]}
		if e.Email == "" {
			e.Title, e.Company, e.LinkedIn = c[3], c[4], c[12]
		}
		out = append(out, e)
	}
	writeJSON(w, out)
}
