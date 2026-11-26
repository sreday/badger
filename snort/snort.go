// Package snort is Snort by Badger: push the websites' lineups from the
// WWW sheets, and drop speaker headshots that sort themselves to the right
// site by the colour behind the face. Nobody needs the sheets or GitHub.
//
// What a sheet holds never shows on a Snort page (Marek, 2026-11-25:
// "users of the app are not allowed to preview anything from spreadsheets,
// just push as is"): a push answers pushed, already up to date, or why it
// was refused, and links the commit. The email column is cut inside Google,
// by the Snort feed script, and Snort refuses a sheet that still holds an
// email address or a phone number. See README.md.
package snort

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"image/png"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sreday/badger/ui"
	"github.com/sreday/badger/web"
)

//go:embed templates/*.html
var templateFS embed.FS

const dayFmt = "2006-01-02"

type Config struct {
	Base          string // /snort (Snout strips it)
	DataDir       string // Snort keeps its state in DataDir/snort
	AdminUser     string
	AdminPassword string
	Version       string

	Feed   string // SNORT_FEED: the keyed address of the Snort feed script
	Tokens string // SNORT_GITHUB_TOKENS: owner=token,owner=token
}

func ConfigFromEnv() Config {
	return Config{Feed: os.Getenv("SNORT_FEED"), Tokens: os.Getenv("SNORT_GITHUB_TOKENS")}
}

// Push is the last push of an event: public facts only.
type Push struct {
	At     time.Time
	Result string // pushed, same, refused
	Note   string // the refusal, in Snort's own words
	Repo   string
	Commit string
	Build  string // building, live, failed
}

type state struct {
	Pushes map[string]*Push // event id -> the last push
}

type Server struct {
	cfg  Config
	tmpl *template.Template
	now  func() time.Time
	dir  string
	gh   *github

	feed func(ev Event) ([]string, [][]string, error) // tests replace it

	mu       sync.Mutex
	st       state
	events   map[string][]Event
	eventsAt time.Time
	eventErr string
	pushing  map[string]bool

	shotsMu sync.Mutex
	shots   map[string]*shot // uploads waiting for "Add headshots"

	tagsMu sync.Mutex
	tags   map[string]bool // Conf42's topic tags (src/thumbnails names), read once a day
	tagsAt time.Time

	logMu sync.Mutex
	log   []Entry // what happened, oldest first (log.go)
}

func New(cfg Config) (*Server, error) {
	funcs := template.FuncMap{
		"tool": ui.Tool,
		"when": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.UTC().Format("Mon 2 Jan, 15:04") + " UTC"
		},
		"date": func(d string) string {
			t, err := time.Parse(dayFmt, d)
			if err != nil {
				return d
			}
			return t.Format("Mon 2 Jan 2006")
		},
		"short": func(s string) string {
			if len(s) > 7 {
				return s[:7]
			}
			return s
		},
	}
	t, err := template.New("board.html").Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/board.html")
	if err == nil {
		t, err = ui.Parse(t)
	}
	if err != nil {
		return nil, fmt.Errorf("parsing templates: %w", err)
	}
	dir := filepath.Join(cfg.DataDir, "snort")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, tmpl: t, now: time.Now, dir: dir, gh: &github{tokens: parseTokens(cfg.Tokens)}, pushing: map[string]bool{}, shots: map[string]*shot{}}
	s.feed = func(ev Event) ([]string, [][]string, error) { return readFeed(s.cfg.Feed, ev) }
	readJSON(filepath.Join(dir, "state.json"), &s.st)
	readJSON(filepath.Join(dir, "events.json"), &s.events)
	readJSON(s.logPath(), &s.log)
	if s.st.Pushes == nil {
		s.st.Pushes = map[string]*Push{}
	}
	return s, nil
}

// Start reads the events list in the background, then every few hours.
func (s *Server) Start() {
	go func() {
		for {
			s.refreshEvents()
			time.Sleep(eventsTTL)
		}
	}()
}

func (s *Server) refreshEvents() {
	list, errs := s.readEvents(s.now())
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.events == nil {
		s.events = map[string][]Event{}
	}
	for b, l := range list {
		s.events[b] = l // a brand that failed keeps its old list
	}
	s.eventsAt, s.eventErr = s.now(), errs
	writeJSON(filepath.Join(s.dir, "events.json"), s.events)
	if errs != "" {
		log.Printf("snort: events: %d brands read; %s", len(list), errs)
	}
}

