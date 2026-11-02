package app

import (
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
}

var checkinHeader = []string{"Time", "Name", "Email", "Job title", "Company", "How", "Result", "Error", "Laptop", "Event", "Event ID", "Entry ID"}

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
	if f, err := os.Open(l.path(event)); err == nil {
		rows, _ := csv.NewReader(f).ReadAll()
		f.Close()
		for _, r := range rows {
			if len(r) == len(checkinHeader) {
				ids[r[len(r)-1]] = true
			}
		}
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
	n, err := s.checkins.add(ok, s.checkinRow)
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
	return []string{c.At, name, c.Email, blankMissing(g.Title), blankMissing(g.Company), c.Method, c.Status, c.Error, c.Laptop, evName, c.Event, c.ID}
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
	event := r.PathValue("eventID")
	if !safeID.MatchString(event) {
		http.NotFound(w, r)
		return
	}
	s.checkins.mu.Lock()
	data, err := os.ReadFile(s.checkins.path(event))
	s.checkins.mu.Unlock()
	if os.IsNotExist(err) {
		data, err = []byte(strings.Join(checkinHeader, ",")+"\n"), nil
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
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
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s - check-ins.csv"`, name))
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
	f, err := os.Open(s.checkins.path(event))
	var rows [][]string
	if err == nil {
		rows, _ = csv.NewReader(f).ReadAll()
		f.Close()
	}
	s.checkins.mu.Unlock()
	out := []checkin{}
	for i, c := range rows {
		if i == 0 || len(c) != len(checkinHeader) {
			continue
		}
		out = append(out, checkin{At: c[0], Name: c[1], Email: c[2], Method: c[5], Status: c[6], Error: c[7], Laptop: c[8], Event: c[10], ID: c[11]})
	}
	writeJSON(w, out)
}
