// Package snap (Snap by Badger) finds the loose ends in mark@sreday.com's
// mail: a sponsor who'll "think about it", a speaker who cancelled but is
// still on the lineup, a host who said "let's touch base in 6 months". Snout
// hosts it at scan.sreday.com/snap, the way it hosts Snoop at /snoop.
//
// Every 6 hours Snout reads Gmail with Snoop's Google token (free), drops
// what can't be a loose end by rules (robots, newsletters, nobody replied,
// too old: 30 days back, a year for sponsorship), and sends Claude only the
// threads that changed since it last read them, as short excerpts. Without
// ANTHROPIC_API_KEY the rules alone run: unanswered threads, by category.
package snap

import (
	"bytes"
	"context"
	"crypto/subtle"
	"embed"
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
// login and Snoop's Google token; the rest comes from the environment.
type Config struct {
	Base          string // /snap (Snout strips it)
	DataDir       string // Snap keeps everything in DataDir/snap
	AdminUser     string
	AdminPassword string
	Version       string

	GoogleToken func() (string, error) // Snoop's access token for mark@sreday.com
	Account     string                 // mark@sreday.com, for the Open in Gmail links

	APIKey      string        // ANTHROPIC_API_KEY; empty = rules only
	Model       string        // SNAP_MODEL
	Every       time.Duration // SNAP_EVERY
	MaxPerRun   int           // SNAP_MAX_PER_RUN: threads Claude reads per run at most
	TeamDomains []string
	Repos       []string // site repos, for lineups and sponsor domains
	TZ          string
}

// ConfigFromEnv reads Snap's own settings.
func ConfigFromEnv() Config {
	every, err := time.ParseDuration(os.Getenv("SNAP_EVERY"))
	if err != nil || every < 10*time.Minute {
		every = 6 * time.Hour
	}
	max, err := strconv.Atoi(os.Getenv("SNAP_MAX_PER_RUN"))
	if err != nil || max <= 0 {
		max = 400
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
		APIKey:      os.Getenv("ANTHROPIC_API_KEY"),
		Model:       envOr("SNAP_MODEL", "claude-haiku-5-5"),
		Every:       every,
		MaxPerRun:   max,
		TeamDomains: domains,
		Repos:       sites.Repos(),
		TZ:          envOr("SNOOP_TZ", "Europe/London"),
		Account:     strings.ToLower(envOr("SNOOP_ACCOUNT", "mark@sreday.com")),
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// RunInfo is one sync, for the Claude menu.
type RunInfo struct {
	Start   time.Time     `json:"start"`
	Took    time.Duration `json:"took"`
	Listed  int           `json:"listed"`  // threads Gmail returned
	Changed int           `json:"changed"` // new or moved since the last run
	Read    int           `json:"read"`    // threads Claude (or the rules) read
	Left    int           `json:"left"`    // still waiting for Claude (over the per-run cap)
	By      string        `json:"by"`      // model or "rules"
	Usage   Usage         `json:"usage"`
	Cost    float64       `json:"cost"` // dollars, -1 unknown
	Error   string        `json:"error,omitempty"`
}

// Server is Snap.
type Server struct {
	cfg      Config
	tmpls    map[string]*template.Template
	store    *Store
	gmail    *Gmail
	analyzer Analyzer
	lineups  *Lineups
	tz       *time.Location
	now      func() time.Time

	kick   chan struct{}
	syncMu sync.Mutex

	mu       sync.Mutex
	running  bool
	runs     []RunInfo // newest first, at most 20
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
		"money": func(f float64) string {
			if f < 0 {
				return "unknown"
			}
			if f < 0.01 {
				return "under 1 cent"
			}
			return fmt.Sprintf("$%.2f", f)
		},
		"num": func(n int64) string {
			switch {
			case n >= 1e6:
				return fmt.Sprintf("%.1fM", float64(n)/1e6)
			case n >= 1e3:
				return fmt.Sprintf("%.1fk", float64(n)/1e3)
			}
			return strconv.FormatInt(n, 10)
		},
		"plus": func(a, b int) int { return a + b },
		"fdate": func(s string) string {
			d, err := time.Parse("2006-01-02", s)
			if err != nil {
				return s
			}
			return d.Format("Mon 2 Jan 2006")
		},
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
	s := &Server{
		cfg:     cfg,
		tmpls:   tmpls,
		store:   store,
		lineups: &Lineups{Repos: cfg.Repos},
		tz:      tz,
		now:     time.Now,
		kick:    make(chan struct{}, 1),
	}
	if cfg.GoogleToken != nil {
		s.gmail = NewGmail(cfg.GoogleToken)
	}
	if cfg.APIKey != "" {
		s.analyzer = NewClaude(cfg.APIKey, cfg.Model)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "runs.json")); err == nil {
		json.Unmarshal(data, &s.runs)
	}
	return s, nil
}

// Start begins the 6-hourly sync; the first one a minute after start.
func (s *Server) Start() {
	if s.cfg.APIKey == "" {
		log.Printf("Snap: ANTHROPIC_API_KEY not set: rules only, no summaries")
	}
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

// Sync reads Gmail, has Claude read what changed, and checks lineups.
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
	run := RunInfo{Start: now, By: "rules"}
	if s.analyzer != nil {
		run.By = s.analyzer.Model()
	}
	err := s.sync(ctx, now, &run)
	if err != nil {
		run.Error = err.Error()
		log.Printf("Snap: %v", err)
	}
	run.Took = s.now().Sub(now).Round(time.Second)
	run.Cost = 0
	if s.analyzer != nil {
		run.Cost = run.Usage.Cost(run.By)
	}
	s.mu.Lock()
	s.runs = append([]RunInfo{run}, s.runs...)
	if len(s.runs) > 20 {
		s.runs = s.runs[:20]
	}
	data, _ := json.Marshal(s.runs)
	s.mu.Unlock()
	os.WriteFile(filepath.Join(s.cfg.DataDir, "snap", "runs.json"), data, 0o600)
	return run
}

func (s *Server) sync(ctx context.Context, now time.Time, run *RunInfo) error {
	if s.gmail == nil {
		return fmt.Errorf("no Google token: connect Google in Snoop")
	}
	refs, err := s.gmail.List(searchQuery)
	run.Listed = len(refs)
	if err != nil {
		return err
	}
	known := s.store.HistoryIDs()
	doms := s.sponsorDomains(now)

	// 1. Headers and snippets of every new or moved thread (free).
	for _, ref := range refs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if h, ok := known[ref.ID]; ok && h == ref.HistoryID {
			continue
		}
		run.Changed++
		p, err := s.gmail.Thread(ref.ID, false)
		if err != nil {
			return fmt.Errorf("reading thread %s: %w", ref.ID, err)
		}
		m := Look(p, now, s.cfg.TeamDomains, doms)
		s.store.Update(ref.ID, func(t *Thread) { applyMeta(t, p, m) })
	}

	// 2. Threads past their window (30 days, a year for sponsorship)
	// step aside, unless a follow-up date keeps them.
	s.store.UpdateAll(func(t *Thread) { expire(t, now) })

	// 3. Claude reads what changed, newest first, up to the cap.
	var pending []Thread
	for _, t := range s.store.All() {
		if t.Pending() {
			pending = append(pending, t)
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].LastAt.After(pending[j].LastAt) })
	if len(pending) > s.cfg.MaxPerRun {
		run.Left = len(pending) - s.cfg.MaxPerRun
		pending = pending[:s.cfg.MaxPerRun]
	}
	var firstErr error
	type item struct {
		p       *ParsedThread
		m       Meta
		excerpt string
	}
	var items []item
	for _, t := range pending {
		p, err := s.gmail.Thread(t.ID, true)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		m := Look(p, now, s.cfg.TeamDomains, doms)
		s.store.Update(t.ID, func(t *Thread) { applyMeta(t, p, m) })
		if m.Skip != "" {
			continue
		}
		items = append(items, item{p, m, Excerpt(p, m)})
	}
	if s.analyzer == nil {
		for _, it := range items {
			a := Rules(it.p, it.m)
			s.store.Update(it.p.ID, func(t *Thread) { applyAnalysis(t, a, it.p.HistoryID, "rules", now) })
			run.Read++
		}
	} else {
		excerpts := make([]string, len(items))
		for i, it := range items {
			excerpts[i] = it.excerpt
		}
		today := now.In(s.tz).Format("Monday 2 January 2006")
		for _, group := range batches(excerpts, 6, 24000) {
			var ex []string
			for _, i := range group {
				ex = append(ex, excerpts[i])
			}
			res, u, err := s.analyzer.Analyze(ctx, today, ex)
			run.Usage.add(u)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				for _, i := range group {
					id := items[i].p.ID
					s.store.Update(id, func(t *Thread) { t.Error = err.Error() })
				}
				continue
			}
			byID := map[string]Analysis{}
			for _, a := range res {
				byID[a.ID] = a
			}
			for _, i := range group {
				it := items[i]
				a, ok := byID[it.p.ID]
				if !ok {
					continue // left pending: next run tries again
				}
				s.store.Update(it.p.ID, func(t *Thread) { applyAnalysis(t, a, it.p.HistoryID, s.analyzer.Model(), now) })
				run.Read++
			}
		}
	}
	s.store.UpdateAll(func(t *Thread) { expire(t, now) })

	// 4. Speakers who cancelled: still on a lineup?
	lineups, lerr := s.lineups.Get(now)
	if lerr == nil || len(lineups) > 0 {
		s.store.UpdateAll(func(t *Thread) {
			if t.CancelName == "" || t.Skip != "" {
				return
			}
			t.StillListed = StillListed(lineups, t.CancelName, t.CancelEvent)
			t.ListedAt = now
		})
	}
	if firstErr != nil {
		return firstErr
	}
	return lerr
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
	var total float64
	for _, rr := range runs {
		if rr.Cost > 0 {
			total += rr.Cost
		}
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
		"Rows":     rows,
		"Tabs":     tabs,
		"View":     view,
		"Q":        q,
		"All":      all,
		"Last":     last,
		"Runs":     runs,
		"Total":    total,
		"Next":     next,
		"Running":  running,
		"Pending":  pending,
		"Claude":   s.analyzer != nil,
		"Model":    s.cfg.Model,
		"Every":    strings.TrimSuffix(strings.TrimSuffix(s.cfg.Every.String(), "0s"), "0m"),
		"HasGmail": s.gmail != nil,
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

// handleReread has Claude read the thread again on the next run (now).
func (s *Server) handleReread(w http.ResponseWriter, r *http.Request) {
	id, ok := s.known(w, r)
	if !ok {
		return
	}
	s.store.Update(id, func(t *Thread) { t.AnalyzedHistory = "" })
	s.runNow()
	back(w, r)
}
