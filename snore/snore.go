// Package snore is Snore by Badger: a board of speaker prospects, the top
// voices of each topic near every city we have run or planned (SRE, LLM,
// PLATFORM) and for each Conf42 topic. A Claude routine on the
// subscription refreshes it every Saturday from open, public speaker
// sources; anyone who lands on one of our lineups leaves the board. Snore
// never calls a model itself. See README.md.
package snore

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sreday/badger/sites"
	"github.com/sreday/badger/ui"
	"github.com/sreday/badger/web"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed mascots/*.png
var mascotFS embed.FS

const dayFmt = "2006-01-02"

type Config struct {
	Base          string // /snore (Snout strips it)
	DataDir       string // Snore keeps everything in DataDir/snore
	AdminUser     string
	AdminPassword string
	Version       string
	PublicURL     string

	Repos    []string // site repos, for the footprint
	HumansDB string   // SNORE_HUMANS_DB: a read-only login to the humans app's database
	CFPDB    string   // SNORE_CFP_DB: a read-only login to cfp.ninja's database
}

func ConfigFromEnv() Config {
	return Config{Repos: sites.Repos(), HumansDB: os.Getenv("SNORE_HUMANS_DB"), CFPDB: os.Getenv("SNORE_CFP_DB")}
}

// state is what Snore keeps between restarts.
type state struct {
	Cards     map[string][]*Card              // topic -> best first
	Rejected  map[string]map[string]time.Time // topic -> name key -> when
	Invited   map[string]time.Time            // name key -> when
	Confirmed map[string]Confirmed            // name key -> LinkedIn count typed in
	Fresh     map[string]time.Time            // topic -> last Claude answer
	Graduated map[string]string               // name key -> the lineup that took them off
}

type Server struct {
	cfg   Config
	tmpl  *template.Template
	now   func() time.Time
	dir   string
	token string
	fp    *Footprint

	mu    sync.Mutex
	st    state
	jobs  []*Job
	fetch func(url string) ([]byte, string, error) // a picture
	page  func(url string) (string, error)         // a page that may show one
	pause time.Duration

	humans     map[string][]humanRow // the last read of the humans app, by name
	humansErr  string
	cfp        map[string]CFP // the last read of cfp.ninja, by name
	readCFP    func(ctx context.Context, dsn string) (map[string]CFP, error)
	readHumans func(ctx context.Context, dsn string) (map[string][]humanRow, error)
	sources    []*Source
	feedsDay   string                  // the day the feeds were last all read
	pool       map[string]*PoolSpeaker // name key -> speaker, from the feeds
	feed       func(url string) ([]feedSpeaker, error)
	bg         sync.WaitGroup // the photo runs in the background
	photoBusy  bool
}

func New(cfg Config) (*Server, error) {
	funcs := template.FuncMap{
		"tool": ui.Tool,
		"k":    fmtCount,
		"initial": func(s string) string {
			for _, r := range s {
				return strings.ToUpper(string(r))
			}
			return ""
		},
		"talks":    cardTalks,
		"cardView": func(c *Card, brand string) cardView { return cardView{c, brand} },
		"date": func(d string) string {
			t, err := time.Parse(dayFmt, d)
			if err != nil {
				return d
			}
			return t.Format("2 Jan 2006")
		},
		"day": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.UTC().Format("Mon 2 Jan")
		},
	}
	t, err := template.New("board.html").Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/board.html")
	if err == nil {
		t, err = ui.Parse(t)
	}
	if err != nil {
		return nil, fmt.Errorf("parsing templates: %w", err)
	}
	dir := filepath.Join(cfg.DataDir, "snore")
	if err := os.MkdirAll(filepath.Join(dir, "photos"), 0o700); err != nil {
		return nil, err
	}
	token, err := loadSecret(filepath.Join(dir, "token"))
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, tmpl: t, now: time.Now, dir: dir, token: token, fetch: fetchPhoto, page: fetchPage, pause: photoPause, readHumans: readHumans, readCFP: readCFP, feed: fetchFeed, pool: map[string]*PoolSpeaker{},
		fp: &Footprint{Repos: cfg.Repos, path: filepath.Join(dir, "footprint.json")}}
	s.fp.load()
	readJSON(filepath.Join(dir, "state.json"), &s.st)
	readJSON(filepath.Join(dir, "jobs.json"), &s.jobs)
	readJSON(filepath.Join(dir, "sources.json"), &s.sources)
	readJSON(filepath.Join(dir, "pool.json"), &s.pool)
	if s.pool == nil {
		s.pool = map[string]*PoolSpeaker{}
	}
	s.st.init()
	return s, nil
}

func (st *state) init() {
	if st.Cards == nil {
		st.Cards = map[string][]*Card{}
	}
	if st.Rejected == nil {
		st.Rejected = map[string]map[string]time.Time{}
	}
	if st.Invited == nil {
		st.Invited = map[string]time.Time{}
	}
	if st.Confirmed == nil {
		st.Confirmed = map[string]Confirmed{}
	}
	if st.Fresh == nil {
		st.Fresh = map[string]time.Time{}
	}
	if st.Graduated == nil {
		st.Graduated = map[string]string{}
	}
}

func (s *Server) save() { writeJSON(filepath.Join(s.dir, "state.json"), s.st) }

// Start keeps the footprint fresh, takes speakers who joined a lineup off
// the board, and queues Saturday's scouting.
func (s *Server) Start() {
	go func() {
		for {
			s.fp.Refresh(s.now())
			s.mu.Lock()
			s.prune(s.now())
			s.mu.Unlock()
			s.syncHumans()
			s.photosLater()
			time.Sleep(footprintEvery)
		}
	}()
	go func() {
		for {
			s.mu.Lock()
			now := s.now().UTC()
			feeds := now.Hour() >= daily0-1 && s.feedsDay != now.Format(dayFmt) // from 04:00 UTC: every feed read again before the day's jobs
			if feeds {
				s.feedsDay = now.Format(dayFmt)
			}
			s.mu.Unlock()
			if feeds {
				s.readSources(true)
			}
			s.mu.Lock()
			s.dueJobs(s.now())
			s.mu.Unlock()
			time.Sleep(10 * time.Minute)
		}
	}()
}

// prune takes off every card whose speaker is now on one of our lineups,
// and the tabs of Conf42 events that have passed.
func (s *Server) prune(now time.Time) {
	lineups := s.fp.Lineups()
	changed := false
	live := map[string]bool{}
	for _, t := range s.fp.Topics(now) {
		live[t.ID] = true
	}
	for id := range s.st.Cards {
		if !live[id] && len(live) > 0 { // a Conf42 event that has passed
			delete(s.st.Cards, id)
			changed = true
		}
	}
	for topic, cs := range s.st.Cards {
		var keep []*Card
		for _, c := range cs {
			hit := ""
			for _, k := range nameKeys(c.Name) {
				if w, ok := lineups[k]; ok {
					hit = w
				}
			}
			if hit != "" {
				s.st.Graduated[c.Key] = hit
				changed = true
				continue
			}
			keep = append(keep, c)
		}
		s.st.Cards[topic] = keep
	}
	if changed {
		s.save()
	}
}

func (s *Server) exclusions() exclusions {
	return exclusions{lineups: s.fp.Lineups(), rejected: s.st.Rejected, invited: s.st.Invited}
}

func (s *Server) topic(id string) (Topic, bool) {
	for _, t := range s.fp.Topics(s.now()) {
		if t.ID == id {
			return t, true
		}
	}
	return Topic{}, false
}

// --- HTTP ---------------------------------------------------------------------

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Robots-Tag", "noindex")
		if web.AdminLocked(r) {
			web.TooMany(w)
			return
		}
		user, pass, ok := r.BasicAuth()
		userOK := s.cfg.AdminUser == "" || subtle.ConstantTimeCompare([]byte(user), []byte(s.cfg.AdminUser)) == 1
		if !ok || !userOK || s.cfg.AdminPassword == "" || subtle.ConstantTimeCompare([]byte(pass), []byte(s.cfg.AdminPassword)) != 1 {
			if ok {
				web.AdminFailed(r, pass)
			}
			w.Header().Set("WWW-Authenticate", `Basic realm="Snout by Badger"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		web.AdminPassed(r)
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

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.requireAdmin(s.handleBoard))
	mux.HandleFunc("GET /photo/{file}", s.requireAdmin(s.handlePhoto))
	mux.HandleFunc("GET /mascot/{file}", s.requireAdmin(handleMascot))
	mux.HandleFunc("GET /admin/status", s.requireAdmin(s.handleStatus))
	mux.HandleFunc("POST /admin/{action}", s.requireAdmin(s.handleAction))
	mux.HandleFunc("GET /api/queue", s.requireToken(s.handleQueue))
	mux.HandleFunc("POST /api/check", s.requireToken(s.handleCheck))
	mux.HandleFunc("POST /api/answers", s.requireToken(s.handleAnswers))
	mux.HandleFunc("POST /api/sources", s.requireToken(s.handleSources))
	mux.HandleFunc("GET /api/candidates", s.requireToken(s.handleCandidates))
	return mux
}

type tab struct {
	ID, Name, Brand string
	On              bool
}

// section is one of our cities and the speakers near it.
type section struct {
	City  City
	Cards []*Card
}

type boardData struct {
	Tabs      []tab // SRE LLM PLATFORM Conf42
	Conf42    []tab // the upcoming Conf42 events, on the Conf42 tab
	Cur       Topic
	Sections  []section // in-person: by city, the most relevant first
	CityPills []tab     // in-person: All + one per city with speakers
	Cards     []*Card   // Conf42: one list
	Fresh     time.Time
	Pending   int
	Scouting  bool // a run has taken a job
	Late      bool // a daily job still open 4 hours after it was queued
	HumansErr string
	Err       string
}

func (s *Server) handleBoard(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	topics := s.fp.Topics(now)
	want := r.FormValue("t")
	cur := topics[0]
	for _, t := range topics {
		if t.ID == want || (want == "conf42" && t.Online && !cur.Online) {
			cur = t
		}
	}
	d := boardData{Cur: cur, Fresh: s.st.Fresh[cur.ID], Err: s.fp.Err, HumansErr: s.humansErr}
	if s.cfg.HumansDB == "" {
		d.HumansErr = "humans app not connected"
	}
	for _, t := range topics {
		if t.Online {
			d.Conf42 = append(d.Conf42, tab{t.ID, t.Name, t.Brand, t.ID == cur.ID})
		} else {
			d.Tabs = append(d.Tabs, tab{t.ID, t.Name, t.Brand, t.ID == cur.ID})
		}
	}
	if len(d.Conf42) > 0 {
		d.Tabs = append(d.Tabs, tab{"conf42", "Conf42", "conf42", cur.Online})
	}
	if cur.Online {
		d.Cards = s.st.Cards[cur.ID]
	} else {
		all := sections(cur, s.st.Cards[cur.ID])
		pick := r.FormValue("c")
		if len(all) > 0 {
			d.CityPills = append(d.CityPills, tab{"", "All", cur.Brand, pick == ""})
		}
		for _, sec := range all {
			on := normName(sec.City.Name) == normName(pick)
			d.CityPills = append(d.CityPills, tab{sec.City.Name, sec.City.Name, cur.Brand, on})
			if pick == "" || on {
				d.Sections = append(d.Sections, sec)
			}
		}
	}
	for _, j := range s.jobs {
		if j.pending(now) {
			d.Pending++
			if !j.Handed.IsZero() {
				d.Scouting = true
			}
			if j.Auto && now.Sub(j.Asked) > 4*time.Hour {
				d.Late = true
			}
		}
	}
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, "board.html", d); err != nil {
		log.Printf("Snore template error: %v", err)
		http.Error(w, "Snore couldn't draw this page: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buf.Bytes())
}

// sections puts each speaker under the nearest of our cities (their first
// city, as Claude ranks them closest first). Cities with an event coming
// lead, the soonest first; then the others, the most recent first. Empty
// cities are left out.
func sections(t Topic, cards []*Card) []section {
	byCity := map[string]*section{}
	for _, c := range t.Cities {
		byCity[normName(c.Name)] = &section{City: c}
	}
	var out []*section
	for _, c := range cards {
		for _, name := range c.Cities {
			if sec := byCity[normName(name)]; sec != nil {
				if len(sec.Cards) == 0 {
					out = append(out, sec)
				}
				sec.Cards = append(sec.Cards, c)
				break
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].City, out[j].City
		if (a.Next != "") != (b.Next != "") {
			return a.Next != ""
		}
		if a.Next != b.Next {
			return a.Next < b.Next
		}
		return a.Last > b.Last
	})
	res := make([]section, len(out))
	for i, sec := range out {
		res[i] = *sec
	}
	return res
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, j := range s.jobs {
		if j.pending(s.now()) {
			n++
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"pending": n})
}

func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	key := normName(r.FormValue("key"))
	switch r.PathValue("action") {
	case "reject": // not a fit for this topic
		t := r.FormValue("topic")
		if s.st.Rejected[t] == nil {
			s.st.Rejected[t] = map[string]time.Time{}
		}
		s.st.Rejected[t][key] = now
		s.drop(t, key)
	case "invited": // off every topic for 90 days
		s.st.Invited[key] = now
		for t := range s.st.Cards {
			s.drop(t, key)
		}
	case "refresh":
		var ids []string
		for _, t := range s.fp.Topics(now) {
			if r.FormValue("topic") == "" || t.ID == r.FormValue("topic") {
				ids = append(ids, t.ID)
			}
		}
		s.queue(ids, weekID(now), false, now)
	default:
		http.NotFound(w, r)
		return
	}
	s.save()
	if r.Header.Get("X-Snore") != "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, s.cfg.Base+"/?t="+r.FormValue("topic"), http.StatusSeeOther)
}

func (s *Server) drop(topic, key string) {
	var keep []*Card
	for _, c := range s.st.Cards[topic] {
		if c.Key != key {
			keep = append(keep, c)
		}
	}
	s.st.Cards[topic] = keep
}

var photoFile = regexp.MustCompile(`^[0-9a-f]{16}\.(jpg|png|webp|gif)$`)

func (s *Server) handlePhoto(w http.ResponseWriter, r *http.Request) {
	f := r.PathValue("file")
	if !photoFile.MatchString(f) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, filepath.Join(s.dir, "photos", f))
}

// --- Photos: a copy of each, so a picture doesn't vanish when its site moves --

func fetchPhoto(url string) ([]byte, string, error) {
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Get(url)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return data, resp.Header.Get("Content-Type"), err
}

var photoExt = map[string]string{"image/jpeg": "jpg", "image/png": "png", "image/webp": "webp", "image/gif": "gif"}

// keepPhoto copies one picture into the data dir and returns its file name
// ("" when it isn't an image). Called without the lock.
func (s *Server) keepPhoto(u string) string {
	data, ct, err := s.fetch(u)
	if err != nil || len(data) == 0 {
		return ""
	}
	if ct == "" || ct == "application/octet-stream" || ct == "binary/octet-stream" {
		ct = http.DetectContentType(data)
	}
	ext := photoExt[strings.TrimSpace(strings.Split(ct, ";")[0])]
	if ext == "" {
		return ""
	}
	sum := sha1.Sum([]byte(u))
	name := hex.EncodeToString(sum[:])[:16] + "." + ext
	if os.WriteFile(filepath.Join(s.dir, "photos", name), data, 0o600) != nil {
		return ""
	}
	return name
}

// --- Small helpers --------------------------------------------------------------

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

func readJSON(path string, v any) bool {
	data, err := os.ReadFile(path)
	return err == nil && json.Unmarshal(data, v) == nil
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func weekID(t time.Time) string {
	y, w := t.UTC().ISOWeek()
	return fmt.Sprintf("%d-W%02d", y, w)
}

// fmtCount: 950, 1.2K, 12K, 1.3M.
func fmtCount(n int) string {
	switch {
	case n >= 1_000_000:
		return strings.TrimSuffix(strconv.FormatFloat(float64(n)/1e6, 'f', 1, 64), ".0") + "M"
	case n >= 10_000:
		return strconv.Itoa(n/1000) + "K"
	case n >= 1000:
		return strings.TrimSuffix(strconv.FormatFloat(float64(n)/1e3, 'f', 1, 64), ".0") + "K"
	}
	return strconv.Itoa(n)
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

var mascotFile = regexp.MustCompile(`^(sreday|llmday|platformday|conf42|frog)\.png$`)

func handleMascot(w http.ResponseWriter, r *http.Request) {
	f := r.PathValue("file")
	if !mascotFile.MatchString(f) {
		http.NotFound(w, r)
		return
	}
	b, err := mascotFS.ReadFile("mascots/" + f)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=604800")
	w.Write(b)
}
