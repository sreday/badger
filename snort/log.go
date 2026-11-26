package snort

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// logKeep is how many entries the log keeps; older ones fall off.
const logKeep = 500

// Entry is one thing that happened in Snort. It holds Snort's own words and
// public facts only (event names, file names of uploads, commits), never
// anything read from a sheet.
type Entry struct {
	At     time.Time
	Who    string // the admin login that did it; "" = Snort itself
	What   string // push, headshots, refresh, build
	Event  string // event name, for push and build
	Result string // pushed, same, refused, added, sorted, live, failed, read
	Note   string
	Repo   string
	Commit string
}

func (s *Server) logPath() string { return filepath.Join(s.dir, "log.json") }

// record adds an entry to the log and saves it.
func (s *Server) record(e Entry) {
	e.At = s.now()
	s.logMu.Lock()
	defer s.logMu.Unlock()
	s.log = append(s.log, e)
	if len(s.log) > logKeep {
		s.log = append([]Entry(nil), s.log[len(s.log)-logKeep:]...)
	}
	writeJSON(s.logPath(), s.log)
}

// recent returns the last n entries, newest first.
func (s *Server) recent(n int) []Entry {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	out := make([]Entry, 0, n)
	for i := len(s.log) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, s.log[i])
	}
	return out
}

// logShow is how many entries the board shows.
const logShow = 60

// who is the admin login of a request.
func who(r *http.Request) string {
	if u, _, ok := r.BasicAuth(); ok && u != "" {
		return u
	}
	return "admin"
}

// uploadEntry says how a drop of headshots was sorted.
func uploadEntry(by string, out []*shot) Entry {
	var ok, bad []string
	for _, sh := range out {
		if sh.Error != "" {
			bad = append(bad, sh.Upload+" ("+sh.Error+")")
		} else {
			ok = append(ok, sh.Upload)
		}
	}
	e := Entry{Who: by, What: "headshots", Result: "sorted"}
	var parts []string
	if len(ok) > 0 {
		parts = append(parts, fmt.Sprintf("%d sorted: %s", len(ok), strings.Join(ok, ", ")))
	}
	if len(bad) > 0 {
		if len(ok) == 0 {
			e.Result = "refused"
		}
		parts = append(parts, fmt.Sprintf("%d turned away: %s", len(bad), strings.Join(bad, ", ")))
	}
	e.Note = strings.Join(parts, "; ")
	return e
}
