// Badger 2.0: the badge printer, friendlier. Runs beside the original
// Badger (the root of this repo), which stays as it is: port 8090 instead
// of 8080, its own folder (~/.badger) for config and printed badges, and
// only the 2.0 badge design (pkg.GenerateBadgeV2).
package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"image"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sreday/badger/pkg"
)

var version = "dev"

// appDir is ~/.badger: config.json (printers) and the printed badges.
func appDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, ".badger")
}

//go:embed config.json
var embeddedConfig []byte

//go:embed assets/template.png
var embeddedTemplate []byte

//go:embed templates/*.html
var templateFS embed.FS

// Config holds the application configuration.
type Config struct {
	Host       string
	Port       string
	SessionKey string
	Debug      bool
	PrintCmd   string
	Printer    string // the one printer Badger 2.0 prints to
}

func loadConfig() Config {
	cfg := Config{
		Host:       envOr("HOST", "0.0.0.0"),
		Port:       envOr("PORT", "8090"),
		SessionKey: envOr("SESSION_KEY", "badger"),
		Debug:      os.Getenv("DEBUG") != "",
	}

	// Printers: ~/.badger/config.json (the installer copies the old
	// Badger's there), else the built-in default.
	data, err := os.ReadFile(filepath.Join(appDir(), "config.json"))
	if err != nil {
		log.Printf("No %s, using the built-in printer settings", filepath.Join(appDir(), "config.json"))
		data = embeddedConfig
	}
	{
		var fc struct {
			PrintCmd string   `json:"print_cmd"`
			Printer  string   `json:"printer"`
			Printers []string `json:"printers"` // the original Badger's list: 2.0 takes the first
		}
		if err := json.Unmarshal(data, &fc); err != nil {
			log.Printf("Warning: could not parse config.json: %v", err)
		} else {
			cfg.PrintCmd = fc.PrintCmd
			cfg.Printer = fc.Printer
			if cfg.Printer == "" && len(fc.Printers) > 0 {
				cfg.Printer = fc.Printers[0]
			}
		}
	}

	return cfg
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Server holds application state.
type Server struct {
	cfg     Config
	tmpls   map[string]*template.Template
	mu      sync.RWMutex
	events  map[string]pkg.LumaEventEntry       // apiID -> event
	guests  map[string]map[string]pkg.BadgeData // eventID -> email -> badge
	printer *printer
}

func newServer(cfg Config) (*Server, error) {
	if err := pkg.InitBadgeAssets(embeddedTemplate, ""); err != nil {
		return nil, fmt.Errorf("initializing badge assets: %w", err)
	}

	pages := []string{"login.html", "events.html", "guests.html"}
	tmpls := make(map[string]*template.Template, len(pages))
	for _, page := range pages {
		t, err := template.ParseFS(templateFS, "templates/layout.html", "templates/"+page)
		if err != nil {
			return nil, fmt.Errorf("parsing template %s: %w", page, err)
		}
		tmpls[page] = t
	}

	return &Server{
		cfg:     cfg,
		tmpls:   tmpls,
		events:  make(map[string]pkg.LumaEventEntry),
		guests:  make(map[string]map[string]pkg.BadgeData),
		printer: &printer{name: cfg.Printer},
	}, nil
}

func (s *Server) getAPIKey(r *http.Request) string {
	c, err := r.Cookie("api_key")
	if err != nil {
		return ""
	}
	return c.Value
}

func (s *Server) setAPIKey(w http.ResponseWriter, key string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "api_key",
		Value:    key,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearAPIKey(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     "api_key",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) renderError(w http.ResponseWriter, tmplName string, errMsg string, data map[string]any) {
	if data == nil {
		data = make(map[string]any)
	}
	data["Error"] = errMsg
	log.Printf("Error: %s", errMsg)
	w.WriteHeader(http.StatusInternalServerError)
	if err := s.tmpls[tmplName].ExecuteTemplate(w, tmplName, data); err != nil {
		log.Printf("Template error: %v", err)
		http.Error(w, errMsg, http.StatusInternalServerError)
	}
}

// handleIndex shows login page or event list.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	key := s.getAPIKey(r)
	if key == "" {
		s.tmpls["login.html"].ExecuteTemplate(w, "login.html", nil)
		return
	}

	events, err := pkg.ListEvents(key)
	if err != nil {
		s.renderError(w, "login.html", fmt.Sprintf("Failed to fetch events: %v", err), nil)
		return
	}

	s.mu.Lock()
	for _, e := range events {
		s.events[e.Event.APIId] = e
	}
	s.mu.Unlock()

	now := time.Now()
	var upcoming, past []pkg.LumaEventEntry
	for _, e := range events {
		endAt, err := time.Parse(time.RFC3339, e.Event.EndAt)
		if err == nil && endAt.Before(now) {
			past = append(past, e)
		} else {
			upcoming = append(upcoming, e)
		}
	}
	// Upcoming: soonest first; Past: most recent first
	sort.Slice(upcoming, func(i, j int) bool {
		return upcoming[i].Event.StartAt < upcoming[j].Event.StartAt
	})
	sort.Slice(past, func(i, j int) bool {
		return past[i].Event.StartAt > past[j].Event.StartAt
	})

	s.tmpls["events.html"].ExecuteTemplate(w, "events.html", map[string]any{
		"Upcoming":    upcoming,
		"Past":        past,
		"ShowPrinter": true,
	})
}

// renderBadge draws the 2.0 badge.
func (s *Server) renderBadge(data pkg.BadgeData) (image.Image, error) {
	return pkg.GenerateBadgeV2(data)
}

// handleLogin processes the API key form.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	key := r.FormValue("key")
	if key == "" {
		s.renderError(w, "login.html", "API key is required", nil)
		return
	}

	// Validate by trying to list events
	_, err := pkg.ListEvents(key)
	if err != nil {
		s.renderError(w, "login.html", fmt.Sprintf("Invalid API key: %v", err), nil)
		return
	}

	s.setAPIKey(w, key)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleLogout clears the session.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.clearAPIKey(w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleCSV processes a CSV file upload and redirects to the guest list.
func (s *Server) handleCSV(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		s.renderError(w, "login.html", fmt.Sprintf("Failed to parse upload: %v", err), nil)
		return
	}

	file, header, err := r.FormFile("csv")
	if err != nil {
		s.renderError(w, "login.html", "CSV file is required", nil)
		return
	}
	defer file.Close()

	log.Printf("Received CSV upload: %s (%d bytes)", header.Filename, header.Size)

	guests, err := pkg.ParseCSV(file)
	if err != nil {
		s.renderError(w, "login.html", fmt.Sprintf("Failed to parse CSV: %v", err), nil)
		return
	}

	if len(guests) == 0 {
		s.renderError(w, "login.html", "CSV file contains no guest records", nil)
		return
	}

	eventID := fmt.Sprintf("csv-%d", time.Now().UnixMilli())

	eventName := strings.TrimSuffix(header.Filename, ".csv")
	if eventName == "" {
		eventName = "CSV Import"
	}

	syntheticEvent := pkg.LumaEventEntry{
		Event: pkg.LumaEvent{
			APIId:   eventID,
			Name:    eventName,
			StartAt: time.Now().Format(time.RFC3339),
			EndAt:   time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		},
	}

	s.mu.Lock()
	s.events[eventID] = syntheticEvent
	s.guests[eventID] = guests
	s.mu.Unlock()

	log.Printf("CSV import: %d guests loaded as event %s", len(guests), eventID)

	http.Redirect(w, r, "/event/"+eventID, http.StatusSeeOther)
}

// handleEvent shows the guest list for an event.
func (s *Server) handleEvent(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("eventID")

	s.mu.RLock()
	eventData, eventOk := s.events[eventID]
	cachedGuests := s.guests[eventID]
	s.mu.RUnlock()

	if !eventOk {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	var guests map[string]pkg.BadgeData

	if strings.HasPrefix(eventID, "csv-") {
		// CSV-imported event: guests already in memory
		guests = cachedGuests
	} else {
		// Luma API event: fetch latest guest list
		key := s.getAPIKey(r)
		if key == "" {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}

		var err error
		guests, err = pkg.GetGuests(eventID, key)
		if err != nil {
			s.renderError(w, "guests.html", fmt.Sprintf("Failed to fetch guests: %v", err), map[string]any{
				"Event":   eventData,
				"EventID": eventID,
			})
			return
		}

		s.mu.Lock()
		s.guests[eventID] = guests
		s.mu.Unlock()
	}

	// Sort guests by name for display
	sorted := make([]pkg.BadgeData, 0, len(guests))
	for _, g := range guests {
		sorted = append(sorted, g)
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})

	s.tmpls["guests.html"].ExecuteTemplate(w, "guests.html", map[string]any{
		"Event":       eventData,
		"EventID":     eventID,
		"Guests":      sorted,
		"ShowPrinter": true,
	})
}

// handlePrintAll prints badges for all guests.
func (s *Server) handlePrintAll(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("eventID")

	var guests map[string]pkg.BadgeData

	if strings.HasPrefix(eventID, "csv-") {
		s.mu.RLock()
		guests = s.guests[eventID]
		s.mu.RUnlock()
		if guests == nil {
			http.Error(w, "Event not found", http.StatusNotFound)
			return
		}
	} else {
		key := s.getAPIKey(r)
		if key == "" {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		var err error
		guests, err = pkg.GetGuests(eventID, key)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to fetch guests: %v", err), http.StatusInternalServerError)
			return
		}
	}

	sorted := make([]pkg.BadgeData, 0, len(guests))
	for _, g := range guests {
		sorted = append(sorted, g)
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})

	jobs := []PrintJob{}
	for _, guest := range sorted {
		img, err := s.renderBadge(guest)
		if err != nil {
			log.Printf("Error generating badge for %s: %v", guest.Name, err)
			continue
		}
		jobs = append(jobs, s.printer.Print(s.cfg, img, filepath.Join(appDir(), "badges", eventID+"-"), guest.Name))
	}

	if r.Method == http.MethodPost {
		writeJSON(w, map[string]any{"count": len(jobs), "jobs": jobs})
		return
	}
	fmt.Fprintf(w, "OK: %d badges printing", len(jobs))
}

