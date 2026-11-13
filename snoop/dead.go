package snoop

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The Dead end tab (Marek 2026-11-13): "the dead end is a bin where the 100%
// dead threads are manually put inside (with option to bring back)". A call
// or a Luma event dragged onto the Dead end tab leaves its own tab and the
// actions board; "Bring back to Calendly" (or Luma, Recurring, Other) puts
// it back where it was. Nothing is deleted: the trace stays.
//
// And the three kinds that used to hide behind the "Show declined,
// cancelled & empty" switch are tabs of their own now: All (the calls that
// count, the default), Declined / Cancelled, Empty.

var viewNames = map[string]string{"calendly": "Calendly", "luma": "Luma", "recurring": "Recurring", "other": "Other", "dead": "Dead end"}

// kindOf sorts a call into All, declined (declined or cancelled: one tab) or empty.
func kindOf(c Call, now time.Time) string {
	switch {
	case c.Cancelled, (c.Declined || c.OthersDeclined) && len(c.Lines()) == 0:
		return "declined"
	case c.Empty(now):
		return "empty"
	}
	return "all"
}

func validKind(k string) string {
	switch k {
	case "cancelled":
		return "declined" // one tab now: Declined / Cancelled
	case "declined", "empty":
		return k
	}
	return "all"
}

// deadLuma: the Luma events put in the Dead end bin, by their Luma page
// (Luma events aren't calls, so they are remembered apart).
type deadLuma struct {
	mu   sync.Mutex
	path string
	urls map[string]bool
}

func loadDeadLuma(dir string) *deadLuma {
	d := &deadLuma{path: filepath.Join(dir, "dead-luma.json"), urls: map[string]bool{}}
	if data, err := os.ReadFile(d.path); err == nil {
		json.Unmarshal(data, &d.urls)
	}
	return d
}

func (d *deadLuma) has(url string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.urls[url]
}

func (d *deadLuma) set(url string, dead bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if dead {
		d.urls[url] = true
	} else {
		delete(d.urls, url)
	}
	data, _ := json.Marshal(d.urls)
	return writeFileAtomic(d.path, data)
}

// handleDead: POST /admin/call/{id}/dead (dead=1 into the bin, 0 back out).
func (s *Server) handleDead(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.store.Get(id); !ok {
		http.NotFound(w, r)
		return
	}
	dead := r.FormValue("dead") != "0"
	if _, err := s.store.Update(id, func(c *Call) { c.DeadEnd = dead }); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.backToActions(w, r)
}

// handleDeadLuma: POST /admin/luma/dead (url, dead=1|0).
func (s *Server) handleDeadLuma(w http.ResponseWriter, r *http.Request) {
	url := strings.TrimSpace(r.FormValue("url"))
	if url == "" {
		http.Error(w, "Which Luma event?", http.StatusBadRequest)
		return
	}
	if err := s.dead.set(url, r.FormValue("dead") != "0"); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.backToActions(w, r)
}

// deadLumaRows: the Luma events in the bin, for the Dead end tab (latest first).
func (s *Server) deadLumaRows(now time.Time, all []Call, view string) []lumaRow {
	if view != "dead" {
		return nil
	}
	s.lumaMu.Lock()
	events := append([]LumaEvent(nil), s.luma...)
	s.lumaMu.Unlock()
	var out []lumaRow
	for _, e := range events {
		if !s.dead.has(e.URL) {
			continue
		}
		lr := lumaRow{LumaEvent: e, Rel: relDay(e.Start, now, s.tz), Base: s.cfg.Base, TZ: s.tz, Dead: true}
		if c, ok := lumaTrace(e, all); ok {
			cr := s.row(c, now)
			lr.Trace = &cr
		}
		out = append([]lumaRow{lr}, out...)
	}
	return out
}
