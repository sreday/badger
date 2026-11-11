// Package snap (Snap by Badger) finds the loose ends in mark@sreday.com's
// mail: a sponsor who'll "think about it", a speaker who cancelled but is
// still on the lineup, a host who said "let's touch base in 6 months". Snout
// hosts it at scan.sreday.com/snap, the way it hosts Snoop at /snoop.
//
// Snout does the free work: every hour it reads Gmail with Snap's own Google
// token, drops what can't be a loose end by rules (robots, newsletters,
// nobody replied, too old: 30 days back, a year for sponsorship), checks
// lineups, and keeps the changed threads in a queue as short excerpts.
// Claude does the reading on Marek's subscription: a Claude Code routine
// fetches the queue (GET /snap/api/queue) every 6 hours and posts its read
// back (POST /snap/api/answers). Until it answers, the rules' read shows.
package snap

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sreday/badger/sites"
	"github.com/sreday/badger/ui"
)

//go:embed templates/*.html
var templateFS embed.FS

// Config is Snap's configuration. Snout fills in where it's mounted, its
// login and public URL; the rest comes from the environment.
type Config struct {
	Base          string // /snap (Snout strips it)
	DataDir       string // Snap keeps everything in DataDir/snap
	AdminUser     string
	AdminPassword string
	Version       string

	PublicURL    string // https://scan.sreday.com, for the OAuth redirect
	ClientID     string // SNAP_GOOGLE_CLIENT_ID: Snap's own OAuth client ("Snap" in the Cloud console)
	ClientSecret string // SNAP_GOOGLE_CLIENT_SECRET
	Account      string // mark@sreday.com, for the Open in Gmail links

	Every       time.Duration // SNAP_EVERY: how often Gmail is read (free)
	Routine     string        // SNAP_ROUTINE: the Claude routine's page, for the menu
	TeamDomains []string
	Repos       []string // site repos, for lineups and sponsor domains
	TZ          string
}

// QueueSize is how many threads one GET /api/queue hands out; the routine
// asks again until the queue is empty.
const QueueSize = 25