func (s *Server) event(id string) (Event, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.events {
		for _, ev := range l {
			if ev.ID == id {
				return ev, true
			}
		}
	}
	return Event{}, false
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

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.requireAdmin(s.handleBoard))
	mux.HandleFunc("POST /admin/push", s.requireAdmin(s.handlePush))
	mux.HandleFunc("GET /admin/build", s.requireAdmin(s.handleBuild))
	mux.HandleFunc("POST /admin/refresh", s.requireAdmin(s.handleRefresh))
	mux.HandleFunc("POST /admin/headshots", s.requireAdmin(s.handleUpload))
	mux.HandleFunc("POST /admin/headshots/add", s.requireAdmin(s.handleAddShots))
	mux.HandleFunc("GET /shot/{id}", s.requireAdmin(s.handleShot))
	return mux
}

type row struct {
	Event
	Past bool
	Last *Push
	Busy bool
}

type brandTab struct {
	ID, Name string
	On       bool
	Rows     []row
}

func (s *Server) handleBoard(w http.ResponseWriter, r *http.Request) {
	on := r.URL.Query().Get("b")
	if brandTabs[on] == "" {
		on = "sreday"
	}
	now := s.now()
	s.mu.Lock()
	var tabs []brandTab
	for _, b := range brandOrder {
		t := brandTab{ID: b, Name: brandTabs[b], On: b == on}
		for _, ev := range s.events[b] {
			t.Rows = append(t.Rows, row{Event: ev, Past: !ev.Upcoming(now), Last: s.st.Pushes[ev.ID], Busy: s.pushing[ev.ID]})
		}
		tabs = append(tabs, t)
	}
	loaded, errs := !s.eventsAt.IsZero() || len(s.events) > 0, s.eventErr
	s.mu.Unlock()
	var keys []string
	for _, b := range brandOrder {
		if !s.gh.canWrite(siteRepos[b]) {
			keys = append(keys, brandNames[b])
		}
	}
	if !s.gh.canWrite(conf42Static) && !contains(keys, "Conf42") {
		keys = append(keys, "Conf42 headshots")
	}
	s.render(w, "board.html", map[string]any{
		"Tabs": tabs, "On": on, "Loaded": loaded, "EventErr": errs != "",
		"NoFeed": s.cfg.Feed == "", "NoKeys": keys, "Version": s.cfg.Version,
		"Log": s.recent(logShow),
	})
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		log.Printf("snort: template %s: %v", name, err)
		http.Error(w, "page error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buf.Bytes())
}

func writeJSONResp(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	s.refreshEvents()
	e := Entry{Who: who(r), What: "refresh", Result: "read", Note: "events read again from the websites"}
	s.mu.Lock()
	if s.eventErr != "" {
		e.Result, e.Note = "refused", "some brands could not be read, they keep their old list"
	}
	s.mu.Unlock()
	s.record(e)
	http.Redirect(w, r, "./?b="+r.FormValue("b"), http.StatusSeeOther)
}

// --- Push -----------------------------------------------------------------------

type pushAnswer struct {
	Result string `json:"result"` // pushed, same, refused
	Note   string `json:"note"`
	Commit string `json:"commit,omitempty"`
	URL    string `json:"url,omitempty"`
}

func (s *Server) handlePush(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("event")
	ev, ok := s.event(id)
	if !ok {
		writeJSONResp(w, http.StatusNotFound, pushAnswer{Result: "refused", Note: "no such event"})
		return
	}
	s.mu.Lock()
	if s.pushing[id] {
		s.mu.Unlock()
		writeJSONResp(w, http.StatusConflict, pushAnswer{Result: "refused", Note: "a push of this event is already running"})
		return
	}
	s.pushing[id] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pushing, id)
		s.mu.Unlock()
	}()
	a := s.push(ev)
	e := Entry{Who: who(r), What: "push", Event: ev.Name, Result: a.Result, Note: a.Note}
	if a.Commit != "" {
		e.Repo, e.Commit = ev.Repo, a.Commit
	}
	s.record(e)
	writeJSONResp(w, http.StatusOK, a)
}