// handleViewBadge shows a badge preview.
func (s *Server) handleViewBadge(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("eventID")
	email := r.PathValue("email")

	s.mu.RLock()
	guests, ok := s.guests[eventID]
	s.mu.RUnlock()
	if !ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	data, ok := guests[email]
	if !ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	img, err := s.renderBadge(data)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error generating badge: %v", err), http.StatusInternalServerError)
		return
	}

	pngData, err := pkg.EncodeBadgePNG(img)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error encoding badge: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "image/png")
	w.Write(pngData)
}

// handlePrintBadge prints a badge and shows it.
func (s *Server) handlePrintBadge(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("eventID")
	email := r.PathValue("email")

	s.mu.RLock()
	guests, ok := s.guests[eventID]
	s.mu.RUnlock()
	if !ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	data, ok := guests[email]
	if !ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	img, err := s.renderBadge(data)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error generating badge: %v", err), http.StatusInternalServerError)
		return
	}

	job := s.printer.Print(s.cfg, img, filepath.Join(appDir(), "badges", eventID+"-"), data.Name)
	if r.Method == http.MethodPost {
		// The page's print button: the new job, for the printer panel.
		writeJSON(w, job)
		return
	}

	pngData, err := pkg.EncodeBadgePNG(img)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error encoding badge: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "image/png")
	w.Write(pngData)
}

