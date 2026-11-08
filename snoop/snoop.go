// Package snoop (Snoop by Badger) leaves a trace of every Google Meet that
// mark@sreday.com takes part in: notes to act on (summary + next steps) and
// the full transcript, as a Drive doc and on its admin pages. Snout hosts it
// at scan.sreday.com/snoop, the way it hosts Badger 2.0 at /badger.
//
// It runs on free tools only: Meet's own transcripts and Gemini notes for
// Marek's Meets (Meet REST API), and the Snoop Chrome extension, which reads
// Meet's live captions in any Meet he joins and summarises on the laptop
// with Chrome's built-in AI. No paid LLM is called.
package snoop

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed templates/*.html
var templateFS embed.FS

// Config holds Snoop's configuration. Snout fills in where it's mounted and
// its admin login; the Google settings come from the environment
// (ConfigFromEnv).
type Config struct {
	Base          string // path Snoop is mounted at, e.g. /snoop (Snout strips it)
	PublicURL     string // e.g. https://scan.sreday.com, for the OAuth redirect and links in docs
	DataDir       string // Snoop keeps everything in DataDir/snoop
	AdminUser     string
	AdminPassword string
	Version       string

	ClientID     string
	ClientSecret string
	CalendarID   string
	DriveFolder  string
	TZ           string
	Account      string // the one Google account Snoop traces
}

// ConfigFromEnv reads Snoop's own settings.
func ConfigFromEnv() Config {
	return Config{
		ClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		ClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		CalendarID:   envOr("SNOOP_CALENDAR_ID", "primary"),
		DriveFolder:  envOr("SNOOP_DRIVE_FOLDER", "Snoop"),
		TZ:           envOr("SNOOP_TZ", "Europe/London"),
		Account:      strings.ToLower(envOr("SNOOP_ACCOUNT", "mark@sreday.com")),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Server holds application state.
type Server struct {
	cfg    Config
	tmpls  map[string]*template.Template
	store  *Store
	google *Google
	secret []byte
	tz     *time.Location
	now    func() time.Time // the clock; tests move it

	kick   chan struct{} // "Sync now"
	syncMu sync.Mutex

	statMu   sync.Mutex
	lastSync time.Time
	lastErr  error
}

// New opens Snoop's store and Google token. Call Start to begin syncing.
func New(cfg Config) (*Server, error) {
	if cfg.PublicURL == "" {
		cfg.PublicURL = "https://scan.sreday.com"
	}
	cfg.PublicURL = strings.TrimRight(cfg.PublicURL, "/")
	funcs := template.FuncMap{
		"fmtTime": func(t time.Time, tz *time.Location) string { return t.In(tz).Format("Mon 2 Jan, 15:04") },
		"clock":   func(t time.Time, tz *time.Location) string { return t.In(tz).Format("15:04:05") },
		"short":   func(s string) string { return truncate(s, 140) },
		"tool": func(name, sub string) map[string]string {
			return map[string]string{"Name": name, "Sub": sub}
		},
	}
	pages := []string{"admin.html", "call.html", "actions.html", "device.html"}
	tmpls := make(map[string]*template.Template, len(pages))
	for _, page := range pages {
		t, err := template.New(page).Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/brand.html", "templates/"+page)
		if err != nil {
			return nil, fmt.Errorf("parsing template %s: %w", page, err)
		}
		tmpls[page] = t
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	store, err := OpenStore(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("opening store: %w", err)
	}
	g, err := NewGoogle(cfg.ClientID, cfg.ClientSecret, cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("loading the Google token: %w", err)
	}
	secret, err := loadSecret(filepath.Join(cfg.DataDir, "snoop", "secret"))
	if err != nil {
		return nil, fmt.Errorf("loading secret: %w", err)
	}
	tz, err := time.LoadLocation(cfg.TZ)
	if err != nil {
		return nil, fmt.Errorf("TZ_NAME: %w", err)
	}
	return &Server{
		cfg:    cfg,
		tmpls:  tmpls,
		store:  store,
		google: g,
		secret: secret,
		tz:     tz,
		now:    time.Now,
		kick:   make(chan struct{}, 1),
	}, nil
}

// loadSecret reads the key that signs the OAuth state, creating it on first run.
func loadSecret(path string) ([]byte, error) {
	if data, err := os.ReadFile(path); err == nil && len(data) >= 32 {
		return data, nil
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	return secret, os.WriteFile(path, secret, 0o600)
}

func (s *Server) render(w http.ResponseWriter, name string, data map[string]any) {
	w.Header().Set("Cache-Control", "no-store")
	data["TZ"] = s.tz
	data["Base"] = s.cfg.Base
	data["Page"] = name
	if err := s.tmpls[name].ExecuteTemplate(w, name, data); err != nil {
		log.Printf("Template error: %v", err)
	}
}

func (s *Server) syncNow() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// --- Admin ---------------------------------------------------------------

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		userOK := s.cfg.AdminUser == "" || subtle.ConstantTimeCompare([]byte(user), []byte(s.cfg.AdminUser)) == 1
		if !ok || !userOK || subtle.ConstantTimeCompare([]byte(pass), []byte(s.cfg.AdminPassword)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="Snout by Badger"`) // Snout's, so the browser asks once
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// callRow is one line of the /admin list.
type callRow struct {
	Call
	Status, Reason string
	OpenSteps      int
}

func (s *Server) row(c Call, now time.Time) callRow {
	st, reason := c.Status(now)
	r := callRow{Call: c, Status: st, Reason: reason}
	if n := c.Notes(); n != nil {
		for i := range n.NextSteps {
			if !c.Ticked[i] {
				r.OpenSteps++
			}
		}
	}
	return r
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	calls := Search(s.store.All(), q)
	// Calendly bookings (sales calls) get their own tab, shown first.
	var calendly, other []callRow
	for _, c := range calls {
		if q == "" && (c.Cancelled || c.Declined && len(c.Lines()) == 0 || c.Empty(now)) && r.URL.Query().Get("all") == "" {
			continue // still traced, just not in the way
		}
		if c.Calendly {
			calendly = append(calendly, s.row(c, now))
		} else {
			other = append(other, s.row(c, now))
		}
	}
	view := "calendly"
	if r.URL.Query().Get("view") == "other" {
		view = "other"
	}
	rows := calendly
	if view == "other" {
		rows = other
	}
	s.statMu.Lock()
	lastSync, lastErr := s.lastSync, s.lastErr
	s.statMu.Unlock()
	errText := ""
	if lastErr != nil {
		errText = lastErr.Error()
	}
	s.render(w, "admin.html", map[string]any{
		"Rows":      rows,
		"View":      view,
		"NCalendly": len(calendly),
		"NOther":    len(other),
		"Q":         q,
		"All":       r.URL.Query().Get("all") != "",
		"Connected": s.google.Connected(),
		"CanAuth":   s.cfg.ClientID != "" && s.cfg.ClientSecret != "",
		"LastSync":  lastSync,
		"SyncError": errText,
		"Devices":   s.store.Devices(),
		"Version":   s.cfg.Version,
	})
}

func (s *Server) handleCall(w http.ResponseWriter, r *http.Request) {
	c, ok := s.store.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	type step struct {
		I    int
		Text string
		Done bool
	}
	var steps []step
	notes := c.Notes()
	if notes != nil {
		for i, t := range notes.NextSteps {
			steps = append(steps, step{i, t, c.Ticked[i]})
		}
	}
	type para struct {
		Speaker string
		At      time.Time
		Texts   []string
	}
	var paras []para
	for _, l := range c.Lines() {
		if n := len(paras); n > 0 && paras[n-1].Speaker == l.Speaker {
			paras[n-1].Texts = append(paras[n-1].Texts, l.Text)
			continue
		}
		paras = append(paras, para{l.Speaker, l.At, []string{l.Text}})
	}
	s.render(w, "call.html", map[string]any{
		"Row":    s.row(c, s.now()),
		"Notes":  notes,
		"Steps":  steps,
		"Paras":  paras,
		"Source": c.TranscriptSource(),
	})
}

// handleTick marks a next step done or not done.
func (s *Server) handleTick(w http.ResponseWriter, r *http.Request) {
	i, err := strconv.Atoi(r.PathValue("i"))
	if err != nil || i < 0 {
		http.Error(w, "bad step", http.StatusBadRequest)
		return
	}
	done := r.FormValue("done") == "1"
	_, err = s.store.Update(r.PathValue("id"), func(c *Call) {
		if c.Ticked == nil {
			c.Ticked = make(map[int]bool)
		}
		if done {
			c.Ticked[i] = true
		} else {
			delete(c.Ticked, i)
		}
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.syncNow() // rewrites the Drive doc's checkboxes
	if r.Header.Get("X-Requested-With") == "fetch" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, r.Header.Get("Referer"), http.StatusSeeOther)
}

// handleRetry forgets what the Meet API gave for a call and fetches it again.
func (s *Server) handleRetry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.store.Get(id); !ok {
		http.NotFound(w, r)
		return
	}
	s.store.Update(id, func(c *Call) {
		c.Armed, c.ArmError, c.Error = false, "", ""
		c.TranscriptDone, c.APILines, c.APINotes = false, nil, nil
		c.DocHash = ""
	})
	s.syncNow()
	http.Redirect(w, r, s.cfg.Base+"/admin/call/"+id, http.StatusSeeOther)
}

func (s *Server) handleActions(w http.ResponseWriter, r *http.Request) {
	type action struct {
		Call Call
		I    int
		Text string
	}
	var open []action
	for _, c := range s.store.All() {
		n := c.Notes()
		if n == nil {
			continue
		}
		for i, t := range n.NextSteps {
			if !c.Ticked[i] {
				open = append(open, action{c, i, t})
			}
		}
	}
	s.render(w, "actions.html", map[string]any{"Actions": open})
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	s.syncNow()
	http.Redirect(w, r, s.cfg.Base+"/admin", http.StatusSeeOther)
}

// --- Connect Google --------------------------------------------------------

func (s *Server) redirectURI() string { return s.cfg.PublicURL + s.cfg.Base + "/admin/google/callback" }

// oauthState signs the time, so a callback is only accepted for a flow
// started here in the last 15 minutes.
func (s *Server) oauthState(t time.Time) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte("oauth:" + ts))
	return ts + "." + hex.EncodeToString(m.Sum(nil))
}

func (s *Server) validState(state string) bool {
	ts, _, ok := strings.Cut(state, ".")
	sec, err := strconv.ParseInt(ts, 10, 64)
	if !ok || err != nil {
		return false
	}
	t := time.Unix(sec, 0)
	return s.now().Sub(t) < 15*time.Minute && hmac.Equal([]byte(state), []byte(s.oauthState(t)))
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	if s.cfg.ClientID == "" || s.cfg.ClientSecret == "" {
		http.Error(w, "GOOGLE_CLIENT_ID / GOOGLE_CLIENT_SECRET aren't set", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, s.google.AuthURL(s.redirectURI(), s.oauthState(s.now()), s.cfg.Account), http.StatusSeeOther)
}

func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	if e := r.FormValue("error"); e != "" {
		http.Error(w, "Google said: "+e, http.StatusBadRequest)
		return
	}
	if !s.validState(r.FormValue("state")) {
		http.Error(w, "This sign-in link expired; go back to /admin and click Connect Google again.", http.StatusBadRequest)
		return
	}
	email, err := s.google.Exchange(r.FormValue("code"), s.redirectURI(), s.cfg.Account)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	log.Printf("Google connected as %s", email)
	s.syncNow()
	http.Redirect(w, r, s.cfg.Base+"/admin", http.StatusSeeOther)
}

// --- Extension devices -----------------------------------------------------

func (s *Server) handleNewDevice(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = "Chrome"
	}
	token, err := s.store.NewDevice(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Shown once: only the hash is kept.
	s.render(w, "device.html", map[string]any{"Name": name, "Token": token, "Server": s.cfg.PublicURL + s.cfg.Base})
}

func (s *Server) handleRemoveDevice(w http.ResponseWriter, r *http.Request) {
	if err := s.store.RemoveDevice(r.PathValue("hash")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, s.cfg.Base+"/admin", http.StatusSeeOther)
}

// --- Ingest (the Snoop extension) -----------------------------------------

// ingestReq is what the extension posts: caption lines as they firm up,
// then, when the call ends, its on-device notes.
type ingestReq struct {
	MeetingCode string    `json:"meeting_code"`
	Title       string    `json:"title"`
	StartedAt   time.Time `json:"started_at"`
	Lines       []Line    `json:"lines"`
	Ended       bool      `json:"ended"`
	Notes       *Notes    `json:"notes"`
	Problem     string    `json:"problem"`
}

func (s *Server) device(r *http.Request) (Device, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return Device{}, false
	}
	return s.store.Device(token)
}

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	d, ok := s.device(r)
	if !ok {
		http.Error(w, "unknown device token", http.StatusUnauthorized)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"device": d.Name, "version": s.cfg.Version})
}

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	d, ok := s.device(r)
	if !ok {
		http.Error(w, "unknown device token", http.StatusUnauthorized)
		return
	}
	var req ingestReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&req); err != nil {
		http.Error(w, "bad JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	id, err := s.ingest(d, req)
	if err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, errBadIngest) {
			code = http.StatusBadRequest
		}
		http.Error(w, err.Error(), code)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"call": id})
}

var errBadIngest = errors.New("meeting_code and started_at are required")

func (s *Server) ingest(d Device, req ingestReq) (string, error) {
	if meetingCodeFromURL("meet.google.com/"+req.MeetingCode) != req.MeetingCode || req.StartedAt.IsZero() {
		return "", errBadIngest
	}
	now := s.now()
	adHoc := false
	c, ok := s.store.FindByCode(req.MeetingCode, req.StartedAt)
	id := c.ID
	if !ok {
		// No calendar event: someone sent a link. Keyed by when the
		// extension saw the call start, so later posts find it again.
		id = CallID("ext_" + req.MeetingCode + "_" + req.StartedAt.UTC().Format("20060102T1504"))
		adHoc = true
	}
	_, err := s.store.Update(id, func(c *Call) {
		if adHoc {
			c.MeetingCode = req.MeetingCode
			if c.Start.IsZero() {
				c.Start = req.StartedAt
			}
			if c.Title == "" || req.Title != "" && strings.HasPrefix(c.Title, "Meet ") {
				c.Title = req.Title
				if c.Title == "" {
					c.Title = "Meet " + req.MeetingCode
				}
			}
		}
		c.ExtLines = mergeLines(c.ExtLines, req.Lines)
		if adHoc {
			end := c.Start.Add(time.Minute)
			if n := len(c.ExtLines); n > 0 && c.ExtLines[n-1].At.After(end) {
				end = c.ExtLines[n-1].At
			}
			if end.After(c.End) {
				c.End = end
			}
		}
		if req.Notes != nil {
			n := *req.Notes
			n.By = "Chrome on-device AI"
			c.ExtNotes = &n
		}
		if req.Ended {
			c.ExtEnded = true
		}
		if req.Problem != "" {
			c.ExtProblem = truncate(req.Problem, 200)
		} else if len(req.Lines) > 0 {
			c.ExtProblem = ""
		}
		c.ExtDevice = d.Name
		c.ExtLastAt = now.UTC()
	})
	return id, err
}

// mergeLines adds new caption lines, skipping ones already in (the
// extension resends after a network blip), and keeps them in time order.
func mergeLines(have, add []Line) []Line {
	seen := make(map[string]bool, len(have))
	key := func(l Line) string { return l.At.UTC().Truncate(time.Second).String() + "|" + l.Speaker + "|" + l.Text }
	for _, l := range have {
		seen[key(l)] = true
	}
	for _, l := range add {
		l.Text = strings.TrimSpace(l.Text)
		if l.Text == "" || seen[key(l)] {
			continue
		}
		seen[key(l)] = true
		have = append(have, l)
	}
	sortLines(have)
	return have
}

func sortLines(ls []Line) {
	for i := 1; i < len(ls); i++ { // insertion sort: nearly always in order already
		for j := i; j > 0 && ls[j].At.Before(ls[j-1].At); j-- {
			ls[j], ls[j-1] = ls[j-1], ls[j]
		}
	}
}

// --- Routes ----------------------------------------------------------------

// private keeps every Snoop page out of search engines and frames.
func private(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		next(w, r)
	}
}

// Handler serves Snoop's pages, relative to Config.Base (strip that prefix
// before this handler). Admin pages use Snout's admin login; the extension
// authenticates with its device token.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	admin := func(h http.HandlerFunc) http.HandlerFunc { return private(s.requireAdmin(h)) }
	mux.HandleFunc("GET /admin", admin(s.handleAdmin))
	mux.HandleFunc("GET /admin/call/{id}", admin(s.handleCall))
	mux.HandleFunc("POST /admin/call/{id}/tick/{i}", admin(s.handleTick))
	mux.HandleFunc("POST /admin/call/{id}/retry", admin(s.handleRetry))
	mux.HandleFunc("GET /admin/actions", admin(s.handleActions))
	mux.HandleFunc("POST /admin/sync", admin(s.handleSync))
	mux.HandleFunc("GET /admin/google/connect", admin(s.handleConnect))
	mux.HandleFunc("GET /admin/google/callback", admin(s.handleCallback))
	mux.HandleFunc("POST /admin/devices", admin(s.handleNewDevice))
	mux.HandleFunc("POST /admin/devices/{hash}/remove", admin(s.handleRemoveDevice))
	mux.HandleFunc("GET /ping", private(s.handlePing))
	mux.HandleFunc("POST /ingest", private(s.handleIngest))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, s.cfg.Base+"/admin", http.StatusSeeOther)
	})
	return mux
}

// Start begins the sync loop. Without a Google OAuth client only the
// extension's captions come in.
func (s *Server) Start() {
	if s.cfg.ClientID == "" || s.cfg.ClientSecret == "" {
		log.Printf("Snoop: GOOGLE_CLIENT_ID/GOOGLE_CLIENT_SECRET not set: only the extension's captions come in")
	}
	go s.loop()
}