// ConfigFromEnv reads Snap's own settings.
func ConfigFromEnv() Config {
	every, err := time.ParseDuration(os.Getenv("SNAP_EVERY"))
	if err != nil || every < 10*time.Minute {
		every = time.Hour
	}
	team := "sreday.com,conf42.com,llmday.com,platformday.com,promptengineering.rocks,devopsnotdead.com"
	if v := os.Getenv("SNAP_TEAM_DOMAINS"); v != "" {
		team = v
	}
	var domains []string
	for _, d := range strings.Split(team, ",") {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
			domains = append(domains, d)
		}
	}
	return Config{
		Every:        every,
		Routine:      envOr("SNAP_ROUTINE", "https://claude.ai/code/routines"),
		TeamDomains:  domains,
		Repos:        sites.Repos(),
		TZ:           envOr("SNAP_TZ", "Europe/London"),
		Account:      strings.ToLower(envOr("SNAP_ACCOUNT", "mark@sreday.com")),
		ClientID:     os.Getenv("SNAP_GOOGLE_CLIENT_ID"),
		ClientSecret: os.Getenv("SNAP_GOOGLE_CLIENT_SECRET"),
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// RunInfo is one Gmail sync, for the menu.
type RunInfo struct {
	Start   time.Time     `json:"start"`
	Took    time.Duration `json:"took"`
	Listed  int           `json:"listed"`  // threads Gmail returned
	Changed int           `json:"changed"` // new or moved since the last sync
	Queued  int           `json:"queued"`  // waiting for Claude after this sync
	Error   string        `json:"error,omitempty"`
}

// ClaudeInfo is what the routine did lately.
type ClaudeInfo struct {
	LastFetch   time.Time `json:"last_fetch"`
	LastAnswer  time.Time `json:"last_answer"`
	Model       string    `json:"model"`
	Applied     int       `json:"applied"` // threads read in the last answer
	AppliedRun  int       `json:"applied_run"`
	Stale       int       `json:"stale"` // answers for a thread that moved on meanwhile
	RunStart    time.Time `json:"run_start"`
	TotalRead   int       `json:"total_read"`
	LastProblem string    `json:"last_problem,omitempty"`
}

type cached struct {
	history, excerpt string
}

// Server is Snap.
type Server struct {
	cfg     Config
	tmpls   map[string]*template.Template
	store   *Store
	gmail   *Gmail
	google  *Google
	lineups *Lineups
	tz      *time.Location
	now     func() time.Time
	token   string // the routine's bearer token
	dir     string

	kick   chan struct{}
	syncMu sync.Mutex

	mu       sync.Mutex
	running  bool
	runs     []RunInfo // newest first, at most 20
	claude   ClaudeInfo
	excerpts map[string]cached // thread id -> its excerpt for the queue (memory only)
	spDoms   map[string]bool
	spDomsAt time.Time
}

func New(cfg Config) (*Server, error) {
	tz, err := time.LoadLocation(cfg.TZ)
	if err != nil {
		tz = time.UTC
	}
	funcs := template.FuncMap{
		"tool": ui.Tool,
		"iso":  func(t time.Time) string { return t.UTC().Format(time.RFC3339) },
		"day": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.In(tz).Format("Mon 2 Jan 2006")
		},
		"when": func(t time.Time) string {
			if t.IsZero() {
				return "never"
			}
			return t.In(tz).Format("Mon 2 Jan, 15:04")
		},
		"fdate": func(s string) string {
			d, err := time.Parse("2006-01-02", s)
			if err != nil {
				return s
			}
			return d.Format("Mon 2 Jan 2006")
		},
		"rules": func(by string) bool { return by == "rules" || by == "" },
	}
	tmpls := map[string]*template.Template{}
	for _, page := range []string{"index.html"} {
		t, err := template.New(page).Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/"+page)
		if err == nil {
			t, err = ui.Parse(t)
		}
		if err != nil {
			return nil, fmt.Errorf("parsing template %s: %w", page, err)
		}
		tmpls[page] = t
	}
	dir := filepath.Join(cfg.DataDir, "snap")
	store, err := OpenStore(dir)
	if err != nil {
		return nil, fmt.Errorf("opening store: %w", err)
	}
	token, err := loadToken(filepath.Join(dir, "token"))
	if err != nil {
		return nil, fmt.Errorf("routine token: %w", err)
	}
	s := &Server{
		cfg:      cfg,
		tmpls:    tmpls,
		store:    store,
		lineups:  &Lineups{Repos: cfg.Repos},
		tz:       tz,
		now:      time.Now,
		token:    token,
		dir:      dir,
		kick:     make(chan struct{}, 1),
		excerpts: map[string]cached{},
	}
	if s.google, err = NewGoogle(cfg.ClientID, cfg.ClientSecret, dir); err != nil {
		return nil, fmt.Errorf("Google token: %w", err)
	}
	s.gmail = NewGmail(s.google.AccessToken)
	if data, err := os.ReadFile(filepath.Join(dir, "runs.json")); err == nil {
		json.Unmarshal(data, &s.runs)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "claude.json")); err == nil {
		json.Unmarshal(data, &s.claude)
	}
	return s, nil
}

// loadToken reads the routine's bearer token, made on first start. It's
// pasted into the routine's prompt once (see README).
func loadToken(path string) (string, error) {
	if data, err := os.ReadFile(path); err == nil && len(bytes.TrimSpace(data)) >= 32 {
		return string(bytes.TrimSpace(data)), nil
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b)
	return tok, os.WriteFile(path, []byte(tok+"\n"), 0o600)
}

// Start begins the hourly Gmail sync; the first one a minute after start.
func (s *Server) Start() {
	go func() {
		timer := time.NewTimer(time.Minute)
		for {
			select {
			case <-timer.C:
			case <-s.kick:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
			}
			s.Sync(context.Background())
			timer.Reset(s.cfg.Every)
		}
	}()
}

func (s *Server) runNow() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// sponsorDomains: the web domains of sponsors listed on upcoming event
// pages, refreshed hourly.
func (s *Server) sponsorDomains(now time.Time) map[string]bool {
	s.mu.Lock()
	fresh := !s.spDomsAt.IsZero() && now.Sub(s.spDomsAt) < time.Hour
	doms := s.spDoms
	s.mu.Unlock()
	if fresh {
		return doms
	}
	all, _ := sites.ListSponsors(s.cfg.Repos)
	doms = map[string]bool{}
	for _, list := range all {
		for _, sp := range list {
			d := strings.ToLower(sp.URL)
			d = strings.TrimPrefix(strings.TrimPrefix(d, "https://"), "http://")
			d = strings.TrimPrefix(d, "www.")
			if i := strings.IndexAny(d, "/?#"); i >= 0 {
				d = d[:i]
			}
			if d != "" {
				doms[d] = true
			}
		}
	}
	s.mu.Lock()
	s.spDoms, s.spDomsAt = doms, now
	s.mu.Unlock()
	return doms
}