// handleSubmit prints a walk-in badge. Every field is optional; without a
// LinkedIn the badge has no QR code. The LinkedIn may be typed any way
// (linkedin.com/in/jo, in/jo, a full URL).
func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	data := pkg.BadgeData{
		Name:     strings.TrimSpace(r.FormValue("name")),
		Title:    strings.TrimSpace(r.FormValue("title")),
		Company:  strings.TrimSpace(r.FormValue("company")),
		LinkedIn: pkg.ExtractLinkedIn(map[string]string{"linkedin": r.FormValue("linkedin")}),
	}
	if data.Name == "" && data.Title == "" && data.Company == "" && data.LinkedIn == "" {
		http.Error(w, "Fill in at least one field", http.StatusBadRequest)
		return
	}
	label := data.Name
	if label == "" {
		label = "Walk-in"
	}

	img, err := s.renderBadge(data)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error generating badge: %v", err), http.StatusInternalServerError)
		return
	}

	job := s.printer.Print(s.cfg, img, filepath.Join(appDir(), "badges", "adhoc-"), label)
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, job) // the page's form: the job, for the printer panel
		return
	}

	pngData, err := pkg.EncodeBadgePNG(img)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error encoding badge: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "image/png")
	w.Write(pngData)
}

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println("badger2", version)
		return
	}
	log.Printf("Badger 2.0 %s starting", version)

	cfg := loadConfig()

	srv, err := newServer(cfg)
	if err != nil {
		log.Fatalf("Failed to initialize server: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", srv.handleIndex)
	mux.HandleFunc("POST /luma", srv.handleLogin)
	mux.HandleFunc("POST /csv", srv.handleCSV)
	mux.HandleFunc("GET /logout", srv.handleLogout)
	mux.HandleFunc("GET /event/{eventID}", srv.handleEvent)
	mux.HandleFunc("GET /event/{eventID}/print-all", srv.handlePrintAll)
	mux.HandleFunc("GET /view/{eventID}/{email}", srv.handleViewBadge)
	mux.HandleFunc("GET /print/{eventID}/{email}", srv.handlePrintBadge)
	mux.HandleFunc("POST /print/{eventID}/{email}", srv.handlePrintBadge)
	mux.HandleFunc("POST /event/{eventID}/print-all", srv.handlePrintAll)
	mux.HandleFunc("GET /printer", srv.handlePrinterInfo)
	mux.HandleFunc("POST /printer", srv.handleSetPrinter)
	mux.HandleFunc("POST /submit", srv.handleSubmit)

	addr := cfg.Host + ":" + cfg.Port
	browserURL := "http://localhost:" + cfg.Port
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		// Already running (typed "badger2" twice): show it instead.
		log.Printf("Badger 2.0 is already running: opening %s", browserURL)
		openBrowser(browserURL)
		return
	}
	log.Printf("Badger 2.0 is running at %s (close this window to stop it)", browserURL)
	if os.Getenv("BADGER_NO_BROWSER") == "" {
		openBrowser(browserURL)
	}
	if err := http.Serve(ln, mux); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

// openBrowser opens a URL in the default browser, best effort.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("Open %s in your browser", url)
	}
}

// handlePrinterInfo is the printer panel's data: state, printers, jobs.
func (s *Server) handlePrinterInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.printer.Info())
}

// handleSetPrinter chooses the printer (from the panel's list).
func (s *Server) handleSetPrinter(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("printer"))
	known := listPrinters()
	ok := len(known) == 0 // no CUPS list: take it as typed
	for _, p := range known {
		ok = ok || p == name
	}
	if name == "" || !ok {
		http.Error(w, "Unknown printer", http.StatusBadRequest)
		return
	}
	if err := s.printer.SetName(name); err != nil {
		log.Printf("Saving the printer choice: %v", err)
	}
	writeJSON(w, s.printer.Info())
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}
