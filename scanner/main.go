// Command snout (Snout by Badger) is the sponsor lead scanner: sponsors scan attendees'
// badge QR codes (their LinkedIn), and each scan is matched to the event's
// Luma guest list and kept in the order the conversations happened.
package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"
)

var version = "dev"

//go:embed templates/*.html
var templateFS embed.FS

//go:embed assets/jsQR.js
var jsQR []byte

//go:embed assets/snout-logo.png
var logoPNG []byte

//go:embed assets/snout-icon.png
var iconPNG []byte

//go:embed assets/sw.js
var serviceWorker []byte

// Config holds the scanner configuration, all from the environment.
type Config struct {
	Host          string
	Port          string
	DataDir       string
	LumaAPIKey    string
	AdminUser     string
	AdminPassword string
	PublicURL     string
	FullAnswers   bool // CSV and lead view include phone + every registration answer
	SMTPAddr      string
	SMTPUser      string
	SMTPPassword  string
	ReplyTo       string
}

func loadConfig() Config {
	return Config{
		Host:          envOr("HOST", "0.0.0.0"),
		Port:          envOr("PORT", "8081"),
		DataDir:       envOr("DATA_DIR", "data"),
		LumaAPIKey:    os.Getenv("LUMA_API_KEY"),
		AdminUser:     os.Getenv("ADMIN_USER"),
		AdminPassword: os.Getenv("ADMIN_PASSWORD"),
		PublicURL:     strings.TrimRight(os.Getenv("PUBLIC_URL"), "/"),
		FullAnswers:   os.Getenv("FULL_ANSWERS") == "1" || os.Getenv("FULL_ANSWERS") == "true",
		SMTPAddr:      envOr("SMTP_ADDR", "smtp.gmail.com:587"),
		SMTPUser:      os.Getenv("SMTP_USER"),
		SMTPPassword:  os.Getenv("SMTP_PASSWORD"),
		ReplyTo:       envOr("MAIL_REPLY_TO", "mark@sreday.com"),
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
	guests *GuestCache
	secret []byte

	pinMu    sync.Mutex
	pinFails map[string]*pinFail

	siteMu     sync.Mutex
	siteSynced time.Time
	siteErr    error

	now  func() time.Time // the clock; tests move it
	mail Mailer           // nil when SMTP isn't configured

	mailMu   sync.Mutex
	mailSent map[string][]time.Time // sponsor token -> recent sends, for the rate limit
}

type pinFail struct {
	count int
	until time.Time
}

func newServer(cfg Config) (*Server, error) {
	pages := []string{"admin.html", "pin.html", "scan.html", "dash.html"}
	tmpls := make(map[string]*template.Template, len(pages))
	for _, page := range pages {
		t, err := template.ParseFS(templateFS, "templates/layout.html", "templates/"+page)
		if err != nil {
			return nil, fmt.Errorf("parsing template %s: %w", page, err)
		}
		tmpls[page] = t
	}

	store, err := OpenStore(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("opening store: %w", err)
	}
	secret, err := loadSecret(filepath.Join(cfg.DataDir, "secret"))
	if err != nil {
		return nil, fmt.Errorf("loading secret: %w", err)
	}

	if err := store.EnsurePINs(); err != nil {
		return nil, fmt.Errorf("giving links PINs: %w", err)
	}

	srv := &Server{
		cfg:      cfg,
		tmpls:    tmpls,
		store:    store,
		guests:   NewGuestCache(cfg.LumaAPIKey, 5*time.Minute),
		secret:   secret,
		pinFails: make(map[string]*pinFail),
		now:      time.Now,
		mailSent: make(map[string][]time.Time),
	}
	if cfg.SMTPUser != "" && cfg.SMTPPassword != "" {
		srv.mail = smtpMailer(cfg.SMTPAddr, cfg.SMTPUser, cfg.SMTPPassword, cfg.ReplyTo)
	}
	return srv, nil
}

// loadSecret reads the cookie signing key, creating it on first run so
// sponsor sessions survive restarts.
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
	if name == "pin.html" {
		w.Header().Set("X-Page", "pin") // the service worker doesn't keep this page
	}
	if err := s.tmpls[name].ExecuteTemplate(w, name, data); err != nil {
		log.Printf("Template error: %v", err)
	}
}

