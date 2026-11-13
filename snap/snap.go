// Package snap (Snap by Badger) finds the loose ends in mark@sreday.com's
// mail: a sponsor who'll "think about it", a speaker who cancelled but is
// still on the lineup, a host who said "let's touch base in 6 months". Snout
// hosts it at scan.sreday.com/snap, the way it hosts Snoop at /snoop.
//
// Snout does the free work: every 15 minutes it reads the Primary tab with
// Snap's own Google token (a year back), drops what can't matter by rules
// (robots, newsletters, calendar answers, nobody replied), checks lineups,
// and queues the changed, ripe threads as short excerpts. Claude does the
// reading on Marek's subscription: a Claude Code routine fetches the queue
// (GET /snap/api/queue) every hour from 06:20 to 22:20 UTC and posts its
// read back (POST /snap/api/answers). Only what Claude flags is shown.
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
	HotEvery    time.Duration // SNAP_HOT_EVERY: how often the newest mail is looked at (hot.go)
	Routine     string        // SNAP_ROUTINE: the Claude routine's page, for the menu
	TeamDomains []string

	DraftRoutine string   // SNAP_DRAFT_ROUTINE: trig_ id of "Snap: draft a reply" (draft.go)
	DraftToken   string   // SNAP_DRAFT_TOKEN: its API trigger token
	Repos        []string // site repos, for lineups and sponsor domains
	TZ           string
}

// QueueSize is how many threads one GET /api/queue hands out; the routine
// asks again until the queue is empty.
const QueueSize = 25

