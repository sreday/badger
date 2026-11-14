// Package snitch is Snitch by Badger: each team member's working week,
// planned as missions on a Monday-Friday board, shipped to a page of their
// own (a fixed link + PIN) where they start, pause and finish each mission.
// Every click is logged; Claude reads the logs. See README.md.
package snitch

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sreday/badger/sites"
	"github.com/sreday/badger/ui"
	"github.com/sreday/badger/web"
)

//go:embed templates/*.html
var templateFS embed.FS

// The LLMday llama: it dances on today in the calendar (the site's dancing
// GIF, made small).
//
//go:embed assets/llama.webp
var llamaWebP []byte

// Config is Snitch's configuration. Snout fills in where it's mounted and
// its login; the rest comes from the environment.
type Config struct {
	Base          string // /snitch (Snout strips it)
	DataDir       string // Snitch keeps everything in DataDir/snitch
	AdminUser     string
	AdminPassword string
	Version       string
	PublicURL     string

	TZ           string   // SNITCH_TZ: the team's clock for the board and the logs
	Routine      string   // SNITCH_ROUTINE: trig_ id of "Snitch: read the logs"
	RoutineToken string   // SNITCH_ROUTINE_TOKEN: its API trigger token (or pasted in the Claude menu)
	Repos        []string // site repos, for the events
}

func ConfigFromEnv() Config {
	tz := os.Getenv("SNITCH_TZ")
	if tz == "" {
		tz = "Europe/London"
	}
	return Config{TZ: tz, Routine: os.Getenv("SNITCH_ROUTINE"), RoutineToken: os.Getenv("SNITCH_ROUTINE_TOKEN"), Repos: sites.Repos()}
}

type Server struct {
	cfg    Config
	tmpls  map[string]*template.Template
	tz     *time.Location
	now    func() time.Time
	dir    string
	token  string // the routine's bearer token
	secret []byte // signs the workers' cookies
	events *Events

	mu        sync.Mutex // guards everything below
	st        *Store
	pinFails  map[string]*pinFail
	lastClick map[string]time.Time // person -> last start/pause/finish
	lastSeen  map[string]time.Time // person -> last sign of the page
	jobs      []*Job
	lessons   Lessons
	claude    claudeSetup
	fireBase  string
}

func New(cfg Config) (*Server, error) {
	tz, err := time.LoadLocation(cfg.TZ)
	if err != nil {
		tz = time.UTC
	}
	funcs := template.FuncMap{
		"tool":  ui.Tool,
		"min":   fmtMin,
		"hours": fmtHours,
		"when": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.In(tz).Format("Mon 2 Jan, 15:04")
		},
	}
	tmpls := map[string]*template.Template{}
	for _, page := range []string{"admin.html", "me.html", "pin.html"} {
		t, err := template.New(page).Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/"+page)
		if err == nil {
			t, err = ui.Parse(t)
		}
		if err != nil {
			return nil, fmt.Errorf("parsing template %s: %w", page, err)
		}
		tmpls[page] = t
	}
	dir := filepath.Join(cfg.DataDir, "snitch")
	st, err := OpenStore(dir)
	if err != nil {
		return nil, fmt.Errorf("opening store: %w", err)
	}
	token, err := loadSecret(filepath.Join(dir, "token"))
	if err != nil {
		return nil, err
	}
	secret, err := loadSecret(filepath.Join(dir, "secret"))
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg: cfg, tmpls: tmpls, tz: tz, now: time.Now, dir: dir, token: token, secret: []byte(secret), st: st,
		events:   &Events{Repos: cfg.Repos, path: eventsPath(dir)},
		pinFails: map[string]*pinFail{}, lastClick: map[string]time.Time{}, lastSeen: map[string]time.Time{},
		fireBase: fireURL,
	}
	s.events.load()
	readJSON(filepath.Join(dir, "jobs.json"), &s.jobs)
	readJSON(filepath.Join(dir, "lessons.json"), &s.lessons)
	readJSON(filepath.Join(dir, "claude.json"), &s.claude)
	return s, nil
}

func loadSecret(path string) (string, error) {
	if data, err := os.ReadFile(path); err == nil && len(bytes.TrimSpace(data)) >= 32 {
		return string(bytes.TrimSpace(data)), nil
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	v := hex.EncodeToString(b)
	return v, os.WriteFile(path, []byte(v+"\n"), 0o600)
}

// Start keeps the events list fresh and runs the small housekeeping tick
// (Claude's weekly jobs, clocks left running past their week).
func (s *Server) Start() {
	go func() {
		for {
			s.events.Refresh(s.now())
			time.Sleep(eventsEvery)
		}
	}()
	go func() {
		for {
			s.tick()
			time.Sleep(10 * time.Minute)
		}
	}()
}

func (s *Server) tick() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.closeOldClocks(now)
	s.dueJobs(now)
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Robots-Tag", "noindex")
		user, pass, ok := r.BasicAuth()
		userOK := s.cfg.AdminUser == "" || subtle.ConstantTimeCompare([]byte(user), []byte(s.cfg.AdminUser)) == 1
		if !ok || !userOK || s.cfg.AdminPassword == "" || subtle.ConstantTimeCompare([]byte(pass), []byte(s.cfg.AdminPassword)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="Snout by Badger"`) // Snout's, so the browser asks once
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if s.token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// Handler serves Snitch under Base (Snout strips the prefix).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.requireAdmin(s.handleAdmin))
	mux.HandleFunc("POST /admin/{action}", s.requireAdmin(s.handleAction))
	mux.HandleFunc("GET /admin/jobs", s.requireAdmin(s.handleJobs))
	mux.HandleFunc("GET /static/llama.webp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/webp")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(llamaWebP)
	})
	mux.HandleFunc("GET /api/queue", s.requireToken(s.handleQueue))
	mux.HandleFunc("POST /api/answers", s.requireToken(s.handleAnswers))
	// The workers' pages: the link is the secret, the PIN opens it.
	mux.HandleFunc("GET /{token}", web.Private(s.handleMe))
	mux.HandleFunc("POST /{token}/{act}", web.Private(s.handleMeAct))
	return mux
}

func (s *Server) render(w http.ResponseWriter, page, name string, data any) {
	var buf bytes.Buffer
	if err := s.tmpls[page].ExecuteTemplate(&buf, name, data); err != nil {
		log.Printf("Snitch template error: %v", err)
		http.Error(w, "Snitch couldn't draw this page: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(buf.Bytes())
}

// personTZ is the clock that decides the person's "today".
func (s *Server) personTZ(p Person) *time.Location {
	if p.TZ != "" {
		if loc, err := time.LoadLocation(p.TZ); err == nil {
			return loc
		}
	}
	return s.tz
}

// frozen: the day can't be planned any more. It is past, or the worker
// has started a mission on it.
func (s *Server) frozen(p Person, w *Week, day int, now time.Time) bool {
	mon, ok := mondayOf(w.ID)
	if !ok {
		return true
	}
	return mon.AddDate(0, 0, day).Before(dateOf(now, s.personTZ(p))) || w.dayStarted(day)
}