// Sync reads Gmail, applies the rules, fills the queue and checks lineups.
func (s *Server) Sync(ctx context.Context) RunInfo {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	now := s.now()
	run := RunInfo{Start: now}
	if err := s.sync(ctx, now, &run); err != nil {
		run.Error = err.Error()
		log.Printf("Snap: %v", err)
	}
	run.Took = s.now().Sub(now).Round(time.Second)
	s.mu.Lock()
	s.runs = append([]RunInfo{run}, s.runs...)
	if len(s.runs) > 20 {
		s.runs = s.runs[:20]
	}
	data, _ := json.Marshal(s.runs)
	s.mu.Unlock()
	os.WriteFile(filepath.Join(s.dir, "runs.json"), data, 0o600)
	return run
}

func (s *Server) sync(ctx context.Context, now time.Time, run *RunInfo) error {
	if email, _ := s.google.Connected(); email == "" {
		return ErrNotConnected
	}
	refs, err := s.gmail.List(searchQuery)
	run.Listed = len(refs)
	if err != nil {
		return err
	}
	known := s.store.HistoryIDs()
	doms := s.sponsorDomains(now)

	// 1. Headers and snippets of every new or moved thread. Saved every 200,
	// so a first read of a year of mail doesn't rewrite the file thousands of
	// times, and an interrupted one keeps what it read. A thread that fails
	// is tried again next hour; ten failures in a row stop the read.
	var firstErr error
	fails := 0
	for i, ref := range refs {
		if ctx.Err() != nil {
			s.store.Save()
			return ctx.Err()
		}
		if h, ok := known[ref.ID]; ok && h == ref.HistoryID {
			continue
		}
		p, err := s.gmail.Thread(ref.ID, false)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("reading thread %s: %w", ref.ID, err)
			}
			if fails++; fails >= 10 {
				s.store.Save()
				return firstErr
			}
			continue
		}
		fails = 0
		run.Changed++
		m := Look(p, now, s.cfg.TeamDomains, doms)
		s.store.Stage(ref.ID, func(t *Thread) { applyMeta(t, p, m) })
		if i%200 == 199 {
			s.store.Save()
		}
	}
	s.store.Save()
	// 2. Threads past their window (30 days, a year for sponsorship) step
	// aside, unless a follow-up date keeps them.
	s.store.UpdateAll(func(t *Thread) { expire(t, now) })

	// 3. Every thread waiting for Claude: its excerpt for the queue, and the
	// rules' read to show meanwhile.
	for _, t := range s.store.All() {
		if !t.Pending() {
			continue
		}
		s.mu.Lock()
		c, ok := s.excerpts[t.ID]
		s.mu.Unlock()
		if ok && c.history == t.HistoryID && t.AnalyzedHistory == t.HistoryID {
			continue
		}
		p, err := s.gmail.Thread(t.ID, true)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		m := Look(p, now, s.cfg.TeamDomains, doms)
		s.store.Stage(t.ID, func(t *Thread) {
			applyMeta(t, p, m)
			if m.Skip == "" && t.AnalyzedHistory != p.HistoryID {
				applyAnalysis(t, Rules(p, m), p.HistoryID, "rules", now)
			}
		})
		if m.Skip == "" {
			s.mu.Lock()
			s.excerpts[t.ID] = cached{p.HistoryID, Excerpt(p, m)}
			s.mu.Unlock()
		}
	}
	s.store.Save()
	s.store.UpdateAll(func(t *Thread) { expire(t, now) })
	run.Queued = len(s.queue(0))

	// 4. Speakers who cancelled: still on a lineup?
	s.checkLineups(now)
	return firstErr
}

