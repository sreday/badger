package snore

import (
	"net/http"
	"sort"
	"strings"
	"time"
)

// In the room: the companies whose people register for, come to or speak
// at our events. A company counts when several of its people do, or when
// one of them is an inside contact (marketing, events, developer
// relations, partnerships, a founder).

const (
	roomWindow = 24 * month // registrations older than this say little
	roomMax    = 400
	roomCrowd  = 3 // this many people make a company worth a look without an inside contact
)

type roomPerson struct {
	Name, Title, Profile, Role string
	Last                       time.Time
	Came, Spoke                bool
}

type roomRow struct {
	Key, Company, Domain string
	People               int // distinct people
	Came                 int // of them, came at least once
	Events               int // distinct events
	Last                 time.Time
	Inside               []roomPerson // the inside contacts, newest first
	Sponsor              bool         // sponsored us in the last year
	Level                int
}

// roomRows groups the event touches by company.
func (s *Server) roomRows(now time.Time) []roomRow {
	s.mu.Lock()
	touches, names := s.touches, s.companyNames
	s.mu.Unlock()
	sponsor := map[string]bool{}
	for _, t := range s.fp.SponsorTouches() {
		if now.Sub(t.At) < currentWithin {
			sponsor[t.Company] = true
		}
	}
	type acc struct {
		people map[string]*roomPerson
		events map[string]bool
		last   time.Time
		all    []Touch
	}
	by := map[string]*acc{}
	for _, t := range touches {
		a := by[t.Company]
		if a == nil {
			a = &acc{people: map[string]*roomPerson{}, events: map[string]bool{}}
			by[t.Company] = a
		}
		a.all = append(a.all, t)
		if t.Kind != TouchRegistered && t.Kind != TouchCheckedIn && t.Kind != TouchSpoke {
			continue
		}
		if now.Sub(t.At) > roomWindow {
			continue
		}
		k := personKey(t)
		p := a.people[k]
		if p == nil {
			p = &roomPerson{Name: t.Person, Title: t.Title, Profile: t.Profile, Role: roleOf(t.Title)}
			a.people[k] = p
		}
		if t.At.After(p.Last) {
			p.Last = t.At
		}
		p.Came = p.Came || t.Kind == TouchCheckedIn
		p.Spoke = p.Spoke || t.Kind == TouchSpoke
		if t.Event != "" {
			a.events[t.Event] = true
		}
		if t.At.After(a.last) {
			a.last = t.At
		}
	}
	var rows []roomRow
	for key, a := range by {
		if len(a.people) == 0 {
			continue
		}
		r := roomRow{Key: key, Company: names[key], People: len(a.people), Events: len(a.events), Last: a.last, Sponsor: sponsor[key]}
		for _, p := range a.people {
			if p.Came {
				r.Came++
			}
			if insideRole(p.Role) {
				r.Inside = append(r.Inside, *p)
			}
		}
		if len(r.Inside) == 0 && r.People < roomCrowd {
			continue
		}
		sort.Slice(r.Inside, func(i, j int) bool {
			if !r.Inside[i].Last.Equal(r.Inside[j].Last) {
				return r.Inside[i].Last.After(r.Inside[j].Last)
			}
			return r.Inside[i].Name < r.Inside[j].Name
		})
		if !strings.HasPrefix(key, "name:") {
			r.Domain = key
		}
		if r.Company == "" {
			r.Company = strings.TrimPrefix(key, "name:")
		}
		r.Level = warmthOf(a.all, now).Level
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if (len(a.Inside) > 0) != (len(b.Inside) > 0) {
			return len(a.Inside) > 0
		}
		if a.People != b.People {
			return a.People > b.People
		}
		if !a.Last.Equal(b.Last) {
			return a.Last.After(b.Last)
		}
		return a.Key < b.Key
	})
	return rows
}

type roomData struct {
	Rows   []roomRow
	Total  int
	Inside int // companies with an inside contact
	Off    bool
}

// handleRoom: GET /room.
func (s *Server) handleRoom(w http.ResponseWriter, r *http.Request) {
	rows := s.roomRows(s.now())
	d := roomData{Total: len(rows), Off: s.cfg.HumansDB == ""}
	for _, row := range rows {
		if len(row.Inside) > 0 {
			d.Inside++
		}
	}
	if len(rows) > roomMax {
		rows = rows[:roomMax]
	}
	for i := range rows {
		if len(rows[i].Inside) > 4 {
			rows[i].Inside = rows[i].Inside[:4]
		}
	}
	d.Rows = rows
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "room.html", d); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
