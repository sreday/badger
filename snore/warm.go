package snore

import (
	"context"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
)

// The Warmed up list: companies where someone on the team already has a
// contact, warmest first, with who should follow up.

// warmMax: rows on the page.
const warmMax = 400

type warmRow struct {
	Key, Company, Domain string
	Warmth
	Label string // what the level means, in words
	Stale bool
}

var levelWords = map[int]string{
	7: "Sponsor or host", 6: "In touch with the organizer", 5: "Spoke at our events", 4: "Replied to the team",
	3: "Came to an event", 2: "Connected or registered", 1: "Invited only",
}

// syncTouches reads the outreach log again. Called without the lock.
func (s *Server) syncTouches() {
	if s.cfg.HumansDB == "" || s.readTouches == nil {
		return
	}
	d, err := s.readTouches(context.Background(), s.cfg.HumansDB)
	if err != nil {
		log.Printf("Snore: outreach log: %v", strings.ReplaceAll(err.Error(), s.cfg.HumansDB, "<db>"))
		return
	}
	touches, names := buildTouches(d)
	s.mu.Lock()
	s.touches, s.companyNames, s.touchesAt = touches, names, s.now()
	s.mu.Unlock()
	log.Printf("Snore: outreach log read, %d touch(es) at %d compan(ies)", len(touches), len(names))
}

// warmRows: every company where a teammate holds a contact, at level 2 or
// more. Our sponsor history is added, so a sponsor shows as one.
func (s *Server) warmRows(now time.Time) []warmRow {
	s.mu.Lock()
	touches, names := s.touches, s.companyNames
	s.mu.Unlock()
	by := map[string][]Touch{}
	mate := map[string]bool{}
	for _, t := range touches {
		by[t.Company] = append(by[t.Company], t)
		if t.By != "" && t.By != Organizer {
			mate[t.Company] = true
		}
	}
	for _, t := range s.fp.SponsorTouches() {
		if mate[t.Company] {
			by[t.Company] = append(by[t.Company], t)
		}
	}
	var rows []warmRow
	for key, ts := range by {
		if !mate[key] {
			continue
		}
		w := warmthOf(ts, now)
		if w.Level < 2 {
			continue
		}
		if len(w.Proof) > 3 {
			w.Proof = w.Proof[:3]
		}
		r := warmRow{Key: key, Company: names[key], Warmth: w, Label: levelWords[w.Level], Stale: w.Stale(now)}
		if !strings.HasPrefix(key, "name:") {
			r.Domain = key
		}
		if r.Company == "" {
			r.Company = strings.TrimPrefix(key, "name:")
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Level != b.Level {
			return a.Level > b.Level
		}
		if a.People != b.People {
			return a.People > b.People
		}
		if !a.Last.At.Equal(b.Last.At) {
			return a.Last.At.After(b.Last.At)
		}
		return a.Key < b.Key
	})
	return rows
}

type warmData struct {
	Rows   []warmRow
	Total  int
	Owners []string
	By     string
	At     time.Time
	Off    bool // no login to the outreach log
}

// handleWarm: GET /warm, the Warmed up list. ?by=<teammate> narrows it.
func (s *Server) handleWarm(w http.ResponseWriter, r *http.Request) {
	rows := s.warmRows(s.now())
	d := warmData{By: r.URL.Query().Get("by"), Off: s.cfg.HumansDB == ""}
	s.mu.Lock()
	d.At = s.touchesAt
	s.mu.Unlock()
	seen := map[string]bool{}
	for _, row := range rows {
		if row.Owner != Organizer && !seen[row.Owner] {
			seen[row.Owner] = true
			d.Owners = append(d.Owners, row.Owner)
		}
		if d.By != "" && row.Owner != d.By {
			continue
		}
		d.Total++
		if len(d.Rows) < warmMax {
			d.Rows = append(d.Rows, row)
		}
	}
	sort.Strings(d.Owners)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "warm.html", d); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