func (s *Server) checkLineups(now time.Time) {
	lineups, err := s.lineups.Get(now)
	if err != nil && len(lineups) == 0 {
		log.Printf("Snap: %v", err)
		return
	}
	s.store.UpdateAll(func(t *Thread) {
		if t.CancelName == "" || t.Skip != "" {
			return
		}
		t.StillListed = StillListed(lineups, t.CancelName, t.CancelEvent)
		t.ListedAt = now
	})
}

// QueueItem is one thread for Claude.
type QueueItem struct {
	ID      string `json:"id"`
	History string `json:"history"`
	Excerpt string `json:"excerpt"`
}

// queue lists the threads waiting for Claude whose excerpt is ready, newest
// first; at most n (0 = all).
func (s *Server) queue(n int) []QueueItem {
	all := s.store.All()
	sort.Slice(all, func(i, j int) bool { return all[i].LastAt.After(all[j].LastAt) })
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []QueueItem
	for _, t := range all {
		if !t.Pending() {
			continue
		}
		c, ok := s.excerpts[t.ID]
		if !ok || c.history != t.HistoryID {
			continue
		}
		out = append(out, QueueItem{t.ID, t.HistoryID, c.excerpt})
		if n > 0 && len(out) == n {
			break
		}
	}
	return out
}

// --- The routine's API ------------------------------------------------------

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

// handleQueue: GET /api/queue hands out the next threads to read, with the
// instructions. "remaining" counts what's left after these.
func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	all := s.queue(0)
	items := all
	if len(items) > QueueSize {
		items = items[:QueueSize]
	}
	if items == nil {
		items = []QueueItem{}
	}
	s.mu.Lock()
	if now.Sub(s.claude.LastFetch) > time.Hour {
		s.claude.RunStart, s.claude.AppliedRun = now, 0 // a new routine run
	}
	s.claude.LastFetch = now
	s.saveClaudeLocked()
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"today":        now.In(s.tz).Format("Monday 2 January 2006"),
		"instructions": Instructions,
		"threads":      items,
		"remaining":    len(all) - len(items),
	})
}