// ConfigFromEnv reads Snap's own settings.
func ConfigFromEnv() Config {
	every, err := time.ParseDuration(os.Getenv("SNAP_EVERY"))
	if err != nil || every < 10*time.Minute {
		every = 15 * time.Minute
	}
	hot, err := time.ParseDuration(os.Getenv("SNAP_HOT_EVERY"))
	if err != nil || hot < 30*time.Second {
		hot = time.Minute
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
		HotEvery:     hot,
		Routine:      envOr("SNAP_ROUTINE", "https://claude.ai/code/routines"),
		TeamDomains:  domains,
		Repos:        sites.Repos(),
		TZ:           envOr("SNAP_TZ", "Europe/London"),
		Account:      strings.ToLower(envOr("SNAP_ACCOUNT", "mark@sreday.com")),
		ClientID:     os.Getenv("SNAP_GOOGLE_CLIENT_ID"),
		ClientSecret: os.Getenv("SNAP_GOOGLE_CLIENT_SECRET"),
		DraftRoutine: os.Getenv("SNAP_DRAFT_ROUTINE"),
		DraftToken:   os.Getenv("SNAP_DRAFT_TOKEN"),
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
	Fires       int       `json:"fires,omitempty"`     // times Snout started the routine for fresh mail
	LastFire    time.Time `json:"last_fire,omitempty"` // the last such start
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
	runStart time.Time // when the running Gmail read began
	runs     []RunInfo // newest first, at most 20
	claude   ClaudeInfo
	excerpts map[string]cached // thread id -> its excerpt for the queue (memory only)
	moves    []Move            // Marek's hand sorting, oldest first (sorting.go)
	fireBase string            // the routines' fire URL (tests point it elsewhere)
	draftGap time.Duration     // how often watchDraft looks
	briefs   []Brief           // what Marek typed for Claude (learning.go)
	facts    map[string]Facts  // Claude's background checks per company
	composes []Compose         // "draft something new" jobs (compose.go)
	sends    []Send            // Claude's drafts and what Mark sent (style.go)
	style    Style             // Mark's style notes, learned from his sends
	spDoms   map[string]bool
	spDomsAt time.Time
	lastFire time.Time // when Snout last started the reading routine (hot.go)
	picks    []Pick    // what Marek chose on each card (tuning.go)
	tuning   Tuning    // why the options missed, and the rules that came out of it
	tuneUpTo time.Time // the newest miss in the tuning job last handed out
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
		fireBase: fireURL,
		draftGap: draftLookGap,
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
	s.loadMoves()
	s.loadComposes()
	s.loadLearning()
	s.loadStyle()
	s.rereadFlagged("2026-11-12-options")
	s.rereadFlagged("2026-11-12-topics")
	s.rereadFlagged("2026-11-12-between") // "Ball between Rod and Kyndryl"
	s.recheckBounces("2026-11-13-bounces")
	s.loadTuning()
	s.loadTook()
	s.rereadFlagged("2026-11-13-hot") // three tabs, fresh mail flagged at once
	setMine(cfg.Account, cfg.TeamDomains)
	s.recheckRecent("2026-11-13-forms") // form leads and Mark's other addresses
	s.recheckRecent("2026-11-13-speaker-forms")
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
	go s.keepRecentDrafts()
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
	// The fast lane: a look at the newest mail every minute (hot.go).
	go func() {
		time.Sleep(3 * time.Minute) // after the first full read has begun
		every := s.cfg.HotEvery
		if every <= 0 {
			every = time.Minute
		}
		for {
			s.syncRecent(context.Background())
			time.Sleep(every)
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
	s.running, s.runStart = true, s.now()
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
	listed := make(map[string]bool, len(refs))
	for _, r := range refs {
		listed[r.ID] = true
	}
	s.store.Prune(listed)
	doms := s.sponsorDomains(now)

	// 1. Headers and snippets of every new or moved thread.
	changed, replied, firstErr := s.readChanged(ctx, refs, now, doms)
	run.Changed = changed
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if changed == 0 && firstErr != nil {
		return firstErr // ten failures in a row: Gmail is down
	}
	for _, id := range replied {
		s.updateTopic(id, reopen)
	}
	// 2. Threads past their window (30 days, a year for sponsorship) step
	// aside, unless a follow-up date keeps them.
	s.store.UpdateAll(func(t *Thread) { expire(t, now) })

	// Open loose ends whose related threads moved go back to Claude (topics.go).
	s.markRelated()

	// 3. Every thread waiting for Claude: its excerpt for the queue, and the
	// rules' read to show meanwhile.
	if err := s.fillQueue(now, doms); err != nil && firstErr == nil {
		firstErr = err
	}
	run.Queued = len(s.queue(0))

	// 4. Speakers who cancelled: still on a lineup?
	s.checkLineups(now)

	// 5. Claude's drafts that went out: what Mark changed (style.go).
	s.checkSends()

	// Fresh mail this read found doesn't wait for the minute's look.
	s.fireIfHot(now)
	return firstErr
}

// readChanged reads the headers and snippets of every listed thread that is
// new or moved since Snap last saw it. Saved every 200, so a first read of a
// year of mail doesn't rewrite the file thousands of times, and an
// interrupted one keeps what it read. A thread that fails is tried again
// next time; ten failures in a row stop the read. replied are the snoozed
// or closed threads someone answered.
func (s *Server) readChanged(ctx context.Context, refs []ThreadRef, now time.Time, doms map[string]bool) (changed int, replied []string, firstErr error) {
	known := s.store.HistoryIDs()
	fails, staged := 0, 0
	sent := map[string]time.Time{} // threads Mark has just answered
	for _, ref := range refs {
		if ctx.Err() != nil {
			break
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
				break
			}
			continue
		}
		fails = 0
		changed++
		m := Look(p, now, s.cfg.TeamDomains, doms)
		reopened := false
		var sentAt time.Time
		s.store.Stage(ref.ID, func(t *Thread) {
			if !t.LastAt.IsZero() && m.LastAt.After(t.LastAt) && m.LastFrom == "me" {
				sentAt = m.LastAt // Mark's own new message
			}
			reopened = applyMeta(t, p, m)
		})
		if reopened {
			replied = append(replied, ref.ID)
		}
		if !sentAt.IsZero() {
			sent[ref.ID] = sentAt
		}
		if staged++; staged%200 == 0 {
			s.store.Save()
		}
	}
	if staged > 0 {
		s.store.Save()
	}
	// Mark answered: the whole topic parks in Snoozed, wherever in it he wrote (sent.go).
	for id, at := range sent {
		s.updateTopic(id, func(t *Thread) { parkSent(t, at) })
	}
	return changed, replied, firstErr
}

// fillQueue readies every thread waiting for Claude: its excerpt for the
// queue, and the rules' read meanwhile.
func (s *Server) fillQueue(now time.Time, doms map[string]bool) error {
	var firstErr error
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
	return firstErr
}

func (s *Server) checkLineups(now time.Time) {
	lineups, err := s.lineups.Get(now)
	if err != nil && len(lineups) == 0 {
		log.Printf("Snap: %v", err)
		return
	}
	s.store.UpdateAll(func(t *Thread) {
		if t.CancelName == "" || t.Skip != "" || now.Sub(t.LastAt) < ripeSpeaker { // 48 hours: Marek may fix the lineup himself
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
	all, now := s.store.All(), s.now()
	// Money first: sponsor and hosting threads of any age, then the last
	// 30 days, then the rest; the newest first within each.
	tier := func(t Thread) int {
		switch {
		case t.FreshInbound(now):
			return -1 // the fast lane: fresh mail from a person
		case t.ByClaude() && t.ClaudeHistory == "":
			return 0 // Marek asked for another read
		case t.Category == "sponsors" || t.Category == "events" || t.Sponsor:
			return 0
		case now.Sub(t.LastAt) <= 30*24*time.Hour:
			return 1
		}
		return 2
	}
	sort.Slice(all, func(i, j int) bool {
		if a, b := tier(all[i]), tier(all[j]); a != b {
			return a < b
		}
		return all[i].LastAt.After(all[j].LastAt)
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []QueueItem
	for _, t := range all {
		if !t.Queued(now) {
			continue
		}
		c, ok := s.excerpts[t.ID]
		if !ok || c.history != t.HistoryID {
			continue
		}
		out = append(out, QueueItem{t.ID, t.HistoryID, s.domainHintLocked(t.People) + s.threadContextLocked(t) + s.relatedBlock(t, all) + c.excerpt})
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
		"instructions": Instructions + s.tuningBlock() + s.eventsList(now) + s.sortingLessons() + s.briefLessons(),
		"threads":      items,
		"remaining":    len(all) - len(items),
		"style":        s.styleJob(),
		"tuning":       s.tuningJob(),
	})
}

// handleAnswers: POST /api/answers takes Claude's read. An answer for a
// thread that moved on since (new mail) is dropped; the thread is queued again.
func (s *Server) handleAnswers(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Model   string       `json:"model"`
		Threads []Analysis   `json:"threads"`
		Style   *StyleAnswer `json:"style"`
		Tuning  *TuneAnswer  `json:"tuning"`
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
		s.saveFacts(a.Company, a.CompanyFacts)
		applied++
	}
	learnt := s.applyStyle(in.Style)
	tuned := s.applyTuning(in.Tuning)
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
	json.NewEncoder(w).Encode(map[string]int{"applied": applied, "stale": stale, "unknown": unknown, "remaining": len(s.queue(0)), "style_learnt": learnt, "tuning_learnt": tuned})
}

func (s *Server) saveClaudeLocked() {
	data, _ := json.Marshal(s.claude)
	os.WriteFile(filepath.Join(s.dir, "claude.json"), data, 0o600)
}

// applyMeta copies what Gmail says. A new message from someone else on a
// thread Marek snoozed or closed brings it back to Active (Marek
// 2026-11-12: "If I move threads to Snoozed, and then people reply, make
// them jump back into Active again"); his own sends don't. It reports
// whether it did, so the rest of the topic comes back too.
func applyMeta(t *Thread, p *ParsedThread, m Meta) (reopened bool) {
	moved := !t.LastAt.IsZero() && m.LastAt.After(t.LastAt) && m.LastFrom != "me"
	sent := !t.LastAt.IsZero() && m.LastAt.After(t.LastAt) && m.LastFrom == "me"
	t.Subject, t.People, t.Messages = p.Subject, m.People, len(p.Mails)
	t.HasDraft = p.HasDraft
	if t.Subject == "" {
		t.Subject = "(no subject)"
	}
	t.FirstAt, t.LastAt, t.LastFrom = m.FirstAt, m.LastAt, m.LastFrom
	t.HistoryID, t.Internal, t.Sponsor, t.Skip, t.Snippet = p.HistoryID, m.Internal, m.Sponsor, m.Skip, m.Snippet
	t.Form = m.Form
	if t.AnalyzedHistory == "" {
		t.Waiting = m.Waiting
	}
	if moved {
		reopened = t.Done || t.DeadEnd || !t.SnoozeUntil.IsZero() || t.FollowUp != ""
		t.Done, t.DeadEnd, t.SnoozeUntil = false, false, time.Time{}
		t.FollowUp = "" // a "come back on" date is answered by the reply; Claude sets a new one
		t.SentAt = time.Time{}
	}
	if sent && t.Flagged() {
		// Mark answered: the card parks in Snoozed (sent.go). Claude's read
		// stays on it, and Claude reads the thread again when it wakes.
		parkSent(t, m.LastAt)
		t.AnalyzedHistory = p.HistoryID
	}
	// A bounce on Mark's latest mail: nobody will answer, so a dead end
	// (once per bounce, so Marek can still bring it back by hand).
	if !m.Bounced.IsZero() && m.Bounced.After(t.BouncedAt) {
		t.BouncedAt, t.DeadEnd = m.Bounced, true
		reopened = false
	}
	return reopened
}

// reopen brings back a whole topic after a reply on one of its threads.
func reopen(t *Thread) { t.Done, t.DeadEnd, t.SnoozeUntil, t.FollowUp = false, false, time.Time{}, "" }

func applyAnalysis(t *Thread, a Analysis, history, by string, now time.Time) {
	t.AnalyzedHistory, t.AnalyzedAt, t.AnalyzedBy, t.Error = history, now, by, ""
	if by != "rules" {
		t.ClaudeHistory = history
		t.SentAt = time.Time{} // a fresh read: the parked-by-a-send state is over
	}
	t.Category, t.Loose, t.Label, t.Why, t.Summary = a.Category, a.Loose, a.Label, a.Why, a.Summary
	if t.Form == "speaker" {
		t.Category = "speakers" // a Fast Track or Waitlist form always sits in Speakers
	}
	if !validCategory(t.Category) {
		t.Category = "events" // no Other tab: what's neither money nor a talk
	}
	if t.MovedTo != "" {
		t.Category = t.MovedTo
	}
	t.Waiting, t.FollowUp, t.Priority, t.Company, t.Event = a.Waiting, a.FollowUp, a.Priority, a.Company, a.Event
	t.Between = ""
	if t.Waiting == "between" {
		t.Between = clip(strings.TrimSpace(a.Between), 60)
	}
	if by != "rules" {
		t.Topic = strings.TrimSpace(a.Topic)
	}
	if _, err := time.Parse("2006-01-02", t.FollowUp); err != nil {
		t.FollowUp = ""
	}
	t.Actions, t.Ticked = a.Actions, nil
	t.CancelName, t.CancelEvent = strings.TrimSpace(a.CancelName), strings.TrimSpace(a.CancelEvent)
	if t.CancelName == "" {
		t.StillListed, t.ListedAt = "", time.Time{}
	}
	fixCard(t)
	if !t.Loose {
		t.Label, t.Actions = "", nil
	}
}

// expire: threads step aside after a year, except a loose end Claude
// flagged (it waits for Marek's Done or Dead end), a lineup problem, or a
// follow-up date (kept until a month after it).
func expire(t *Thread, now time.Time) {
	if t.Skip != "" && t.Skip != "older than a year" {
		return
	}
	keep := now.Sub(t.LastAt) <= longWindow || t.Flagged() || t.StillListed != ""
	if d, ok := t.FollowUpDate(); ok && now.Before(d.AddDate(0, 1, 0)) {
		keep = true
	}
	switch {
	case !keep && t.Skip == "":
		t.Skip = "older than a year"
	case keep && t.Skip == "older than a year" && t.AnalyzedHistory != "":
		t.Skip = ""
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
	mux.HandleFunc("POST /admin/thread/{id}/deadend", s.requireAdmin(s.handleDeadEnd))
	mux.HandleFunc("POST /admin/thread/{id}/move", s.requireAdmin(s.handleMove))
	mux.HandleFunc("POST /admin/thread/{id}/state", s.requireAdmin(s.handleState))
	mux.HandleFunc("GET /admin/activity", s.requireAdmin(s.handleActivity))
	mux.HandleFunc("POST /admin/compose", s.requireAdmin(s.handleCompose))
	mux.HandleFunc("POST /admin/thread/{id}/draft", s.requireAdmin(s.handleDraft))
	mux.HandleFunc("GET /admin/thread/{id}/draft", s.requireAdmin(s.handlePreview))
	mux.HandleFunc("POST /admin/thread/{id}/draft/correct", s.requireAdmin(s.handleCorrect))
	mux.HandleFunc("POST /admin/thread/{id}/snooze", s.requireAdmin(s.handleSnooze))
	mux.HandleFunc("POST /admin/thread/{id}/tick/{i}", s.requireAdmin(s.handleTick))
	mux.HandleFunc("POST /admin/thread/{id}/reread", s.requireAdmin(s.handleReread))
	mux.HandleFunc("POST /admin/thread/{id}/ask", s.requireAdmin(s.handleAsk))
	mux.HandleFunc("GET /admin/thread/{id}/qa", s.requireAdmin(s.handleQA))
	mux.HandleFunc("POST /admin/thread/{id}/qa/{qid}/apply", s.requireAdmin(s.handleApply))
	mux.HandleFunc("POST /admin/qq-token", s.requireAdmin(s.handleQQToken))
	mux.HandleFunc("POST /admin/thread/{id}/enrich", s.requireAdmin(s.handleEnrich))
	mux.HandleFunc("POST /admin/read-token", s.requireAdmin(s.handleReadToken))
	mux.HandleFunc("POST /admin/tuning/forget", s.requireAdmin(s.handleForgetRule))
	mux.HandleFunc("GET /admin/google/connect", s.requireAdmin(s.handleConnect))
	mux.HandleFunc("GET /admin/google/callback", s.requireAdmin(s.handleCallback))
	mux.HandleFunc("GET /api/queue", s.requireToken(s.handleQueue))
	mux.HandleFunc("POST /api/answers", s.requireToken(s.handleAnswers))
	mux.HandleFunc("GET /api/qa", s.requireToken(s.handleQAQueue))
	mux.HandleFunc("GET /api/search", s.requireToken(s.handleSearch))
	mux.HandleFunc("POST /api/qa", s.requireToken(s.handleQAAnswers))
	return mux
}

var categories = []struct{ Key, Name string }{
	{"sponsors", "Sponsors"}, {"speakers", "Speakers"}, {"events", "Events"},
}

// Row is a thread as the page shows it.
type Row struct {
	Thread
	State     string // open, due, later, snoozed, done
	Gmail     string
	Prompts   []string     // per action: what Copy for Claude copies
	Drafting  bool         // Claude is writing a draft (draft.go)
	Fixing    bool         // Claude is saving Mark's corrected draft (preview.go)
	FixFailed bool         // the correction did not reach Gmail
	BallClass string       // you, them, between (rowline.go)
	BallText  string       // "Ball in your park", "Aleksandra <> Raymond"
	Conf      string       // the conference, PEC for short
	Neon      template.CSS // the glow behind the picked row, in the conference's colours
	Headline  string       // the real deal in a few words (Claude's label); the subject goes underneath
	Status    string       // where it stands, briefly
	Bucket    string       // active, closed, snoozed
	Segs      []Seg        // the wheel's arcs, one per option
	Siblings  []Sibling    // the topic's other emails (topics.go)
	Fresh     time.Time    // the topic's newest message (ranking)
	Hot       bool         // fresh, ball with Mark, waiting for room on the Hot plate
	Sep       bool         // the first card after the tab's top block
	Selected  bool
}

func (s *Server) row(t Thread, now time.Time) Row {
	fixCard(&t) // SREday, LLMday, PLATFORMday, Conf42, PEC: always spelled right
	r := Row{Thread: t, Gmail: "https://mail.google.com/mail/u/?authuser=" + s.cfg.Account + "#all/" + t.ID}
	switch {
	case t.DeadEnd:
		r.State = "dead"
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
	r.Drafting = t.Drafting(now)
	r.BallClass, r.BallText = ball(t)
	r.Conf = confName(t.Event)
	r.Neon = neonStyle(t)
	r.Fixing, r.FixFailed = t.Fix.Saving(now), t.Fix.Status == "failed" && now.Sub(t.Fix.At) < time.Hour
	r.Bucket = t.Bucket(now)
	r.Segs = wheelSegs(len(t.Actions) >= 2, len(t.Actions) >= 3, s.CanDraft())
	switch {
	case t.StillListed != "" && t.CancelName != "":
		r.Headline = t.CancelName + " cancelled, talk still listed"
	case t.Label != "":
		r.Headline = t.Label
	default:
		r.Headline = t.Subject
	}
	r.Subject, r.Headline = fixBrands(r.Subject), fixBrands(r.Headline) // shown only; prompts above keep the real subject
	r.Status = t.Why
	if r.Status == "" {
		r.Status = t.Summary
	}
	return r
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
	if !validCategory(view) {
		view = "hot" // the plate: what to answer now
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	st := r.URL.Query().Get("st") // Active (default), Closed, Snoozed
	if (st != "closed" && st != "snoozed") || view == "hot" {
		st = "active"
	}
	pending := 0
	var cards, hot []Row
	all := s.store.All()
	leads := topicLeads(all) // one card per topic: the thread Claude read last speaks for it
	for _, t := range all {
		if t.Queued(now) {
			pending++
		}
		if k := normTopic(t.Topic); k != "" && t.ByClaude() && leads[k].ID != t.ID {
			continue
		}
		if !t.IsLoose() {
			continue
		}
		if !validCategory(t.Category) {
			t.Category = "events"
		}
		bucket := t.Bucket(now)
		isHot := t.Hot(now)
		if bucket != st && !isHot {
			continue
		}
		row := s.row(t, now)
		row.Siblings = s.siblings(t, all)
		for i := range row.Siblings {
			row.Siblings[i].Subject = fixBrands(row.Siblings[i].Subject)
		}
		row.Fresh = t.LastAt // the topic's newest message, on any of its threads
		for _, o := range row.Siblings {
			if o.LastAt.After(row.Fresh) {
				row.Fresh = o.LastAt
				if o.LastFrom == "you" {
					isHot = false // Mark already answered in another email of this topic
				}
			}
		}
		if isHot {
			hot = append(hot, row)
		} else if bucket == st {
			cards = append(cards, row)
		}
	}
	plate, waiting := hotPlate(hot)
	for i := range waiting {
		waiting[i].Hot = true
	}
	if st == "active" {
		cards = append(cards, waiting...) // no room on the plate yet: they sit in their own tab
	}
	var rows []Row
	switch {
	case q != "":
		// A search looks through every tab, the plate included.
		for _, row := range append(append([]Row{}, plate...), cards...) {
			if matches(row.Thread, q) {
				rows = append(rows, row)
			}
		}
		byStakes(rows)
	case view == "hot":
		rows = plate
	default:
		for _, row := range cards {
			if row.Category == view {
				rows = append(rows, row)
			}
		}
		if st == "active" {
			rows = orderTab(rows, now)
		} else {
			sort.SliceStable(rows, func(i, j int) bool { return freshOf(rows[i]).After(freshOf(rows[j])) })
		}
	}
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
	tabs := []map[string]any{{"Key": "hot", "Name": "Hot", "N": len(plate), "On": view == "hot"}}
	for _, c := range categories {
		tabs = append(tabs, map[string]any{"Key": c.Key, "Name": c.Name, "N": 0, "On": c.Key == view})
	}
	s.render(w, "index.html", map[string]any{
		"Rows":       rows,
		"Tabs":       tabs,
		"View":       view,
		"HotN":       len(plate),
		"HotMore":    len(waiting),
		"Q":          q,
		"St":         st,
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
		"CanDraft":   s.CanDraft(),
		"CanFire":    s.CanFire(),
		"Tune":       s.tuneView(),
		"Activity":   s.activities(now),
		"Log":        s.logRows(now),
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
	s.updateTopic(id, func(t *Thread) {
		t.Done = done
		if done {
			t.DoneAt = now
		}
	})
	back(w, r)
}

// handleDeadEnd: Marek gives up on a thread. Only new mail brings it back.
func (s *Server) handleDeadEnd(w http.ResponseWriter, r *http.Request) {
	id, ok := s.known(w, r)
	if !ok {
		return
	}
	dead := r.FormValue("dead") != "0"
	if t, _ := s.store.Get(id); dead {
		s.recordPick("deadend", "", &t)
	}
	s.updateTopic(id, func(t *Thread) { t.DeadEnd = dead })
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
	if d, err := time.ParseInLocation("2006-01-02", r.FormValue("until"), s.tz); err == nil && d.After(s.now()) {
		until = d // Claude's "Snooze until 15 Jan"
	}
	if t, _ := s.store.Get(id); !until.IsZero() {
		s.recordPick("snooze", "", &t)
	}
	s.updateTopic(id, func(t *Thread) { t.SnoozeUntil = until })
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

// handleState: Mark as Active, Closed or Snoozed (a week).
func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	id, ok := s.known(w, r)
	if !ok {
		return
	}
	now := s.now()
	switch r.FormValue("s") {
	case "active":
		s.updateTopic(id, func(t *Thread) {
			t.Done, t.DeadEnd, t.SnoozeUntil = false, false, time.Time{}
			if t.Later(now) {
				t.FollowUp = "" // back now, not on the follow-up date
			}
		})
	case "closed":
		s.updateTopic(id, func(t *Thread) { t.Done, t.DoneAt = true, now })
	case "snoozed":
		s.updateTopic(id, func(t *Thread) { t.Done, t.DeadEnd, t.SnoozeUntil = false, false, now.AddDate(0, 0, 7) })
	default:
		http.Error(w, "Active, Closed or Snoozed", http.StatusBadRequest)
		return
	}
	back(w, r)
}
