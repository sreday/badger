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
	s := &Server{cfg: cfg, host: host, tmpls: tmpls, now: cfg.Now, sniff: store, sniffLimit: newLimiter()}
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
	ID, Name, When, Path string
	Sessions             int
}

// handleSniffIndex lists the events to open Sniff for: upcoming ones from
// Luma, and any older one that already has sessions.
func (s *Server) handleSniffIndex(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{}
	events, err := luma.ListEvents(s.cfg.LumaAPIKey)
	if err != nil {
		data["Error"] = "Luma: " + err.Error()
	}
	var rows []sniffEventRow
	for _, ev := range luma.RecentEvents(events) {
		start, _ := ev.Times()
		when := ""
		if !start.IsZero() {
			when = event.LocalTime(start, ev.Timezone)
		}
		rows = append(rows, sniffEventRow{ID: ev.APIId, Name: ev.Name, When: when, Path: s.eventPagePath(ev.APIId), Sessions: s.visibleSessions(ev.APIId)})
	}
	data["Events"] = rows
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