// push reads the event's tab through the feed and commits it as the site's
// lineup file. Nothing from the sheet goes into the answer.
func (s *Server) push(ev Event) pushAnswer {
	file, err := s.lineupFor(ev)
	if err != nil {
		return s.note(ev, &Push{Result: "refused", Note: err.Error()})
	}
	old, err := s.gh.raw(ev.Repo, ev.File)
	if err == nil && bytes.Equal(old, file) {
		return s.note(ev, &Push{Result: "same", Note: "the website already has this lineup"})
	}
	sha, err := s.gh.commit(ev.Repo, "Snort: "+ev.Name+" lineup from the sheet", map[string][]byte{ev.File: file})
	if err != nil {
		log.Printf("snort: push %s: commit failed", ev.ID)
		return s.note(ev, &Push{Result: "refused", Note: "GitHub did not take it, try again in a minute"})
	}
	log.Printf("snort: pushed %s (%d bytes)", ev.ID, len(file))
	return s.note(ev, &Push{Result: "pushed", Note: "pushed, the website is building", Repo: ev.Repo, Commit: sha, Build: "building"})
}

// lineupFor builds the site file of an event from the sheet, or says why not.
func (s *Server) lineupFor(ev Event) ([]byte, error) {
	if !s.gh.canWrite(ev.Repo) {
		return nil, fmt.Errorf("Snort has no GitHub key for %s yet, ask Marek", brandNames[ev.Brand])
	}
	want := siteHeader
	if cur, err := s.gh.raw(ev.Repo, ev.File); err == nil {
		h, _, err := parseCSV(cur)
		if err != nil {
			return nil, errColumns
		}
		want = h
	} else if ev.Brand == "conf42" {
		return nil, errNoSiteFile // Conf42's columns change by year: start from a file
	}
	header, rows, err := s.feed(ev)
	if err != nil {
		return nil, err
	}
	file, _, err := lineupFile(want, header, rows)
	return file, err
}

func (s *Server) note(ev Event, p *Push) pushAnswer {
	p.At = s.now()
	a := pushAnswer{Result: p.Result, Note: p.Note, Commit: p.Commit}
	if p.Commit != "" {
		a.URL = "https://github.com/" + p.Repo + "/commit/" + p.Commit
	}
	s.mu.Lock()
	// "Up to date" or a refusal keeps the last commit and its build.
	if old := s.st.Pushes[ev.ID]; old != nil && p.Commit == "" && old.Commit != "" {
		p.Repo, p.Commit, p.Build = old.Repo, old.Commit, old.Build
	}
	s.st.Pushes[ev.ID] = p
	writeJSON(filepath.Join(s.dir, "state.json"), s.st)
	s.mu.Unlock()
	return a
}

// handleBuild answers how the site build of the event's last push is going.
func (s *Server) handleBuild(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("event")
	s.mu.Lock()
	p := s.st.Pushes[id]
	var repo, sha, was string
	if p != nil {
		repo, sha, was = p.Repo, p.Commit, p.Build
	}
	s.mu.Unlock()
	if sha == "" {
		writeJSONResp(w, http.StatusOK, map[string]string{"build": ""})
		return
	}
	if was == "building" || was == "" {
		if st := s.gh.buildState(repo, sha); st != "" {
			was = st
			s.mu.Lock()
			changed := false
			if q := s.st.Pushes[id]; q != nil && q.Commit == sha && q.Build != st {
				q.Build, changed = st, true
				writeJSON(filepath.Join(s.dir, "state.json"), s.st)
			}
			s.mu.Unlock()
			if changed && st != "building" {
				ev, _ := s.event(id)
				note := "the website is live"
				if st == "failed" {
					note = "the website build failed"
				}
				s.record(Entry{What: "build", Event: ev.Name, Result: st, Note: note, Repo: repo, Commit: sha})
			}
		}
	}
	writeJSONResp(w, http.StatusOK, map[string]string{"build": was})
}

// --- Headshots ------------------------------------------------------------------

// shot is one uploaded headshot waiting for "Add headshots".
type shot struct {
	ID      string `json:"id"`
	Upload  string `json:"upload"` // the file name as uploaded
	Name    string `json:"name"`   // the name it gets on the site
	Brand   string `json:"brand"`  // where it goes; "" = Snort can't tell
	Event   string `json:"event,omitempty"`
	Choices []opt  `json:"choices,omitempty"` // where it may go, when unsure
	Replace bool   `json:"replace"`           // the site already has a file of that name
	Error   string `json:"error,omitempty"`
	Done    string `json:"done,omitempty"`
	data    []byte
	at      time.Time
}

