// Package app is Badger 2.0: the badge printer, friendlier. It runs on a
// laptop (badger2, beside the original Badger: port 8090 instead of 8080,
// its own folder ~/.badger, only the 2.0 badge design) or hosted inside
// Snout at /badger, where the browser prints the badges on the laptop's
// own printer (Hosted).
package app

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"image"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sreday/badger/pkg"
)

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
	Printers   []string // the printers Badger 2.0 prints to

	// Hosted inside Snout: no Luma login (LumaKey is the server's), pages
	// under Base (e.g. /badger), and the browser prints each badge on the
	// laptop's own printer instead of CUPS.
	Hosted  bool
	Base    string
	LumaKey string
}

// LoadConfig reads the laptop setup: env and ~/.badger/config.json.
func LoadConfig() Config {
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
			Printers []string `json:"printers"` // the original Badger's list works as it is
			Printer  string   `json:"printer"`  // an early 2.0's single printer
		}
		if err := json.Unmarshal(data, &fc); err != nil {
			log.Printf("Warning: could not parse config.json: %v", err)
		} else {
			cfg.PrintCmd = fc.PrintCmd
			cfg.Printers = fc.Printers
			if len(cfg.Printers) == 0 && fc.Printer != "" {
				cfg.Printers = []string{fc.Printer}
			}
		}
	}

	return cfg
}

// brandOf is the conference brand in an event's name, for its colour
// accent (Snout's colours): sreday, llmday (and PEC), platformday, or ""
// (DevOps Not Dead, CSV imports: the badge purple).
func brandOf(eventName string) string {
	n := strings.ToLower(eventName)
	switch {
	case strings.Contains(n, "platformday"):
		return "platformday"
	case strings.Contains(n, "llmday") || strings.Contains(n, "prompt engineering"):
		return "llmday"
	case strings.Contains(n, "sreday"):
		return "sreday"
	}
	return ""
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
	web     *webPrints // hosted: badges waiting for the browser to print them
}

// NewServer sets Badger 2.0 up; Handler serves it.
func NewServer(cfg Config) (*Server, error) {
	if err := pkg.InitBadgeAssets(embeddedTemplate, ""); err != nil {
		return nil, fmt.Errorf("initializing badge assets: %w", err)
	}

	pages := []string{"login.html", "events.html", "guests.html"}
	tmpls := make(map[string]*template.Template, len(pages))
	for _, page := range pages {
		t, err := template.New("layout.html").Funcs(template.FuncMap{
			"base":   func() string { return cfg.Base },
			"hosted": func() bool { return cfg.Hosted },
			"brand":  brandOf,
		}).ParseFS(templateFS, "templates/layout.html", "templates/"+page)
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
		printer: &printer{names: cfg.Printers},
		web:     &webPrints{},
	}, nil
}

// Handler serves Badger 2.0's pages (under Config.Base when hosted: strip
// that prefix before this handler).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	if !s.cfg.Hosted {
		mux.HandleFunc("POST /luma", s.handleLogin)
		mux.HandleFunc("POST /csv", s.handleCSV)
		mux.HandleFunc("GET /logout", s.handleLogout)
	}
	mux.HandleFunc("GET /event/{eventID}", s.handleEvent)
	mux.HandleFunc("GET /event/{eventID}/print-all", s.handlePrintAll)
	mux.HandleFunc("GET /view/{eventID}/{email}", s.handleViewBadge)
	mux.HandleFunc("GET /print/{eventID}/{email}", s.handlePrintBadge)
	mux.HandleFunc("POST /print/{eventID}/{email}", s.handlePrintBadge)
	mux.HandleFunc("POST /event/{eventID}/print-all", s.handlePrintAll)
	mux.HandleFunc("POST /wifi-badge", s.handleWifiBadge)
	mux.HandleFunc("GET /printer", s.handlePrinterInfo)
	mux.HandleFunc("GET /jobs", s.handleJobs)
	mux.HandleFunc("POST /printer", s.handleSetPrinter)
	mux.HandleFunc("POST /submit", s.handleSubmit)
	mux.HandleFunc("GET /printed/{id}", s.handlePrinted)
	return mux
}

// print sends a badge to the printer: CUPS on a laptop; hosted, it waits
// for the page to fetch it (job.URL) and print it in the browser.
func (s *Server) print(img image.Image, prefix, label string) PrintJob {
	if !s.cfg.Hosted {
		return s.printer.Print(s.cfg, img, prefix, label)
	}
	job := PrintJob{Name: label, At: time.Now(), Status: "browser"}
	png, err := pkg.EncodeBadgePNG(img)
	if err != nil {
		job.Status, job.Error = "failed", err.Error()
		return job
	}
	job.URL = s.cfg.Base + "/printed/" + s.web.add(png)
	return job
}

// handlePrinted is a hosted badge, once, for the browser to print.
func (s *Server) handlePrinted(w http.ResponseWriter, r *http.Request) {
	png, ok := s.web.get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(png)
}

func (s *Server) getAPIKey(r *http.Request) string {
	if s.cfg.Hosted {
		return s.cfg.LumaKey
	}
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
	http.Redirect(w, r, s.cfg.Base+"/", http.StatusSeeOther)
}

// handleLogout clears the session.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.clearAPIKey(w)
	http.Redirect(w, r, s.cfg.Base+"/", http.StatusSeeOther)
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

	http.Redirect(w, r, s.cfg.Base+"/event/"+eventID, http.StatusSeeOther)
}