// --- Admin ---------------------------------------------------------------

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		userOK := s.cfg.AdminUser == "" || subtle.ConstantTimeCompare([]byte(user), []byte(s.cfg.AdminUser)) == 1
		if !ok || !userOK || subtle.ConstantTimeCompare([]byte(pass), []byte(s.cfg.AdminPassword)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="Snout by Badger"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) baseURL(r *http.Request) string {
	if s.cfg.PublicURL != "" {
		return s.cfg.PublicURL
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

type sponsorRow struct {
	Sponsor
	Link     string
	DashLink string // the desktop dashboard: same token and PIN
	Leads    int
}

type eventGroup struct {
	ID       string
	Name     string
	Brand    string // sreday, llmday, platformday: the card's accent
	When     string // "Mon 2 Nov 2026", in the event's time zone
	Day      string // "2"
	Month    string // "Nov"
	Phase    string
	Until    string // when the phase ends, in the event's time zone
	Sponsors []sponsorRow

	Up      bool // upcoming: sponsors can still be added
	Extra   bool // beyond the first three, shown with "Show all"
	CanMail bool
}

// siteSyncEvery is how often the admin page re-reads the sponsor lists on
// the event pages. GitHub allows 60 unauthenticated API calls an hour, and
// a sync uses one per site repo.
const siteSyncEvery = 10 * time.Minute

// syncSiteSponsors keeps a sponsor link for every sponsor listed on an
// upcoming event's page.
func (s *Server) syncSiteSponsors(events []LumaEvent, force bool) error {
	s.siteMu.Lock()
	defer s.siteMu.Unlock()
	if !force && time.Since(s.siteSynced) < siteSyncEvery {
		return s.siteErr
	}
	s.siteSynced = time.Now()
	bySite, err := ListSiteSponsors(siteRepos())
	s.siteErr = err
	// A page we couldn't read looks like "no sponsors", so only remove
	// links when every page was read.
	prune := err == nil
	for _, ev := range events {
		var names []string
		for _, sp := range bySite[ev.APIId] {
			names = append(names, sp.Name)
		}
		added, removed, err := s.store.SyncSiteSponsors(ev, names, prune)
		if err != nil {
			s.siteErr = err
			return err
		}
		if added > 0 || removed > 0 {
			log.Printf("%s: %d sponsor links added, %d removed, from the event page", ev.Name, added, removed)
		}
		if added > 0 {
			go s.guests.Index(ev.APIId)
		}
	}
	return s.siteErr
}

// recentEvents keeps events from the last week onwards, soonest first;
// sponsors aren't set up for old ones.
func recentEvents(events []LumaEvent) []LumaEvent {
	cutoff := time.Now().AddDate(0, 0, -7)
	var recent []LumaEvent
	for _, e := range events {
		if t, err := time.Parse(time.RFC3339, e.StartAt); err != nil || t.After(cutoff) {
			recent = append(recent, e)
		}
	}
	sort.SliceStable(recent, func(i, j int) bool { return recent[i].StartAt < recent[j].StartAt })
	return recent
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{}

	events, err := ListEvents(s.cfg.LumaAPIKey)
	if err != nil {
		data["Error"] = fmt.Sprintf("Failed to fetch events from Luma: %v", err)
	}
	recent := recentEvents(events)
	data["Events"] = recent

	if err == nil {
		if err := s.syncSiteSponsors(recent, false); err != nil {
			data["SiteError"] = fmt.Sprintf("Some event pages couldn't be read, so their sponsors may be missing: %v", err)
		}
	}

	base := s.baseURL(r)
	now := s.now()
	byEvent := map[string][]sponsorRow{}
	for _, sp := range s.store.Sponsors() {
		link := base + "/s/" + sp.Token
		byEvent[sp.EventID] = append(byEvent[sp.EventID], sponsorRow{Sponsor: sp, Link: link, DashLink: link + "/dashboard", Leads: s.store.LeadCount(sp.Token)})
	}
	group := func(id, name, tz string, start, end time.Time, rows []sponsorRow) eventGroup {
		sort.SliceStable(rows, func(i, j int) bool { return strings.ToLower(rows[i].Name) < strings.ToLower(rows[j].Name) })
		phase, until := phaseLabel(Sponsor{EventStart: start, EventEnd: end, EventTZ: tz}, now)
		g := eventGroup{ID: id, Name: name, Brand: brandOf(name), Phase: phase, Until: until, Sponsors: rows, CanMail: s.mail != nil}
		if !start.IsZero() {
			loc, err := time.LoadLocation(tz)
			if err != nil {
				loc = time.UTC
			}
			t := start.In(loc)
			g.When, g.Day, g.Month = t.Format("Mon 2 Jan 2006"), t.Format("2"), t.Format("Jan")
		}
		return g
	}

	// Upcoming: every Luma event that hasn't closed yet, soonest first.
	var upcoming []eventGroup
	for _, e := range recent {
		start, end := e.Times()
		if g := group(e.APIId, e.Name, e.Timezone, start, end, byEvent[e.APIId]); g.Phase == PhaseTest || g.Phase == PhaseLive {
			upcoming = append(upcoming, g)
			delete(byEvent, e.APIId)
		}
	}
	// Past: closed events that have sponsor links, most recent first; their
	// CSVs stay downloadable until the leads are deleted.
	var past []eventGroup
	for id, rows := range byEvent {
		sp := rows[0].Sponsor
		past = append(past, group(id, sp.EventName, sp.EventTZ, sp.EventStart, sp.EventEnd, rows))
	}
	sort.Slice(past, func(i, j int) bool {
		return past[i].Sponsors[0].EventStart.After(past[j].Sponsors[0].EventStart)
	})
	for i := range upcoming {
		upcoming[i].Up, upcoming[i].Extra = true, i >= 3
	}
	for i := range past {
		past[i].Extra = i >= 3
	}
	data["Upcoming"] = upcoming
	data["Past"] = past
	data["CanMail"] = s.mail != nil
	data["Flash"] = r.URL.Query().Get("msg")
	s.render(w, "admin.html", data)
}

// phaseLabel returns the phase and when it ends, formatted for people.
func phaseLabel(sp Sponsor, now time.Time) (string, string) {
	phase := sp.PhaseAt(now)
	liveFrom, closesAt, deletesAt := sp.Window()
	var until time.Time
	switch phase {
	case PhaseTest:
		until = liveFrom
	case PhaseLive:
		until = closesAt
	case PhaseClosed:
		// After the event only the day matters: "until 21 Jan 2027".
		return phase, localDate(deletesAt, sp.EventTZ)
	}
	if until.IsZero() {
		return phase, ""
	}
	return phase, localTime(until, sp.EventTZ)
}

// brandOf picks the scanner's tint from the event name: purple SREday
// (the default), green LLMday (and LLMday + PEC London), orange PLATFORMday.
func brandOf(eventName string) string {
	n := strings.ToLower(eventName)
	switch {
	case strings.Contains(n, "platformday"):
		return "platformday"
	case strings.Contains(n, "llmday") || strings.Contains(n, "prompt engineering"):
		return "llmday"
	default:
		return "sreday"
	}
}

// localTime formats t in the event's time zone, e.g. "2 Nov 22:00".
func localTime(t time.Time, tz string) string {
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		loc = time.UTC
	}
	return t.In(loc).Format("2 Jan 15:04")
}

func (s *Server) handleRefreshSites(w http.ResponseWriter, r *http.Request) {
	events, err := ListEvents(s.cfg.LumaAPIKey)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to fetch events from Luma: %v", err), http.StatusBadGateway)
		return
	}
	s.syncSiteSponsors(recentEvents(events), true) // errors show on the admin page
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) handleNewPIN(w http.ResponseWriter, r *http.Request) {
	sp, err := s.store.NewPIN(r.PathValue("token"))
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to set PIN: %v", err), http.StatusBadRequest)
		return
	}
	s.pinMu.Lock()
	delete(s.pinFails, sp.Token)
	s.pinMu.Unlock()
	http.Redirect(w, r, "/admin#"+sp.Token, http.StatusSeeOther)
}

