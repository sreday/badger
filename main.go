package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"image"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sreday/badger/pkg"
)

var version = "dev"

//go:embed config.json
var embeddedConfig []byte

//go:embed assets/template.png
var embeddedTemplate []byte

//go:embed templates/*.html
var templateFS embed.FS

// Config holds the application configuration.
type Config struct {
	Host         string
	Port         string
	FontPath     string
	WifiID       string
	WifiPassword string
	WifiAuth     string
	SessionKey   string
	Debug        bool
	PrintCmd     string
	Printers     []string
}

func loadConfig() Config {
	cfg := Config{
		Host:         envOr("HOST", "0.0.0.0"),
		Port:         envOr("PORT", "8080"),
		FontPath:     os.Getenv("FONT_PATH"),
		WifiID:       os.Getenv("WIFI_ID"),
		WifiPassword: os.Getenv("WIFI_PASSWORD"),
		WifiAuth:     envOr("WIFI_AUTH", "WPA"),
		SessionKey:   envOr("SESSION_KEY", "badger"),
		Debug:        os.Getenv("DEBUG") != "",
	}

	// Load printer config from config.json (prefer local file, fall back to embedded)
	data, err := os.ReadFile("config.json")
	if err != nil {
		log.Printf("No local config.json, using embedded default")
		data = embeddedConfig
	}
	{
		var fc struct {
			PrintCmd string   `json:"print_cmd"`
			Printers []string `json:"printers"`
		}
		if err := json.Unmarshal(data, &fc); err != nil {
			log.Printf("Warning: could not parse config.json: %v", err)
		} else {
			cfg.PrintCmd = fc.PrintCmd
			cfg.Printers = fc.Printers
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
	cfg    Config
	tmpls  map[string]*template.Template
	mu     sync.RWMutex
	events map[string]pkg.LumaEventEntry     // apiID -> event
	guests map[string]map[string]pkg.BadgeData // eventID -> email -> badge
}

func newServer(cfg Config) (*Server, error) {
	if err := pkg.InitBadgeAssets(embeddedTemplate, cfg.FontPath); err != nil {
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
		cfg:    cfg,
		tmpls:  tmpls,
		events: make(map[string]pkg.LumaEventEntry),
		guests: make(map[string]map[string]pkg.BadgeData),
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

	s.mu.RLock()
	wifiID := s.cfg.WifiID
	wifiPassword := s.cfg.WifiPassword
	wifiAuth := s.cfg.WifiAuth
	s.mu.RUnlock()

	s.tmpls["events.html"].ExecuteTemplate(w, "events.html", map[string]any{
		"Upcoming":     upcoming,
		"Past":         past,
		"WifiID":       wifiID,
		"WifiPassword": wifiPassword,
		"WifiAuth":     wifiAuth,
	})
}

func (s *Server) wifiConfig() (string, string, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.WifiID, s.cfg.WifiPassword, s.cfg.WifiAuth
}

// handleWifi updates WiFi configuration.
func (s *Server) handleWifi(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.cfg.WifiID = r.FormValue("wifi_id")
	s.cfg.WifiPassword = r.FormValue("wifi_password")
	s.cfg.WifiAuth = r.FormValue("wifi_auth")
	s.mu.Unlock()

	http.Redirect(w, r, "/", http.StatusSeeOther)
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
		"Event":   eventData,
		"EventID": eventID,
		"Guests":  sorted,
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

	for _, guest := range sorted {
		wID, wPass, wAuth := s.wifiConfig()
		img, err := pkg.GenerateBadge(guest, wID, wPass, wAuth)
		if err != nil {
			log.Printf("Error generating badge for %s: %v", guest.Name, err)
			continue
		}
		s.printBadge(img, fmt.Sprintf("badges/%s-", eventID), guest.Name)
	}

	fmt.Fprintf(w, "OK: %d badges printing", len(guests))
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

	wID, wPass, wAuth := s.wifiConfig()
	img, err := pkg.GenerateBadge(data, wID, wPass, wAuth)
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

	wID, wPass, wAuth := s.wifiConfig()
	img, err := pkg.GenerateBadge(data, wID, wPass, wAuth)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error generating badge: %v", err), http.StatusInternalServerError)
		return
	}

	s.printBadge(img, fmt.Sprintf("badges/%s-", eventID), data.Name)

	pngData, err := pkg.EncodeBadgePNG(img)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error encoding badge: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "image/png")
	w.Write(pngData)
}

// handleSubmit processes the ad-hoc badge form.
func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	data := pkg.BadgeData{
		Name:     r.FormValue("name"),
		Email:    r.FormValue("email"),
		Title:    r.FormValue("title"),
		Company:  r.FormValue("company"),
		LinkedIn: r.FormValue("linkedin"),
	}

	wID, wPass, wAuth := s.wifiConfig()
	img, err := pkg.GenerateBadge(data, wID, wPass, wAuth)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error generating badge: %v", err), http.StatusInternalServerError)
		return
	}

	s.printBadge(img, "badges/adhoc-", data.Name)

	pngData, err := pkg.EncodeBadgePNG(img)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error encoding badge: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "image/png")
	w.Write(pngData)
}

// printBadge saves the image to disk and sends it to a printer.
func (s *Server) printBadge(img image.Image, pathPrefix, name string) {
	if len(s.cfg.Printers) == 0 {
		log.Printf("No printers configured, skipping print")
		return
	}
	if s.cfg.PrintCmd == "" {
		log.Printf("No print command configured, skipping print")
		return
	}

	// Ensure output directory exists
	dir := filepath.Dir(pathPrefix)
	if dir != "" && dir != "." {
		os.MkdirAll(dir, 0755)
	}

	filename := pathPrefix + strings.ReplaceAll(strings.ToLower(name), " ", "_") + ".png"

	// Save PNG to disk
	pngData, err := pkg.EncodeBadgePNG(img)
	if err != nil {
		log.Printf("Error encoding badge for print: %v", err)
		return
	}
	if err := os.WriteFile(filename, pngData, 0644); err != nil {
		log.Printf("Error saving badge to %s: %v", filename, err)
		return
	}

	printer := s.cfg.Printers[rand.Intn(len(s.cfg.Printers))]

	cmdStr := s.cfg.PrintCmd
	cmdStr = strings.ReplaceAll(cmdStr, "{path}", filename)
	cmdStr = strings.ReplaceAll(cmdStr, "{printer}", printer)

	log.Printf("Print command: %s", cmdStr)
	parts := strings.Fields(cmdStr)
	cmd := exec.Command(parts[0], parts[1:]...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("Print error: %v, output: %s", err, string(output))
	}
}

func main() {
	log.Printf("badger %s starting", version)

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
	mux.HandleFunc("POST /submit", srv.handleSubmit)
	mux.HandleFunc("POST /wifi", srv.handleWifi)

	addr := cfg.Host + ":" + cfg.Port
	log.Printf("Listening on %s", addr)

	browserAddr := "localhost:" + cfg.Port
	go func() {
		// Best-effort: try to open the browser
		exec.Command("open", "http://"+browserAddr).Start()           // macOS
		exec.Command("xdg-open", "http://"+browserAddr).Start()       // Linux
		exec.Command("cmd", "/c", "start", "http://"+browserAddr).Start() // Windows
	}()

	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