// handleEvent shows the guest list for an event.
func (s *Server) handleEvent(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("eventID")

	s.mu.RLock()
	eventData, eventOk := s.events[eventID]
	cachedGuests := s.guests[eventID]
	s.mu.RUnlock()

	if !eventOk {
		http.Redirect(w, r, s.cfg.Base+"/", http.StatusSeeOther)
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
			http.Redirect(w, r, s.cfg.Base+"/", http.StatusSeeOther)
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
		if !s.cfg.Hosted {
			wipeTestBadges(eventData.Event.StartAt, filepath.Join(appDir(), "badges"), eventID+"-")
		}
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

// testModeBefore is how long before an event's start test mode ends (as in
// Snout): prints until then are practice.
const testModeBefore = 6 * time.Hour

// wipeTestBadges removes an event's badge files printed in test mode, once
// the event is live.
func wipeTestBadges(startAt, dir, prefix string) {
	start, err := time.Parse(time.RFC3339, startAt)
	if err != nil {
		return
	}
	live := start.Add(-testModeBefore)
	if time.Now().Before(live) {
		return
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().Before(live) {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
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
			http.Redirect(w, r, s.cfg.Base+"/", http.StatusSeeOther)
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
		job := s.print(img, filepath.Join(appDir(), "badges", eventID+"-"), guest.Name)
		job.For = guest.Email
		jobs = append(jobs, job)
	}

	if r.Method == http.MethodPost {
		writeJSON(w, map[string]any{"count": len(jobs), "jobs": jobs})
		return
	}
	fmt.Fprintf(w, "OK: %d badges printing", len(jobs))
}

// guest finds one guest of an event. A Luma event's list is fetched again
// when it isn't loaded (Badger restarted since the page opened) or the
// person registered since.
func (s *Server) guest(r *http.Request, eventID, email string) (pkg.BadgeData, bool) {
	s.mu.RLock()
	data, ok := s.guests[eventID][email]
	s.mu.RUnlock()
	if ok || strings.HasPrefix(eventID, "csv-") {
		return data, ok
	}
	key := s.getAPIKey(r)
	if key == "" {
		return data, false
	}
	guests, err := pkg.GetGuests(eventID, key)
	if err != nil {
		log.Printf("Fetching the guests of %s: %v", eventID, err)
		return data, false
	}
	s.mu.Lock()
	s.guests[eventID] = guests
	s.mu.Unlock()
	data, ok = guests[email]
	return data, ok
}

// handleViewBadge shows a badge preview.
func (s *Server) handleViewBadge(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("eventID")
	email := r.PathValue("email")

	data, ok := s.guest(r, eventID, email)
	if !ok {
		http.Error(w, "This person isn't on the event's guest list any more. Reload the event page.", http.StatusNotFound)
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

	data, ok := s.guest(r, eventID, email)
	if !ok {
		if r.Method == http.MethodPost {
			writeJSON(w, PrintJob{For: email, At: time.Now(), Status: "failed", Error: "Not on the guest list any more, reload the page"})
		} else {
			http.Error(w, "This person isn't on the event's guest list any more. Reload the event page.", http.StatusNotFound)
		}
		return
	}

	img, err := s.renderBadge(data)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error generating badge: %v", err), http.StatusInternalServerError)
		return
	}

	job := s.print(img, filepath.Join(appDir(), "badges", eventID+"-"), data.Name)
	job.For = data.Email
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

	job := s.print(img, filepath.Join(appDir(), "badges", "adhoc-"), label)
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

// handlePrinterInfo is the printer panel's data: state, printers, jobs.
func (s *Server) handlePrinterInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.printer.Info())
}

// handleSetPrinter switches printers on (printers=A&printers=B; the rest
// off). At least one stays on.
func (s *Server) handleSetPrinter(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	known := map[string]bool{}
	list := listPrinters()
	for _, p := range list {
		known[p] = true
	}
	var names []string
	for _, n := range r.Form["printers"] {
		if n = strings.TrimSpace(n); n != "" && (len(list) == 0 || known[n]) {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		http.Error(w, "Switch on at least one printer", http.StatusBadRequest)
		return
	}
	if err := s.printer.SetNames(names); err != nil {
		log.Printf("Saving the printer choice: %v", err)
	}
	writeJSON(w, s.printer.Info())
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

// handleWifiBadge previews (preview=1: the PNG) or prints the WiFi card for
// one kind of network (password, open, sign-in page, username & password).
// copies (1-50) prints a stack, e.g. one per table.
func (s *Server) handleWifiBadge(w http.ResponseWriter, r *http.Request) {
	card := pkg.WifiCard{
		Kind:     pkg.WifiKind(r.FormValue("kind")),
		SSID:     strings.TrimSpace(r.FormValue("ssid")),
		Password: r.FormValue("password"),
		Username: strings.TrimSpace(r.FormValue("username")),
		SignIn:   strings.TrimSpace(r.FormValue("signin")),
	}
	ssid := card.SSID
	img, err := pkg.GenerateWifiBadge(card)
	if err != nil {
		// What's missing, in the card's words ("no network name").
		http.Error(w, strings.ToUpper(err.Error()[:1])+err.Error()[1:]+".", http.StatusBadRequest)
		return
	}
	if r.FormValue("preview") != "" {
		png, err := pkg.EncodeBadgePNG(img)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(png)
		return
	}
	copies, _ := strconv.Atoi(r.FormValue("copies"))
	copies = max(1, min(copies, 50))
	jobs := []PrintJob{}
	for i := 0; i < copies; i++ {
		jobs = append(jobs, s.print(img, filepath.Join(appDir(), "badges", "wifi-"), "WiFi "+ssid))
	}
	writeJSON(w, map[string]any{"count": len(jobs), "jobs": jobs})
}

// handleJobs is where the page's print jobs are now (?id=A-1&id=B-2):
// queued, printing, printed or failed, for the progress bars.
func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.printer.JobStatuses(r.URL.Query()["id"]))
}
