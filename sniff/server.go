// Package sniff (Sniff by Badger) is live questions and feedback for every
// session of an event: one QR per event, the audience asks and votes on
// their phones, and the speaker follows along on their own page. Snout
// hosts it on scan.sreday.com (Register), the way it hosts Snoop at /snoop.
package sniff

import (
	"embed"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/sreday/badger/event"
	"github.com/sreday/badger/luma"
	"github.com/sreday/badger/ui"
	"github.com/sreday/badger/web"
)

//go:embed templates/*.html
var templateFS embed.FS

// Config holds Sniff's configuration. Snout fills it in from its own.
type Config struct {
	DataDir    string // Sniff's files live here, next to Snout's (sniff_sessions.json, events/<event>/sniff.jsonl, faces/)
	LumaAPIKey string
	PublicURL  string // e.g. https://scan.sreday.com; empty: the request's host

	// Admin guards the organizers' pages (/sniff, /admin/sniff/...):
	// Snout's admin login.
	Admin func(http.HandlerFunc) http.HandlerFunc
	// Now is the clock (Snout's, so tests move both); nil means time.Now.
	Now func() time.Time
}

// GuestList is an event's approved Luma guests, for matching typed names.
// Name indexes are cached per GuestList value, so the host should hand out
// the same comparable value (a pointer) until the list changes.
type GuestList interface {
	Guests() []luma.Guest
}

// Host is what Sniff borrows from the server that hosts it.
type Host interface {
	// EventGuests returns the event's guest list, from the host's cache.
	EventGuests(eventID string) GuestList
}

// Server is Sniff's state: its store, pages and rate limits.
type Server struct {
	cfg   Config
	host  Host
	tmpls map[string]*template.Template
	now   func() time.Time // the clock; tests move it

	sniff      *SniffStore
	sniffLimit *limiter
	strikes    *strikes // filtered questions in a row, per phone (memory only)
	asked      *quota   // questions per phone per talk (memory only)
}

// New opens Sniff's store in cfg.DataDir and parses its pages.
func New(cfg Config, host Host) (*Server, error) {
	pages := []string{"sniff_ask.html", "sniff_speaker.html", "sniff_admin.html", "sniff_event.html", "sniff_index.html", "sniff_pin.html"}
	funcs := template.FuncMap{"tool": ui.Tool}
	tmpls := make(map[string]*template.Template, len(pages))
	for _, page := range pages {
		t, err := template.New(page).Funcs(funcs).ParseFS(templateFS, "templates/"+page, "templates/sniff_parts.html")
		if err == nil {
			t, err = ui.Parse(t)
		}
		if err != nil {
			return nil, fmt.Errorf("parsing template %s: %w", page, err)
		}
		tmpls[page] = t
	}
	store, err := OpenSniffStore(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, host: host, tmpls: tmpls, now: cfg.Now, sniff: store, sniffLimit: newLimiter(), strikes: newStrikes(), asked: newQuota()}
	go func() { // test posts last an hour (like test scans in Snout): sweep their files
		for range time.Tick(10 * time.Minute) {
			store.ExpireTestPosts(time.Now())
		}
	}()
	if s.now == nil {
		s.now = time.Now
	}
	if s.cfg.Admin == nil {
		s.cfg.Admin = func(http.HandlerFunc) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) { http.Error(w, "Unauthorized", http.StatusUnauthorized) }
		}
	}
	return s, nil
}

// Register adds Sniff's pages to the host's mux, at the root:
//
//	GET  /sniff                 the team: events, and all questions per event (admin)
//	     /sniff/<secret>        the event: tutorial and every speaker's card (anyone with the link)
//	     /q/<code>              one talk's QR: the audience asks and rates; with the PIN
//	                            the presenter hearts and hides, live, on the same page
//	     /sniff/<secret>/<name> the speaker's desktop page, after the event (PIN)
func (s *Server) Register(mux *http.ServeMux) {
	admin := s.cfg.Admin
	mux.HandleFunc("GET /admin/sniff/{event}", admin(s.handleSniffAdminOld)) // old link: now /sniff/<secret>
	mux.HandleFunc("POST /admin/sniff/{event}/refresh", admin(s.handleSniffAdminRefresh))
	mux.HandleFunc("POST /admin/sniff/{event}/add", admin(s.handleSniffAdminAdd))
	mux.HandleFunc("POST /admin/sniff/{event}/reset/{code}", admin(s.handleSniffAdminReset))
	mux.HandleFunc("POST /admin/sniff/{event}/hide/{code}", admin(s.handleSniffAdminHide))
	mux.HandleFunc("GET /admin/sniff/{event}/feedback.csv", admin(s.handleSniffAdminCSV))
	// The audience (one QR per session) and the speaker's live page.
	mux.HandleFunc("GET /q/{code}", web.Private(s.handleSniffAudience))
	mux.HandleFunc("GET /q/{code}/feed", web.Private(s.handleSniffFeed))
	mux.HandleFunc("POST /q/{code}/ask", web.Private(s.handleSniffAsk))
	mux.HandleFunc("POST /q/{code}/vote", web.Private(s.handleSniffVote))
	mux.HandleFunc("POST /q/{code}/rate", web.Private(s.handleSniffRate))
	mux.HandleFunc("POST /q/{code}/del", web.Private(s.handleSniffDelete))
	mux.HandleFunc("POST /q/{code}/pin", web.Private(s.handleSniffPin))
	mux.HandleFunc("POST /q/{code}/mod", web.Private(s.handleSniffPresenterMod)) // the PIN: hearts and hides only
	mux.HandleFunc("POST /q/{code}/logout", web.Private(s.handleSniffLogout))    // the speaker view's Log out
	mux.HandleFunc("GET /q/{code}/face.jpg", s.handleSniffFace)
	mux.HandleFunc("GET /sniff/{token}", web.Private(s.handleSniffLink))                  // an event secret, or an older speaker link
	mux.HandleFunc("GET /sniff/{secret}/{name}", web.Private(s.handleSniffSpeakerByName)) // the speaker's desktop page
	mux.HandleFunc("GET /sniff/{secret}/{name}/qr.png", web.Private(s.byName(s.handleSniffQR)))
	mux.HandleFunc("GET /sniff/{secret}/{name}/slide.png", web.Private(s.byName(s.handleSniffSlide)))
	// The speaker's page talks to these by its token (the PIN cookie gates them).
	mux.HandleFunc("POST /sniff/{token}/pin", web.Private(s.handleSniffSpeakerPin))
	mux.HandleFunc("GET /sniff/{token}/feed", web.Private(s.handleSniffSpeakerFeed))
	mux.HandleFunc("POST /sniff/{token}/mod", web.Private(s.handleSniffMod))
	mux.HandleFunc("GET /sniff/{token}/qr.png", web.Private(s.byToken(s.handleSniffQR)))
	mux.HandleFunc("GET /sniff/{token}/slide.png", web.Private(s.byToken(s.handleSniffSlide)))
	mux.HandleFunc("GET /sniff/{token}/export.csv", web.Private(s.handleSniffExport))
	mux.HandleFunc("GET /sniff", admin(web.Private(s.handleSniffIndex)))
	mux.HandleFunc("GET /api/sniff/link", web.Private(s.handleSniffLinkAPI)) // the onboarding email asks for an event's link (with the key)
}