type opt struct {
	ID, Name string
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeJSONResp(w, http.StatusBadRequest, map[string]string{"error": "the upload did not arrive whole"})
		return
	}
	var out []*shot
	for _, fh := range r.MultipartForm.File["files"] {
		sh := &shot{ID: newID(), Upload: path.Base(strings.ReplaceAll(fh.Filename, `\`, "/")), at: s.now()}
		out = append(out, sh)
		if fh.Size > maxHeadshot {
			sh.Error = "bigger than 5 MB"
			continue
		}
		f, err := fh.Open()
		if err != nil {
			sh.Error = "could not read it"
			continue
		}
		sh.data, _ = io.ReadAll(io.LimitReader(f, maxHeadshot+1))
		f.Close()
		s.sortShot(sh)
	}
	s.shotsMu.Lock()
	for id, sh := range s.shots { // forget uploads nobody added within a day
		if s.now().Sub(sh.at) > 24*time.Hour {
			delete(s.shots, id)
		}
	}
	for _, sh := range out {
		if sh.Error == "" {
			s.shots[sh.ID] = sh
		}
	}
	s.shotsMu.Unlock()
	if len(out) > 0 {
		s.record(uploadEntry(who(r), out))
	}
	writeJSONResp(w, http.StatusOK, map[string]any{"shots": out})
}

// sortShot finds where an upload goes, from the picture itself.
func (s *Server) sortShot(sh *shot) {
	img, err := png.Decode(bytes.NewReader(sh.data))
	if err != nil {
		sh.Error = "not a PNG headshot"
		return
	}
	if !safeName.MatchString(sh.Upload) {
		sh.Error = `rename it to "Full Name.png" first`
		return
	}
	b := img.Bounds()
	// A Conf42 headshot carries its topic tag in the name: the tag decides.
	// (They sit on a sky, not on a circle, so this comes before the corners.)
	if tag := conf42Tag(sh.Upload); tag != "" && (s.knownTag(tag) || !(b.Dx() == 1000 && b.Dy() == 1000)) {
		s.sortConf42(sh, tag)
		sh.Replace = s.exists(sh)
		return
	}
	if !seeThroughCorners(img) {
		sh.Error = `not on a circle: make it with the site's headshot maker first (Conf42 ones are named "Full Name_<topic>.png")`
		return
	}
	switch {
	case b.Dx() == 1000 && b.Dy() == 1000:
		sh.Name = sh.Upload
		sh.Brand = circleBrand(img)
		switch sh.Brand {
		case "pec":
			sh.Brand, sh.Error = "", "a PEC headshot: PEC is not in Snort for now"
			return
		case "":
			sh.Choices = []opt{{"sreday", "SREday"}, {"llmday", "LLMday"}, {"platformday", "PLATFORMday"}}
		}
	default:
		sh.Error = `not a headshot: 1000x1000 for SREday, LLMday, PLATFORMday; Conf42 ones are named "Full Name_<topic>.png"`
		return
	}
	if sh.Brand != "" && sh.Error == "" {
		sh.Replace = s.exists(sh)
	}
}

// sortConf42 files a tagged headshot under Conf42 as it is named, and says
// which upcoming event uses that tag. An unknown tag still goes (a new topic
// may not have its sky yet), with a word on the card.
func (s *Server) sortConf42(sh *shot, tag string) {
	sh.Name, sh.Brand = sh.Upload, "conf42"
	s.mu.Lock()
	evs := append([]Event(nil), s.events["conf42"]...)
	s.mu.Unlock()
	now := s.now()
	for _, ev := range evs {
		if ev.Suffix == tag && ev.Upcoming(now) {
			sh.Event = ev.Name
			return
		}
	}
	if !s.knownTag(tag) {
		sh.Event = "new tag _" + tag
	}
}

// knownTag says whether a Conf42 lineup or sky uses the tag.
func (s *Server) knownTag(tag string) bool {
	s.mu.Lock()
	for _, ev := range s.events["conf42"] {
		if ev.Suffix == tag {
			s.mu.Unlock()
			return true
		}
	}
	s.mu.Unlock()
	return s.conf42Tags()[tag]
}

// conf42Tags are the names of Conf42's topic skies (src/thumbnails), read once a day.
func (s *Server) conf42Tags() map[string]bool {
	s.tagsMu.Lock()
	defer s.tagsMu.Unlock()
	if s.tags != nil && s.now().Sub(s.tagsAt) < 24*time.Hour {
		return s.tags
	}
	body, err := s.gh.get(siteRepos["conf42"], "contents/thumbnails")
	if err != nil {
		return s.tags
	}
	var entries []struct{ Name, Type string }
	if json.Unmarshal(body, &entries) != nil {
		return s.tags
	}
	tags := map[string]bool{}
	for _, en := range entries {
		if en.Type == "file" && strings.HasSuffix(en.Name, ".png") && !strings.HasPrefix(en.Name, "_") {
			tags[strings.TrimSuffix(en.Name, ".png")] = true
		}
	}
	if len(tags) > 0 {
		s.tags, s.tagsAt = tags, s.now()
	}
	return s.tags
}

// target is the repo and path a sorted headshot goes to.
func target(sh *shot) (repo, file string) {
	if sh.Brand == "conf42" {
		return conf42Static, "headshots/" + sh.Name
	}
	return siteRepos[sh.Brand], "speakers/" + sh.Name
}

func (s *Server) exists(sh *shot) bool {
	repo, file := target(sh)
	_, err := s.gh.get(repo, "contents/"+escapePath(file)+"?ref=main")
	return err == nil
}

// handleAddShots commits the uploads: one commit per site. Unsure ones
// come with the brand (or Conf42 sky) the person picked.
func (s *Server) handleAddShots(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs  []string          `json:"ids"`
		Pick map[string]string `json:"pick"` // id -> brand, for the ones Snort couldn't tell
	}
	if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req) != nil {
		writeJSONResp(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	s.shotsMu.Lock()
	var todo []*shot
	for _, id := range req.IDs {
		if sh := s.shots[id]; sh != nil {
			todo = append(todo, sh)
		}
	}
	s.shotsMu.Unlock()
	byRepo := map[string]map[string][]byte{}
	shotsOf := map[string][]*shot{}
	for _, sh := range todo {
		if sh.Brand == "" {
			p := req.Pick[sh.ID]
			for _, c := range sh.Choices {
				if c.ID == p {
					sh.Brand = p
				}
			}
			if sh.Brand == "" {
				sh.Error = "pick where it goes"
				continue
			}
		}
		repo, file := target(sh)
		if !s.gh.canWrite(repo) {
			sh.Error = "Snort has no GitHub key for this site yet, ask Marek"
			continue
		}
		if byRepo[repo] == nil {
			byRepo[repo] = map[string][]byte{}
		}
		byRepo[repo][file] = sh.data
		shotsOf[repo] = append(shotsOf[repo], sh)
	}
	repos := make([]string, 0, len(byRepo))
	for repo := range byRepo {
		repos = append(repos, repo)
	}
	sort.Strings(repos)
	for _, repo := range repos {
		n := len(shotsOf[repo])
		word := "headshots"
		if n == 1 {
			word = "headshot"
		}
		sha, err := s.gh.commit(repo, fmt.Sprintf("Snort: %d %s", len(byRepo[repo]), word), byRepo[repo])
		var files []string
		for _, sh := range shotsOf[repo] {
			files = append(files, sh.Name)
		}
		e := Entry{Who: who(r), What: "headshots", Result: "added", Repo: repo, Commit: sha, Note: fmt.Sprintf("%d %s: %s", n, word, strings.Join(files, ", "))}
		if err != nil {
			e.Result, e.Commit, e.Note = "refused", "", "GitHub did not take "+e.Note
		}
		s.record(e)
		for _, sh := range shotsOf[repo] {
			if err != nil {
				sh.Error = "GitHub did not take it, try again in a minute"
			} else {
				sh.Done = "https://github.com/" + repo + "/commit/" + sha
			}
		}
		if err == nil {
			log.Printf("snort: %d headshots to %s", len(byRepo[repo]), repo)
		}
	}
	s.shotsMu.Lock()
	for _, sh := range todo {
		if sh.Done != "" {
			delete(s.shots, sh.ID)
		}
	}
	s.shotsMu.Unlock()
	writeJSONResp(w, http.StatusOK, map[string]any{"shots": todo})
}

// handleShot shows an upload back (the person's own file, not a sheet's).
func (s *Server) handleShot(w http.ResponseWriter, r *http.Request) {
	s.shotsMu.Lock()
	sh := s.shots[r.PathValue("id")]
	s.shotsMu.Unlock()
	if sh == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Write(sh.data)
}

// --- Small helpers --------------------------------------------------------------

func readJSON(p string, v any) bool {
	data, err := os.ReadFile(p)
	return err == nil && json.Unmarshal(data, v) == nil
}

func writeJSON(p string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
