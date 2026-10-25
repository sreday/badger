// Command badger-scan is the sponsor lead scanner: sponsors scan attendees'
// badge QR codes (their LinkedIn), and each scan is matched to the event's
// Luma guest list and kept in the order the conversations happened.
package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
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
	"strings"
	"sync"
	"time"
)

var version = "dev"

//go:embed templates/*.html
var templateFS embed.FS

//go:embed assets/jsQR.js
var jsQR []byte

// Config holds the scanner configuration, all from the environment.
type Config struct {
	Host          string
	Port          string
	DataDir       string
	LumaAPIKey    string
	AdminUser     string
	AdminPassword string
	PublicURL     string
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
}

type pinFail struct {
	count int
	until time.Time
}

func newServer(cfg Config) (*Server, error) {
	pages := []string{"admin.html", "pin.html", "scan.html"}
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

	return &Server{
		cfg:      cfg,
		tmpls:    tmpls,
		store:    store,
		guests:   NewGuestCache(cfg.LumaAPIKey, 5*time.Minute),
		secret:   secret,
		pinFails: make(map[string]*pinFail),
	}, nil
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
			w.Header().Set("WWW-Authenticate", `Basic realm="badger-scan"`)
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
	Link  string
	Leads int
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{}

	events, err := ListEvents(s.cfg.LumaAPIKey)
	if err != nil {
		data["Error"] = fmt.Sprintf("Failed to fetch events from Luma: %v", err)
	}
	// Only events from the last week onwards; sponsors aren't set up for old ones.
	cutoff := time.Now().AddDate(0, 0, -7)
	var recent []LumaEvent
	for _, e := range events {
		if t, err := time.Parse(time.RFC3339, e.StartAt); err != nil || t.After(cutoff) {
			recent = append(recent, e)
		}
	}
	data["Events"] = recent

	base := s.baseURL(r)
	var rows []sponsorRow
	for _, sp := range s.store.Sponsors() {
		rows = append(rows, sponsorRow{Sponsor: sp, Link: base + "/s/" + sp.Token, Leads: s.store.LeadCount(sp.Token)})
	}
	data["Sponsors"] = rows
	s.render(w, "admin.html", data)
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
	s.writeExport(w, sp)
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
	authed := err == nil && hmac.Equal([]byte(c.Value), []byte(s.sessionValue(sp)))
	return sp, true, authed
}

func (s *Server) handleSponsorPage(w http.ResponseWriter, r *http.Request) {
	sp, ok, authed := s.sponsor(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	data := map[string]any{"Sponsor": sp}
	if !authed {
		s.render(w, "pin.html", data)
		return
	}
	s.render(w, "scan.html", data)
}

func (s *Server) handlePIN(w http.ResponseWriter, r *http.Request) {
	sp, ok, _ := s.sponsor(r)
	if !ok {
		http.NotFound(w, r)
		return
	}

	s.pinMu.Lock()
	f := s.pinFails[sp.Token]
	if f != nil && time.Now().Before(f.until) {
		s.pinMu.Unlock()
		s.render(w, "pin.html", map[string]any{"Sponsor": sp, "Error": "Too many attempts. Try again in a few minutes."})
		return
	}
	s.pinMu.Unlock()

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
		s.render(w, "pin.html", map[string]any{"Sponsor": sp, "Error": "Wrong PIN."})
		return
	}

	s.pinMu.Lock()
	delete(s.pinFails, sp.Token)
	s.pinMu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     s.sessionCookieName(sp),
		Value:    s.sessionValue(sp),
		Path:     "/s/" + sp.Token,
		MaxAge:   60 * 60 * 24 * 30,
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/s/"+sp.Token, http.StatusSeeOther)
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
}

func viewLead(l Lead, ix *GuestIndex) LeadView {
	v := LeadView{
		ScanID:    l.ScanID,
		ScannedAt: l.ScannedAt,
		ScannedBy: l.ScannedBy,
		Rating:    l.Rating,
		Note:      l.Note,
	}
	if g, ok := ix.Lookup(l.Key); ok {
		v.Matched = true
		v.Name, v.Title, v.Company, v.LinkedIn = g.Name, g.Title, g.Company, g.LinkedIn
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
	Status string    `json:"status"` // "ok", "duplicate", "unreadable", "error"
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

	ix := s.guests.Index(sp.EventID)
	now := time.Now().UTC()
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
			lead, dup, err := s.store.AddScan(sp, Lead{
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
			v := viewLead(lead, ix)
			status := "ok"
			if dup {
				status = "duplicate"
			}
			results = append(results, syncResult{ScanID: op.ScanID, Status: status, Lead: &v})
		case "update":
			lead, err := s.store.UpdateLead(sp, op.ScanID, truncate(op.Rating, 20), truncate(op.Note, 2000))
			if err != nil {
				results = append(results, syncResult{ScanID: op.ScanID, Status: "error"})
				continue
			}
			v := viewLead(lead, ix)
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
	ix := s.guests.Index(sp.EventID)
	leads := s.store.Leads(sp.Token)
	views := make([]LeadView, 0, len(leads))
	for _, l := range leads {
		views = append(views, viewLead(l, ix))
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]any{"leads": views})
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	sp, ok, authed := s.sponsor(r)
	if !ok || !authed {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	s.writeExport(w, sp)
}

func (s *Server) writeExport(w http.ResponseWriter, sp *Sponsor) {
	// Re-match against a fresh guest list so late registrations and
	// LinkedIn fixes land in the export.
	ix := s.guests.Fresh(sp.EventID, time.Minute)
	filename := fmt.Sprintf("%s - %s - Leads.csv", sp.EventName, sp.Name)
	filename = strings.NewReplacer(`"`, "", "/", "-", "\\", "-").Replace(filename)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Cache-Control", "no-store")
	if err := WriteCSV(w, sp, s.store.Leads(sp.Token), ix); err != nil {
		log.Printf("Export for %s: %v", sp.Name, err)
	}
}

func (s *Server) handleJSQR(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Write(jsQR)
}

func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin", s.requireAdmin(s.handleAdmin))
	mux.HandleFunc("POST /admin/sponsors", s.requireAdmin(s.handleAddSponsor))
	mux.HandleFunc("GET /admin/export/{token}", s.requireAdmin(s.handleAdminExport))
	mux.HandleFunc("GET /s/{token}", s.handleSponsorPage)
	mux.HandleFunc("POST /s/{token}/pin", s.handlePIN)
	mux.HandleFunc("POST /s/{token}/sync", s.handleSync)
	mux.HandleFunc("GET /s/{token}/leads", s.handleLeads)
	mux.HandleFunc("GET /s/{token}/export.csv", s.handleExport)
	mux.HandleFunc("GET /assets/jsQR.js", s.handleJSQR)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	})
	return mux
}

func main() {
	log.Printf("badger-scan %s starting", version)

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

	addr := cfg.Host + ":" + cfg.Port
	log.Printf("Listening on %s", addr)
	if err := http.ListenAndServe(addr, srv.routes()); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