func (s *Server) handleAddSponsor(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	eventID := r.FormValue("event_id")
	if name == "" || eventID == "" {
		http.Error(w, "Sponsor name and event are required", http.StatusBadRequest)
		return
	}
	events, err := ListEvents(s.cfg.LumaAPIKey)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to fetch events from Luma: %v", err), http.StatusBadGateway)
		return
	}
	for _, e := range events {
		if e.APIId == eventID {
			if _, err := s.store.AddSponsor(name, e); err != nil {
				http.Error(w, fmt.Sprintf("Failed to save sponsor: %v", err), http.StatusInternalServerError)
				return
			}
			// Warm the guest list so the first scan is instant.
			go s.guests.Index(eventID)
			http.Redirect(w, r, "/admin", http.StatusSeeOther)
			return
		}
	}
	http.Error(w, "Unknown event", http.StatusBadRequest)
}

func (s *Server) handleAdminExport(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.store.Sponsor(r.PathValue("token"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.writeExport(w, sp, "admin")
}

// --- Sponsor -------------------------------------------------------------

func (s *Server) sessionCookieName(sp *Sponsor) string {
	return "bs_" + sp.Token[:8]
}

func (s *Server) sessionValue(sp *Sponsor) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(sp.Token + ":" + sp.PIN))
	return hex.EncodeToString(mac.Sum(nil))
}

// sponsor resolves the token in the URL and checks the PIN cookie.
func (s *Server) sponsor(r *http.Request) (*Sponsor, bool, bool) {
	sp, ok := s.store.Sponsor(r.PathValue("token"))
	if !ok {
		return nil, false, false
	}
	c, err := r.Cookie(s.sessionCookieName(sp))
	authed := err == nil && sp.PIN != "" && hmac.Equal([]byte(c.Value), []byte(s.sessionValue(sp)))
	return sp, true, authed
}

func (s *Server) handleSponsorPage(w http.ResponseWriter, r *http.Request) {
	sp, ok, authed := s.sponsor(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	data := map[string]any{"Sponsor": sp}
	if sp.PhaseAt(s.now()) == PhaseDeleted {
		data["Deleted"] = true
		s.render(w, "pin.html", data)
		return
	}
	if !authed {
		s.render(w, "pin.html", data)
		return
	}
	phase, until := phaseLabel(*sp, s.now())
	data["Phase"], data["Until"] = phase, until
	data["CanMail"] = s.mail != nil
	data["Brand"] = brandOf(sp.EventName)
	s.render(w, "scan.html", data)
}

// handleDashboard is the desktop view of a sponsor's leads: the same link
// and PIN as the scanner, read-only, refreshing itself while it's open.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	sp, ok, authed := s.sponsor(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	data := map[string]any{"Sponsor": sp, "Next": "dashboard"}
	if sp.PhaseAt(s.now()) == PhaseDeleted {
		data["Deleted"] = true
		s.render(w, "pin.html", data)
		return
	}
	if !authed {
		s.render(w, "pin.html", data)
		return
	}
	phase, until := phaseLabel(*sp, s.now())
	data["Phase"], data["Until"] = phase, until
	data["Brand"] = brandOf(sp.EventName)
	data["Deletes"] = localDate(sp.PhaseWindowDeletes(), sp.EventTZ)
	if !sp.EventStart.IsZero() {
		data["When"] = localDate(sp.EventStart, sp.EventTZ)
	}
	s.render(w, "dash.html", data)
}