// handleAnswers: POST /api/answers takes Claude's read. An answer for a
// thread that moved on since (new mail) is dropped; the thread is queued again.
func (s *Server) handleAnswers(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Model   string     `json:"model"`
		Threads []Analysis `json:"threads"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&in); err != nil {
		http.Error(w, "bad JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	model := strings.TrimSpace(in.Model)
	if model == "" || model == "rules" {
		model = "claude"
	}
	now := s.now()
	applied, stale, unknown := 0, 0, 0
	for _, a := range in.Threads {
		t, ok := s.store.Get(a.ID)
		switch {
		case !ok:
			unknown++
			continue
		case a.History != t.HistoryID:
			stale++
			continue
		}
		s.store.Update(a.ID, func(t *Thread) { applyAnalysis(t, a, a.History, model, now) })
		applied++
	}
	s.store.UpdateAll(func(t *Thread) { expire(t, now) })
	s.checkLineups(now)
	s.mu.Lock()
	s.claude.LastAnswer, s.claude.Model = now, model
	s.claude.Applied, s.claude.Stale = applied, stale
	s.claude.AppliedRun += applied
	s.claude.TotalRead += applied
	s.claude.LastProblem = ""
	if unknown > 0 {
		s.claude.LastProblem = fmt.Sprintf("%d answers for threads Snap doesn't know", unknown)
	}
	s.saveClaudeLocked()
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"applied": applied, "stale": stale, "unknown": unknown, "remaining": len(s.queue(0))})
}

func (s *Server) saveClaudeLocked() {
	data, _ := json.Marshal(s.claude)
	os.WriteFile(filepath.Join(s.dir, "claude.json"), data, 0o600)
}

// applyMeta copies what Gmail says. New mail on a thread Marek marked done
// or snoozed brings it back.
func applyMeta(t *Thread, p *ParsedThread, m Meta) {
	moved := !t.LastAt.IsZero() && m.LastAt.After(t.LastAt)
	t.Subject, t.People, t.Messages = p.Subject, m.People, len(p.Mails)
	if t.Subject == "" {
		t.Subject = "(no subject)"
	}
	t.FirstAt, t.LastAt, t.LastFrom = m.FirstAt, m.LastAt, m.LastFrom
	t.HistoryID, t.Internal, t.Sponsor, t.Skip, t.Snippet = p.HistoryID, m.Internal, m.Sponsor, m.Skip, m.Snippet
	if t.AnalyzedHistory == "" {
		t.Waiting = m.Waiting
	}
	if moved {
		t.Done, t.SnoozeUntil = false, time.Time{}
	}
}

func applyAnalysis(t *Thread, a Analysis, history, by string, now time.Time) {
	t.AnalyzedHistory, t.AnalyzedAt, t.AnalyzedBy, t.Error = history, now, by, ""
	if by != "rules" {
		t.ClaudeHistory = history
	}
	t.Category, t.Loose, t.Label, t.Why, t.Summary = a.Category, a.Loose, a.Label, a.Why, a.Summary
	t.Waiting, t.FollowUp, t.Priority, t.Company, t.Event = a.Waiting, a.FollowUp, a.Priority, a.Company, a.Event
	if _, err := time.Parse("2006-01-02", t.FollowUp); err != nil {
		t.FollowUp = ""
	}
	t.Actions, t.Ticked = a.Actions, nil
	t.CancelName, t.CancelEvent = strings.TrimSpace(a.CancelName), strings.TrimSpace(a.CancelEvent)
	if t.CancelName == "" {
		t.StillListed, t.ListedAt = "", time.Time{}
	}
	if !t.Loose {
		t.Label, t.Actions = "", nil
	}
}

// expire: only sponsorship (and hosting) is kept a year; the rest 30 days.
// A follow-up date keeps a thread until a month after it.
func expire(t *Thread, now time.Time) {
	if t.Skip != "" && t.Skip != "older than 30 days" {
		return
	}
	window := shortWindow
	if t.Category == "sponsors" || (t.Category == "" && t.Sponsor) {
		window = longWindow
	}
	keep := now.Sub(t.LastAt) <= window
	if d, ok := t.FollowUpDate(); ok && now.Before(d.AddDate(0, 1, 0)) {
		keep = true
	}
	if t.StillListed != "" {
		keep = true
	}
	switch {
	case !keep && t.Skip == "":
		t.Skip = "older than 30 days"
	case keep && t.Skip == "older than 30 days" && t.AnalyzedHistory != "":
		t.Skip = "" // a sponsor thread Claude has read: back in
	}
}

// --- Pages -----------------------------------------------------------------

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

// Handler serves Snap under Base (Snout strips the prefix).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.requireAdmin(s.handleIndex))
	mux.HandleFunc("POST /admin/run", s.requireAdmin(s.handleRun))
	mux.HandleFunc("POST /admin/thread/{id}/done", s.requireAdmin(s.handleDone))
	mux.HandleFunc("POST /admin/thread/{id}/snooze", s.requireAdmin(s.handleSnooze))
	mux.HandleFunc("POST /admin/thread/{id}/tick/{i}", s.requireAdmin(s.handleTick))
	mux.HandleFunc("POST /admin/thread/{id}/reread", s.requireAdmin(s.handleReread))
	mux.HandleFunc("GET /admin/google/connect", s.requireAdmin(s.handleConnect))
	mux.HandleFunc("GET /admin/google/callback", s.requireAdmin(s.handleCallback))
	mux.HandleFunc("GET /api/queue", s.requireToken(s.handleQueue))
	mux.HandleFunc("POST /api/answers", s.requireToken(s.handleAnswers))
	return mux
}

var categories = []struct{ Key, Name string }{
	{"sponsors", "Sponsors"}, {"speakers", "Speakers"}, {"team", "Team"}, {"others", "Others"},
}

// Row is a thread as the page shows it.
type Row struct {
	Thread
	State    string // open, due, later, snoozed, done
	Gmail    string
	Prompts  []string // per action: what Copy for Claude copies
	Selected bool
}

func (s *Server) row(t Thread, now time.Time) Row {
	r := Row{Thread: t, Gmail: "https://mail.google.com/mail/u/?authuser=" + s.cfg.Account + "#all/" + t.ID}
	switch {
	case t.Done:
		r.State = "done"
	case t.SnoozeUntil.After(now):
		r.State = "snoozed"
	case t.Later(now):
		r.State = "later"
	case t.Due(now):
		r.State = "due"
	default:
		r.State = "open"
	}
	var who []string
	for _, p := range t.People {
		if p.Name != "" {
			who = append(who, p.Name+" <"+p.Email+">")
		} else {
			who = append(who, p.Email)
		}
		if len(who) == 4 {
			break
		}
	}
	for _, a := range t.Actions {
		ctx := fmt.Sprintf("Context (from Snap): Gmail thread %q in %s (thread id %s)", t.Subject, s.cfg.Account, t.ID)
		if len(who) > 0 {
			ctx += ", with " + strings.Join(who, ", ")
		}
		ctx += ". Last message " + t.LastAt.In(s.tz).Format("2 Jan 2006") + "."
		if t.Summary != "" {
			ctx += " " + t.Summary
		}
		if t.StillListed != "" {
			ctx += " " + t.StillListed + "."
		}
		r.Prompts = append(r.Prompts, a.Prompt+"\n\n"+ctx+"\nShow me any draft before anything is sent.")
	}
	return r
}

// rank: due follow-ups and lineup problems first, then what waits on Mark,
// then priority, then the latest.
func rank(r Row) int {
	n := 0
	if r.State == "due" || r.StillListed != "" {
		n -= 100
	}
	if r.Waiting == "you" {
		n -= 10
	}
	switch r.Priority {
	case "high":
		n -= 5
	case "low":
		n += 5
	}
	switch r.State {
	case "later":
		n += 200
	case "snoozed":
		n += 300
	case "done":
		n += 400
	}
	return n
}

func matches(t Thread, q string) bool {
	if q == "" {
		return true
	}
	hay := strings.ToLower(strings.Join([]string{t.Subject, t.Company, t.Event, t.Summary, t.Label, t.Why, t.Snippet, t.CancelName}, " "))
	for _, p := range t.People {
		hay += " " + strings.ToLower(p.Name+" "+p.Email)
	}
	for _, w := range strings.Fields(strings.ToLower(q)) {
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	view := r.URL.Query().Get("view")
	ok := false
	for _, c := range categories {
		ok = ok || c.Key == view
	}
	if !ok {
		view = "sponsors"
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	all := r.URL.Query().Get("all") != ""
	counts := map[string]int{}
	pending := 0
	var rows []Row
	for _, t := range s.store.All() {
		if t.Pending() {
			pending++
		}
		if !t.IsLoose() {
			continue
		}
		cat := t.Category
		if cat == "" {
			cat = "others"
		}
		if t.Open(now) {
			counts[cat]++
		}
		if q == "" && cat != view {
			continue
		}
		if !matches(t, q) || (!all && !t.Open(now)) {
			continue
		}
		rows = append(rows, s.row(t, now))
	}
	sort.SliceStable(rows, func(i, j int) bool {
		ri, rj := rank(rows[i]), rank(rows[j])
		if ri != rj {
			return ri < rj
		}
		return rows[i].LastAt.After(rows[j].LastAt)
	})
	sel := r.URL.Query().Get("t")
	found := false
	for i := range rows {
		if rows[i].ID == sel {
			rows[i].Selected, found = true, true
		}
	}
	if !found && len(rows) > 0 {
		rows[0].Selected = true
	}
	s.mu.Lock()
	runs := append([]RunInfo(nil), s.runs...)
	running := s.running
	s.mu.Unlock()
	var last RunInfo
	if len(runs) > 0 {
		last = runs[0]
	}
	next := time.Time{}
	if !last.Start.IsZero() {
		next = last.Start.Add(last.Took).Add(s.cfg.Every)
	}
	tabs := make([]map[string]any, 0, len(categories))
	for _, c := range categories {
		tabs = append(tabs, map[string]any{"Key": c.Key, "Name": c.Name, "N": counts[c.Key], "On": c.Key == view})
	}
	s.render(w, "index.html", map[string]any{
		"Rows":       rows,
		"Tabs":       tabs,
		"View":       view,
		"Q":          q,
		"All":        all,
		"Last":       last,
		"Next":       next,
		"Running":    running,
		"Pending":    pending,
		"Claude":     s.claudeInfo(),
		"ClaudeOK":   s.claudeOK(now),
		"Routine":    s.cfg.Routine,
		"Queued":     len(s.queue(0)),
		"Every":      strings.TrimSuffix(strings.TrimSuffix(s.cfg.Every.String(), "0s"), "0m"),
		"Google":     s.googleEmail(),
		"CanConnect": s.google.CanConnect(),
	})
}

func (s *Server) render(w http.ResponseWriter, name string, data map[string]any) {
	data["Base"] = s.cfg.Base
	data["Version"] = s.cfg.Version
	var buf bytes.Buffer
	if err := s.tmpls[name].ExecuteTemplate(&buf, name, data); err != nil {
		log.Printf("Snap template error: %v", err)
		http.Error(w, "Snap couldn't draw this page: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buf.Bytes())
}

func back(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Requested-With") == "fetch" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	to := r.Header.Get("Referer")
	if to == "" {
		to = "./"
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	s.runNow()
	back(w, r)
}

func (s *Server) known(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if _, ok := s.store.Get(id); !ok {
		http.NotFound(w, r)
		return "", false
	}
	return id, true
}

func (s *Server) handleDone(w http.ResponseWriter, r *http.Request) {
	id, ok := s.known(w, r)
	if !ok {
		return
	}
	done := r.FormValue("done") != "0"
	now := s.now()
	s.store.Update(id, func(t *Thread) {
		t.Done = done
		if done {
			t.DoneAt = now
		}
	})
	back(w, r)
}

func (s *Server) handleSnooze(w http.ResponseWriter, r *http.Request) {
	id, ok := s.known(w, r)
	if !ok {
		return
	}
	days, _ := strconv.Atoi(r.FormValue("days"))
	until := time.Time{}
	if days > 0 {
		until = s.now().AddDate(0, 0, days)
	}
	s.store.Update(id, func(t *Thread) { t.SnoozeUntil = until })
	back(w, r)
}

func (s *Server) handleTick(w http.ResponseWriter, r *http.Request) {
	id, ok := s.known(w, r)
	if !ok {
		return
	}
	i, err := strconv.Atoi(r.PathValue("i"))
	if err != nil || i < 0 {
		http.Error(w, "bad action", http.StatusBadRequest)
		return
	}
	done := r.FormValue("done") != "0"
	s.store.Update(id, func(t *Thread) {
		if t.Ticked == nil {
			t.Ticked = map[int]bool{}
		}
		if done {
			t.Ticked[i] = true
		} else {
			delete(t.Ticked, i)
		}
	})
	back(w, r)
}

// handleReread puts the thread back in Claude's queue (the routine reads it
// on its next run).
func (s *Server) handleReread(w http.ResponseWriter, r *http.Request) {
	id, ok := s.known(w, r)
	if !ok {
		return
	}
	s.store.Update(id, func(t *Thread) { t.ClaudeHistory = "" })
	s.runNow()
	back(w, r)
}

func (s *Server) claudeInfo() ClaudeInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claude
}

// claudeOK: the routine has been round in the last 7 hours (it runs every 6).
func (s *Server) claudeOK(now time.Time) bool {
	c := s.claudeInfo()
	return now.Sub(c.LastFetch) < 7*time.Hour
}

func (s *Server) googleEmail() string {
	email, _ := s.google.Connected()
	return email
}

func (s *Server) redirectURI() string {
	base := strings.TrimRight(s.cfg.PublicURL, "/")
	if base == "" {
		base = "https://scan.sreday.com"
	}
	return base + s.cfg.Base + "/admin/google/callback"
}

// handleConnect: Connect Google sends the browser to Google's consent page
// (Gmail read-only, for mark@sreday.com).
func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	if !s.google.CanConnect() {
		http.Error(w, "SNAP_GOOGLE_CLIENT_ID / SNAP_GOOGLE_CLIENT_SECRET aren't set", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, s.google.AuthURL(s.redirectURI(), s.google.State(s.now()), s.cfg.Account), http.StatusSeeOther)
}

func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	if e := r.FormValue("error"); e != "" {
		http.Error(w, "Google said: "+e, http.StatusBadRequest)
		return
	}
	if !s.google.ValidState(r.FormValue("state"), s.now()) {
		http.Error(w, "This sign-in link expired: go back to Snap and click Connect Google again.", http.StatusBadRequest)
		return
	}
	email, err := s.google.Exchange(r.FormValue("code"), s.redirectURI(), s.cfg.Account)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	log.Printf("Snap: Google connected as %s", email)
	s.runNow()
	http.Redirect(w, r, s.cfg.Base+"/", http.StatusSeeOther)
}