func (s *Server) render(w http.ResponseWriter, name string, data map[string]any) {
	w.Header().Set("Cache-Control", "no-store")
	if err := s.tmpls[name].ExecuteTemplate(w, name, data); err != nil {
		log.Printf("Template error: %v", err)
	}
}

func (s *Server) baseURL(r *http.Request) string { return web.BaseURL(s.cfg.PublicURL, r) }

// guestsOf is the list's guests; none without a list.
func guestsOf(ix GuestList) []luma.Guest {
	if ix == nil {
		return nil
	}
	return ix.Guests()
}

type sniffEventRow struct {
	ID, Name, Path string
	Brand          string // sreday, llmday, platformday: the card's accent
	When           string // "Fri 6 Nov 2026", in the event's time zone
	Day, Month     string // the date chip: "6", "Nov"
	Phase, Until   string // test / live / closed, and when that ends
	Sessions       int
	Extra          bool // beyond the first three, shown with "Show all"
}

// handleSniffIndex lists the events in two tabs, like Snout: Upcoming
// (test or live, soonest first; set up from here) and Past (closed events
// that have sessions, most recent first; their questions stay
// downloadable).
func (s *Server) handleSniffIndex(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{}
	events, err := luma.ListEvents(s.cfg.LumaAPIKey)
	if err != nil {
		data["Error"] = "Luma: " + err.Error()
	}
	now := time.Now()
	recent := map[string]bool{}
	for _, ev := range luma.RecentEvents(events) {
		recent[ev.APIId] = true
	}
	type dated struct {
		row   sniffEventRow
		start time.Time
	}
	var up, gone []dated
	for _, ev := range events {
		s.sniff.NoteEventName(ev.APIId, ev.Name)
		start, end := ev.Times()
		phase, until := event.Label(event.Window{Start: start, End: end}, ev.Timezone, now)
		row := sniffEventRow{ID: ev.APIId, Name: ev.Name, Brand: event.Brand(ev.Name), Phase: phase, Until: until, Sessions: s.visibleSessions(ev.APIId)}
		if !start.IsZero() {
			loc, err := time.LoadLocation(ev.Timezone)
			if err != nil {
				loc = time.UTC
			}
			t := start.In(loc)
			row.When, row.Day, row.Month = t.Format("Mon 2 Jan 2006"), t.Format("2"), t.Format("Jan")
		}
		switch {
		case phase != event.PhaseClosed && recent[ev.APIId]:
			row.Path = s.eventPagePath(ev.APIId)
			up = append(up, dated{row, start})
		case phase == event.PhaseClosed && row.Sessions > 0:
			row.Path = s.eventPagePath(ev.APIId)
			gone = append(gone, dated{row, start})
		}
	}
	sort.SliceStable(up, func(i, j int) bool { return up[i].start.Before(up[j].start) })
	sort.SliceStable(gone, func(i, j int) bool { return gone[i].start.After(gone[j].start) })
	var upcoming, past []sniffEventRow
	for _, d := range up {
		upcoming = append(upcoming, d.row)
	}
	for _, d := range gone {
		past = append(past, d.row)
	}
	for i := range upcoming {
		upcoming[i].Extra = i >= 5
	}
	for i := range past {
		past[i].Extra = i >= 5
	}
	data["Upcoming"], data["Past"] = upcoming, past
	data["LinkKey"], _ = s.sniff.LinkKey()
	s.render(w, "sniff_index.html", data)
}

func (s *Server) visibleSessions(eventID string) int {
	n := 0
	for _, ss := range s.sniff.Sessions(eventID) {
		if !ss.Hidden {
			n++
		}
	}
	return n
}