func (s *Server) handlePIN(w http.ResponseWriter, r *http.Request) {
	sp, ok, _ := s.sponsor(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	// The dashboard sends people back to itself; anything else goes to the scanner.
	next, back := "", "/s/"+sp.Token
	if r.FormValue("next") == "dashboard" {
		next, back = "dashboard", back+"/dashboard"
	}

	s.pinMu.Lock()
	f := s.pinFails[sp.Token]
	if f != nil && time.Now().Before(f.until) {
		s.pinMu.Unlock()
		s.render(w, "pin.html", map[string]any{"Sponsor": sp, "Next": next, "Error": "Too many attempts. Try again in a few minutes."})
		return
	}
	s.pinMu.Unlock()

	if sp.PIN == "" {
		s.render(w, "pin.html", map[string]any{"Sponsor": sp, "Next": next})
		return
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(r.FormValue("pin"))), []byte(sp.PIN)) != 1 {
		s.pinMu.Lock()
		if f == nil {
			f = &pinFail{}
			s.pinFails[sp.Token] = f
		}
		f.count++
		if f.count >= 5 {
			f.count = 0
			f.until = time.Now().Add(10 * time.Minute)
		}
		s.pinMu.Unlock()
		s.render(w, "pin.html", map[string]any{"Sponsor": sp, "Next": next, "Error": "Wrong PIN."})
		return
	}

	s.pinMu.Lock()
	delete(s.pinFails, sp.Token)
	s.pinMu.Unlock()
	s.store.Touch(sp.Token, s.now())

	http.SetCookie(w, &http.Cookie{
		Name:     s.sessionCookieName(sp),
		Value:    s.sessionValue(sp),
		Path:     "/s/" + sp.Token,
		MaxAge:   60 * 60 * 24 * 30,
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// LeadView is what the scanner page shows for a lead.
type LeadView struct {
	ScanID    string    `json:"scan_id"`
	ScannedAt time.Time `json:"scanned_at"`
	ScannedBy string    `json:"scanned_by"`
	Matched   bool      `json:"matched"`
	Name      string    `json:"name"`
	Title     string    `json:"title"`
	Company   string    `json:"company"`
	LinkedIn  string    `json:"linkedin"`
	Rating    string    `json:"rating"`
	Note      string    `json:"note"`
	Email     string    `json:"email,omitempty"`
	Phone     string    `json:"phone,omitempty"`
	Answers   []Answer  `json:"answers,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitzero"` // test leads only
}

func (s *Server) viewLead(l Lead, ix *GuestIndex) LeadView {
	v := LeadView{
		ScanID:    l.ScanID,
		ScannedAt: l.ScannedAt,
		ScannedBy: l.ScannedBy,
		Rating:    l.Rating,
		Note:      l.Note,
	}
	if l.Test {
		v.ExpiresAt = l.ReceivedAt.Add(testLeadTTL)
	}
	if g, ok := ix.Lookup(l.Key); ok {
		v.Matched = true
		v.Name, v.Title, v.Company, v.LinkedIn, v.Email = g.Name, g.Title, g.Company, g.LinkedIn, g.Email
		if s.cfg.FullAnswers {
			v.Phone, v.Answers = g.Phone, g.ExtraAnswers()
		}
		return v
	}
	switch l.Key.Kind {
	case "linkedin":
		v.LinkedIn = "https://www.linkedin.com/in/" + l.Key.Value
		v.Name = "linkedin.com/in/" + l.Key.Value
	case "email":
		v.Name = l.Key.Value
	default:
		v.Name = "Luma guest " + l.Key.Value
	}
	return v
}

// identity is the per-sponsor deduplication key: the Luma guest when the scan
// matched one, so a badge scan and a ticket scan of one person collapse.
func identity(k ScanKey, ix *GuestIndex) string {
	if g, ok := ix.Lookup(k); ok {
		return "guest:" + g.ID
	}
	return k.String()
}

type syncOp struct {
	Op        string    `json:"op"` // "scan" or "update"
	ScanID    string    `json:"scan_id"`
	Raw       string    `json:"raw"`
	ScannedAt time.Time `json:"scanned_at"`
	ScannedBy string    `json:"scanned_by"`
	Rating    string    `json:"rating"`
	Note      string    `json:"note"`
}

type syncResult struct {
	ScanID string    `json:"scan_id"`
	Status string    `json:"status"` // "ok", "duplicate", "unreadable", "closed", "error"
	Lead   *LeadView `json:"lead,omitempty"`
}

// handleSync applies the page's queued scans and updates, in order.
// Every op is idempotent, so the page can safely resend after a timeout.
func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	sp, ok, authed := s.sponsor(r)
	if !ok || !authed {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		Ops []syncOp `json:"ops"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	now := s.now().UTC()
	if sp.PhaseAt(now) == PhaseDeleted {
		http.Error(w, "This event's leads were deleted", http.StatusGone)
		return
	}
	s.store.Touch(sp.Token, now)
	liveFrom, closesAt, _ := sp.Window()
	// Scans made before closing still count when the phone was offline and
	// syncs late, up to a day after.
	open := closesAt.IsZero() || now.Before(closesAt.Add(lateSync))

	ix := s.guests.Index(sp.EventID)
	results := make([]syncResult, 0, len(req.Ops))
	for _, op := range req.Ops {
		if op.ScanID == "" || len(op.ScanID) > 64 {
			results = append(results, syncResult{ScanID: op.ScanID, Status: "error"})
			continue
		}
		switch op.Op {
		case "scan":
			key, ok := ParseScan(op.Raw)
			if !ok {
				results = append(results, syncResult{ScanID: op.ScanID, Status: "unreadable"})
				continue
			}
			scannedAt := op.ScannedAt.UTC()
			// Trust the device clock for ordering (scans may arrive late from
			// an offline queue), unless it is clearly wrong.
			if scannedAt.IsZero() || scannedAt.After(now.Add(5*time.Minute)) || scannedAt.Before(now.AddDate(0, 0, -7)) {
				scannedAt = now
			}
			if !open || (!closesAt.IsZero() && !scannedAt.Before(closesAt)) {
				results = append(results, syncResult{ScanID: op.ScanID, Status: "closed"})
				continue
			}
			lead, dup, err := s.store.AddScan(sp, Lead{
				// A scan made before the live window is a test scan, even
				// when it reaches us later from an offline queue.
				Test:       !liveFrom.IsZero() && scannedAt.Before(liveFrom),
				ScanID:     op.ScanID,
				ScannedAt:  scannedAt,
				ScannedBy:  truncate(op.ScannedBy, 100),
				Raw:        truncate(op.Raw, 500),
				Key:        key,
				Identity:   identity(key, ix),
				ReceivedAt: now,
			})
			if err != nil {
				log.Printf("Storing scan for %s: %v", sp.Name, err)
				results = append(results, syncResult{ScanID: op.ScanID, Status: "error"})
				continue
			}
			v := s.viewLead(lead, ix)
			status := "ok"
			if dup {
				status = "duplicate"
			}
			results = append(results, syncResult{ScanID: op.ScanID, Status: status, Lead: &v})
		case "update":
			if !open {
				results = append(results, syncResult{ScanID: op.ScanID, Status: "closed"})
				continue
			}
			lead, err := s.store.UpdateLead(sp, op.ScanID, truncate(op.Rating, 20), truncate(op.Note, 2000))
			if err != nil {
				results = append(results, syncResult{ScanID: op.ScanID, Status: "error"})
				continue
			}
			v := s.viewLead(lead, ix)
			results = append(results, syncResult{ScanID: op.ScanID, Status: "ok", Lead: &v})
		default:
			results = append(results, syncResult{ScanID: op.ScanID, Status: "error"})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]any{"results": results})
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (s *Server) handleLeads(w http.ResponseWriter, r *http.Request) {
	sp, ok, authed := s.sponsor(r)
	if !ok || !authed {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	now := s.now()
	phase := sp.PhaseAt(now)
	if phase == PhaseDeleted {
		http.Error(w, "This event's leads were deleted", http.StatusGone)
		return
	}
	ix := s.guests.Index(sp.EventID)
	leads := s.store.Leads(sp.Token, phase == PhaseTest, now)
	views := make([]LeadView, 0, len(leads))
	for _, l := range leads {
		views = append(views, s.viewLead(l, ix))
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]any{"leads": views, "phase": phase})
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	sp, ok, authed := s.sponsor(r)
	if !ok || !authed {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	s.writeExport(w, sp, "sponsor")
}

// writeExport sends the CSV and logs who took it (the sponsor or an
// admin), so there's a record of where attendee data went.
func (s *Server) writeExport(w http.ResponseWriter, sp *Sponsor, by string) {
	csvData, filename, err := s.exportCSV(sp)
	if err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, errDeleted) {
			code = http.StatusGone
		}
		http.Error(w, err.Error(), code)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Cache-Control", "no-store")
	w.Write(csvData)
	log.Printf("CSV downloaded by %s: %s, %s (%s)", by, sp.Name, sp.EventName, sp.Token[:8])
}

var errDeleted = errors.New("this event's leads were deleted")

// exportCSV builds a sponsor's CSV: the test leads during test mode, the
// real ones after.
func (s *Server) exportCSV(sp *Sponsor) ([]byte, string, error) {
	now := s.now()
	phase := sp.PhaseAt(now)
	if phase == PhaseDeleted {
		return nil, "", errDeleted
	}
	// Re-match against a fresh guest list so late registrations and
	// LinkedIn fixes land in the export.
	ix := s.guests.Fresh(sp.EventID, time.Minute)
	name := fmt.Sprintf("%s - %s - Leads Scanned", sp.EventName, sp.Name)
	if phase == PhaseTest {
		name += " (test)"
	}
	filename := strings.NewReplacer(`"`, "", "/", "-", "\\", "-").Replace(name) + ".csv"
	var b bytes.Buffer
	if err := WriteCSV(&b, sp, s.store.Leads(sp.Token, phase == PhaseTest, now), ix, s.cfg.FullAnswers); err != nil {
		return nil, "", err
	}
	return b.Bytes(), filename, nil
}

// emailCSV sends a sponsor's CSV to one address, at most 10 times an hour
// per link.
func (s *Server) emailCSV(sp *Sponsor, to, cc, link string) error {
	if s.mail == nil {
		return errors.New("email isn't set up")
	}
	if liveFrom, _, _ := sp.Window(); sp.PhaseAt(s.now()) == PhaseTest {
		return fmt.Errorf("emailing opens %s, 6 hours before the event; test mode can't send emails", localTime(liveFrom, sp.EventTZ))
	}
	addr, err := mail.ParseAddress(strings.TrimSpace(to))
	if err != nil {
		return errors.New("that doesn't look like an email address")
	}
	var ccs []string
	for _, c := range strings.FieldsFunc(cc, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' }) {
		a, err := mail.ParseAddress(c)
		if err != nil {
			return fmt.Errorf("%q in CC doesn't look like an email address", c)
		}
		if !strings.EqualFold(a.Address, addr.Address) {
			ccs = append(ccs, a.Address)
		}
	}
	if len(ccs) > 10 {
		return errors.New("at most 10 people in CC")
	}
	now := s.now()
	s.mailMu.Lock()
	var recent []time.Time
	for _, t := range s.mailSent[sp.Token] {
		if now.Sub(t) < time.Hour {
			recent = append(recent, t)
		}
	}
	if len(recent) >= 10 {
		s.mailMu.Unlock()
		return errors.New("too many emails this hour; download the CSV instead")
	}
	s.mailSent[sp.Token] = append(recent, now)
	s.mailMu.Unlock()

	csvData, filename, err := s.exportCSV(sp)
	if err != nil {
		return err
	}
	phase := sp.PhaseAt(now)
	ix := s.guests.Index(sp.EventID)
	leads := s.store.Leads(sp.Token, phase == PhaseTest, now)
	report := teamReport(sp, leads, now, ix.Len())
	hotText, hotHTML := hotLeads(leads, ix)
	tail := ""
	if phase == PhaseTest {
		tail += "\nThese are test scans. They will be reset before the event.\n"
	}
	if phase != PhaseClosed {
		tail += fmt.Sprintf("\nYour scanner link: %s\nPIN: %s\nEveryone on your team can use the same link and PIN, each with their own name.\n", link, sp.PIN)
	} else {
		tail += fmt.Sprintf("\nYour list stays downloadable here until %s: %s (PIN %s)\n", localDate(sp.PhaseWindowDeletes(), sp.EventTZ), link, sp.PIN)
	}
	tail += "\nQuestions? Just reply to this email. The list is attached.\n"
	tail += "\n" + signature(sp.EventName) + "\n"
	body := report + hotText + tail
	htmlBody := `<div style="font-family:Arial,Helvetica,sans-serif;font-size:14px;line-height:1.5;color:#141c25">` +
		textToHTML(report) + hotHTML + textToHTML(tail) + `</div>`

	// Every email for this sponsor link replies to the one before, so they
	// stay in one thread.
	cur, _ := s.store.Sponsor(sp.Token)
	m := Mail{
		To:        addr.Address,
		Cc:        ccs,
		Subject:   fmt.Sprintf("%s Team Lead Scanning - %s", sp.Name, sp.EventName),
		MessageID: newMessageID(s.cfg.SMTPUser),
		Body:      body,
		HTML:      htmlBody,
		Filename:  filename,
		CSV:       csvData,
	}
	root := m.MessageID
	if cur != nil && cur.MailRoot != "" {
		root = cur.MailRoot
		m.InReplyTo = cur.MailLast
		m.References = cur.MailRoot
		if cur.MailLast != cur.MailRoot {
			m.References += " " + cur.MailLast
		}
	}
	if err := s.mail(m); err != nil {
		return err
	}
	if err := s.store.SetMailThread(sp.Token, root, m.MessageID); err != nil {
		log.Printf("Saving mail thread for %s: %v", sp.Name, err)
	}
	return nil
}

// teamReport is the top of the email: the event's registrations, then how
// the sponsor's team is doing. The registrations come first so a sponsor
// never mistakes their team's scan count for the event's attendance.
func teamReport(sp *Sponsor, leads []Lead, now time.Time, registered int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Hey %s Team,\n\n", sp.Name)
	// While the event is on (or still to come) the numbers are a snapshot;
	// once scanning has closed they're final.
	if sp.PhaseAt(now) == PhaseClosed {
		fmt.Fprintf(&b, "Here's how your team did at %s.\n\n", sp.EventName)
	} else {
		fmt.Fprintf(&b, "Here's how your team is doing at %s, as of right now.\n\n", sp.EventName)
	}
	if registered > 0 {
		fmt.Fprintf(&b, "The event: (%d attendees registered).\n", registered)
	}
	b.WriteString("The list shows only people scanned by your team at the conference.\n\n")
	if len(leads) == 0 {
		b.WriteString("No conversations scanned yet. Open your scanner link and scan a badge to get started.\n")
		return b.String()
	}
	hot, unsure, cold, notes := 0, 0, 0, 0
	for _, l := range leads {
		switch l.Rating {
		case "hot":
			hot++
		case "unsure":
			unsure++
		case "cold":
			cold++
		}
		if strings.TrimSpace(l.Note) != "" {
			notes++
		}
	}
	n := len(leads)
	fmt.Fprintf(&b, "%d %s scanned by your team: %d hot, %d not sure, %d cold, %d not rated, %d with a note.\n",
		n, plural(n, "conversation", "conversations"), hot, unsure, cold, n-hot-unsure-cold, notes)
	return b.String()
}

// signature signs the email as the conference's team.
func signature(eventName string) string {
	n := strings.ToLower(eventName)
	switch {
	case strings.Contains(n, "llmday") && strings.Contains(n, "prompt engineering"):
		return "LLMday & PEC Team"
	case strings.Contains(n, "prompt engineering"):
		return "PEC Team"
	case strings.Contains(n, "llmday"):
		return "LLMday Team"
	case strings.Contains(n, "platformday"):
		return "PLATFORMday Team"
	default:
		return "SREday Team"
	}
}

// localDate formats t's date in the event's time zone, e.g. "31 Jan 2027".
func localDate(t time.Time, tz string) string {
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		loc = time.UTC
	}
	return t.In(loc).Format("2 Jan 2006")
}

// hotLeads lists the leads rated hot, as "John Smith, CTO at Acme -
// john@acme.com - Notes: (note)" with the name linked to their LinkedIn. It
// returns the plain-text and HTML versions.
func hotLeads(leads []Lead, ix *GuestIndex) (string, string) {
	var t, h strings.Builder
	n := 0
	for _, l := range leads {
		if l.Rating != "hot" {
			continue
		}
		name, title, company, email, linkedin := "", "", "", "", ""
		if g, ok := ix.Lookup(l.Key); ok {
			name, title, company, email, linkedin = g.Name, g.Title, g.Company, g.Email, g.LinkedIn
		} else {
			switch l.Key.Kind {
			case "linkedin":
				linkedin = "https://www.linkedin.com/in/" + l.Key.Value
				name = "linkedin.com/in/" + l.Key.Value
			case "email":
				email, name = l.Key.Value, l.Key.Value
			default:
				name = "Luma guest " + l.Key.Value
			}
		}
		if n == 0 {
			t.WriteString("\n🔥 Hot leads registered:\n")
			h.WriteString(`<p style="margin:16px 0 6px">🔥 Hot leads registered:</p><ul style="margin:0;padding-left:20px">`)
		}
		n++
		role := ""
		switch {
		case title != "" && company != "":
			role = title + " at " + company
		case title != "":
			role = title
		default:
			role = company
		}
		t.WriteString("- " + name)
		h.WriteString("<li>")
		if linkedin != "" {
			t.WriteString(" (" + linkedin + ")")
			fmt.Fprintf(&h, `<a href="%s" style="color:#0a66c2;font-weight:bold;text-decoration:none">%s</a>`, html.EscapeString(linkedin), html.EscapeString(name))
		} else {
			fmt.Fprintf(&h, "<b>%s</b>", html.EscapeString(name))
		}
		if role != "" {
			t.WriteString(", " + role)
			h.WriteString(", " + html.EscapeString(role))
		}
		if email != "" && email != name {
			t.WriteString(" - " + email)
			fmt.Fprintf(&h, ` - <a href="mailto:%s" style="color:#141c25">%s</a>`, html.EscapeString(email), html.EscapeString(email))
		}
		if note := strings.TrimSpace(l.Note); note != "" {
			t.WriteString(" - Notes: (" + note + ")")
			h.WriteString(" - Notes: (" + html.EscapeString(note) + ")")
		}
		t.WriteString("\n")
		h.WriteString("</li>")
	}
	if n > 0 {
		h.WriteString("</ul>")
	}
	return t.String(), h.String()
}

// textToHTML escapes plain text for the HTML email, keeping line breaks,
// indentation and making links clickable.
func textToHTML(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		indent := len(line) - len(strings.TrimLeft(line, " "))
		esc := html.EscapeString(strings.TrimLeft(line, " "))
		esc = urlRe.ReplaceAllString(esc, `<a href="$0">$0</a>`)
		b.WriteString(strings.Repeat("&nbsp;", indent) + esc + "<br>\n")
	}
	return b.String()
}

var urlRe = regexp.MustCompile(`https?://[^\s<]+`)

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func (s *Server) handleEmail(w http.ResponseWriter, r *http.Request) {
	sp, ok, authed := s.sponsor(r)
	if !ok || !authed {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		To string `json:"to"`
		Cc string `json:"cc"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	if err := s.emailCSV(sp, req.To, req.Cc, s.baseURL(r)+"/s/"+sp.Token); err != nil {
		log.Printf("Email for %s: %v", sp.Name, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdminEmail(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.store.Sponsor(r.PathValue("token"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	msg := "Sent " + sp.Name + "'s leads to " + r.FormValue("to")
	if err := s.emailCSV(sp, r.FormValue("to"), r.FormValue("cc"), s.baseURL(r)+"/s/"+sp.Token); err != nil {
		msg = "Couldn't email " + sp.Name + "'s leads: " + err.Error()
	}
	http.Redirect(w, r, "/admin?msg="+url.QueryEscape(msg)+"#"+sp.Token, http.StatusSeeOther)
}

// handleQR serves a QR code of a sponsor's scanner link, so a sponsor can
// open it by scanning the admin screen. ?size= picks the pixel size.
func (s *Server) handleQR(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.store.Sponsor(r.PathValue("token"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	size := 256
	if n, err := strconv.Atoi(r.URL.Query().Get("size")); err == nil && n >= 64 && n <= 2048 {
		size = n
	}
	png, err := qrcode.Encode(s.baseURL(r)+"/s/"+sp.Token, qrcode.Medium, size)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Write(png)
}

func (s *Server) handleServiceWorker(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(serviceWorker)
}

// cleanup deletes expired leads every hour (test leads after an hour,
// real ones 90 days or 12 months after closing, see longKeepFrom).
func (s *Server) cleanup() {
	for {
		if n, err := s.store.DeleteExpired(s.now()); err != nil {
			log.Printf("Deleting expired leads: %v", err)
		} else if n > 0 {
			log.Printf("Deleted the leads of %d sponsor links past their retention period", n)
		}
		time.Sleep(time.Hour)
	}
}

func (s *Server) handleLogo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(logoPNG)
}

// handleIcon serves the badger head, the tab and home-screen icon.
func (s *Server) handleIcon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(iconPNG)
}

func (s *Server) handleJSQR(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Write(jsQR)
}

// private marks a sponsor page as unlisted: no search engines, the secret
// link never leaks to other sites as a referrer, and no framing.
func private(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		next(w, r)
	}
}

func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin", s.requireAdmin(s.handleAdmin))
	mux.HandleFunc("POST /admin/sponsors", s.requireAdmin(s.handleAddSponsor))
	mux.HandleFunc("POST /admin/refresh", s.requireAdmin(s.handleRefreshSites))
	mux.HandleFunc("POST /admin/pin/{token}", s.requireAdmin(s.handleNewPIN))
	mux.HandleFunc("GET /admin/export/{token}", s.requireAdmin(s.handleAdminExport))
	mux.HandleFunc("GET /admin/qr/{token}", s.requireAdmin(s.handleQR))
	mux.HandleFunc("POST /admin/email/{token}", s.requireAdmin(s.handleAdminEmail))
	mux.HandleFunc("GET /s/{token}", private(s.handleSponsorPage))
	mux.HandleFunc("GET /s/{token}/dashboard", private(s.handleDashboard))
	mux.HandleFunc("POST /s/{token}/pin", private(s.handlePIN))
	mux.HandleFunc("POST /s/{token}/sync", private(s.handleSync))
	mux.HandleFunc("GET /s/{token}/leads", private(s.handleLeads))
	mux.HandleFunc("GET /s/{token}/export.csv", private(s.handleExport))
	mux.HandleFunc("POST /s/{token}/email", private(s.handleEmail))
	mux.HandleFunc("GET /robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, `User-agent: *
Disallow: /
`)
	})
	mux.HandleFunc("GET /s/sw.js", s.handleServiceWorker)
	mux.HandleFunc("GET /assets/jsQR.js", s.handleJSQR)
	mux.HandleFunc("GET /assets/snout-logo.png", s.handleLogo)
	mux.HandleFunc("GET /assets/snout-icon.png", s.handleIcon)
	mux.HandleFunc("GET /favicon.ico", s.handleIcon)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	})
	return mux
}

func main() {
	log.Printf("snout %s starting", version)

	cfg := loadConfig()
	if cfg.LumaAPIKey == "" {
		log.Fatal("LUMA_API_KEY is required")
	}
	if cfg.AdminPassword == "" {
		log.Fatal("ADMIN_PASSWORD is required")
	}
	if u := os.Getenv("LUMA_BASE_URL"); u != "" {
		lumaBaseURL = strings.TrimRight(u, "/") // for local testing against a fake Luma
	}

	srv, err := newServer(cfg)
	if err != nil {
		log.Fatalf("Failed to initialize server: %v", err)
	}

	if srv.mail == nil {
		log.Printf("SMTP_USER/SMTP_PASSWORD not set: emailing CSVs is off")
	}
	go srv.cleanup()

	addr := cfg.Host + ":" + cfg.Port
	log.Printf("Listening on %s", addr)
	if err := http.ListenAndServe(addr, srv.routes()); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
