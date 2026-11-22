package snore

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Event feeds: public speaker lists that conferences publish. Claude finds
// them (it can search), Snore reads them (it can reach them) and keeps a
// pool of speakers that every job draws its candidates from. The feed's
// address pattern lives in the private recipe ("FEED <url with %s>"), not
// in this repo.

const (
	feedEvery     = 20 * time.Hour
	poolTalksMax  = 6
	candidatesMax = 120
)

var feedID = regexp.MustCompile(`^[a-z0-9]{6,12}$`)

// Source is one conference's public speaker feed.
type Source struct {
	ID       string    `json:"id"`
	Event    string    `json:"event"` // name and year
	City     string    `json:"city,omitempty"`
	Country  string    `json:"country,omitempty"`
	Date     string    `json:"date,omitempty"` // YYYY-MM-DD
	URL      string    `json:"url,omitempty"`  // the conference's page
	Added    time.Time `json:"added"`
	Read     time.Time `json:"read,omitempty"`
	Speakers int       `json:"speakers"`
	Err      string    `json:"error,omitempty"`
}

// PoolSpeaker is a speaker as the feeds show them.
type PoolSpeaker struct {
	Name     string   `json:"name"`
	Tagline  string   `json:"tagline,omitempty"`
	Bio      string   `json:"bio,omitempty"`
	Photo    string   `json:"photo,omitempty"`
	LinkedIn string   `json:"linkedin,omitempty"`
	Links    []string `json:"links,omitempty"`
	Talks    []Talk   `json:"talks"`
}

type feedSpeaker struct {
	FullName       string `json:"fullName"`
	TagLine        string `json:"tagLine"`
	Bio            string `json:"bio"`
	ProfilePicture string `json:"profilePicture"`
	Sessions       []struct {
		Name string `json:"name"`
	} `json:"sessions"`
	Links []struct {
		URL      string `json:"url"`
		LinkType string `json:"linkType"`
	} `json:"links"`
}

// feedURL reads the address pattern from the private recipe.
func (s *Server) feedURL(id string) string {
	b, _ := os.ReadFile(filepath.Join(s.dir, "recipe.txt"))
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Fields(l); len(f) == 2 && f[0] == "FEED" && strings.Contains(f[1], "%s") {
			return fmt.Sprintf(f[1], id)
		}
	}
	return ""
}

func fetchFeed(u string) ([]feedSpeaker, error) {
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var out []feedSpeaker
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("not a speaker list")
	}
	return out, nil
}

// readSource reads one feed into the pool (lock held by the caller only
// around the pool writes).
func (s *Server) readSource(src *Source, now time.Time) error {
	u := s.feedURL(src.ID)
	if u == "" {
		return fmt.Errorf("no FEED line in the recipe")
	}
	list, err := s.feed(u)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	src.Read, src.Speakers, src.Err = now, len(list), ""
	for _, f := range list {
		name := strings.TrimSpace(f.FullName)
		key := normName(name)
		if key == "" {
			continue
		}
		p := s.pool[key]
		if p == nil {
			p = &PoolSpeaker{Name: name}
			s.pool[key] = p
		}
		if f.TagLine != "" {
			p.Tagline = clip(f.TagLine, 160)
		}
		if f.Bio != "" {
			p.Bio = clip(f.Bio, 500)
		}
		if strings.HasPrefix(f.ProfilePicture, "https://") {
			p.Photo = f.ProfilePicture
		}
		for _, l := range f.Links {
			if !strings.HasPrefix(l.URL, "https://") && !strings.HasPrefix(l.URL, "http://") {
				continue
			}
			if strings.EqualFold(l.LinkType, "LinkedIn") {
				p.LinkedIn = l.URL
			} else if !contains(p.Links, l.URL) && len(p.Links) < 5 {
				p.Links = append(p.Links, l.URL)
			}
		}
		for _, ss := range f.Sessions {
			t := Talk{Title: clip(ss.Name, 140), Event: src.Event, Date: src.Date, URL: src.URL}
			dup := false
			for _, o := range p.Talks {
				dup = dup || (o.Title == t.Title && o.Event == t.Event)
			}
			if !dup && t.Title != "" {
				p.Talks = append(p.Talks, t)
			}
		}
		sort.SliceStable(p.Talks, func(i, j int) bool { return p.Talks[i].Date > p.Talks[j].Date })
		if len(p.Talks) > poolTalksMax {
			p.Talks = p.Talks[:poolTalksMax]
		}
	}
	return nil
}

// readSources reads every feed not read in the last 20 hours. Called
// without the lock.
func (s *Server) readSources(force bool) {
	s.mu.Lock()
	now := s.now()
	var todo []*Source
	for _, src := range s.sources {
		if force || now.Sub(src.Read) > feedEvery {
			todo = append(todo, src)
		}
	}
	s.mu.Unlock()
	for _, src := range todo {
		if err := s.readSource(src, now); err != nil {
			s.mu.Lock()
			src.Err = clip(err.Error(), 120)
			s.mu.Unlock()
		}
		time.Sleep(s.pause)
	}
	s.mu.Lock()
	s.saveSources()
	s.mu.Unlock()
}

func (s *Server) saveSources() {
	writeJSON(filepath.Join(s.dir, "sources.json"), s.sources)
	writeJSON(filepath.Join(s.dir, "pool.json"), s.pool)
}

// handleSources: POST /api/sources {"sources": [Source...]}: Claude hands in
// feeds it found. Each is read at once; the answer says which were taken.
func (s *Server) handleSources(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Sources []Source `json:"sources"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		http.Error(w, "Bad JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	type result struct {
		ID       string `json:"id"`
		Taken    bool   `json:"taken"`
		Speakers int    `json:"speakers,omitempty"`
		Why      string `json:"why,omitempty"`
	}
	var out []result
	for i, src := range in.Sources {
		if i >= 40 {
			break
		}
		id := strings.ToLower(strings.TrimSpace(src.ID))
		if !feedID.MatchString(id) || !realConference(src.Event) {
			out = append(out, result{ID: id, Why: "needs an id and the conference's real name and year"})
			continue
		}
		s.mu.Lock()
		known := s.findSource(id)
		s.mu.Unlock()
		if known != nil {
			out = append(out, result{ID: id, Taken: true, Speakers: known.Speakers, Why: "already known"})
			continue
		}
		ns := &Source{ID: id, Event: clip(src.Event, 100), City: clip(src.City, 60), Country: clip(src.Country, 60),
			Date: clip(src.Date, 10), Added: s.now()}
		if strings.HasPrefix(src.URL, "https://") {
			ns.URL = clip(src.URL, 300)
		}
		if err := s.readSource(ns, s.now()); err != nil {
			out = append(out, result{ID: id, Why: clip(err.Error(), 80)})
			continue
		}
		s.mu.Lock()
		s.sources = append(s.sources, ns)
		s.note("feed", "New speaker feed: %s (%d speakers)", ns.Event, ns.Speakers)
		s.saveSources()
		s.mu.Unlock()
		out = append(out, result{ID: id, Taken: true, Speakers: ns.Speakers})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"results": out})
}

func (s *Server) findSource(id string) *Source {
	for _, src := range s.sources {
		if src.ID == id {
			return src
		}
	}
	return nil
}

// candidates: pool speakers for a job, minus anyone on the board, on a
// lineup or marked, the ones whose words match the topic first.
func (s *Server) candidates(t Topic, now time.Time) []map[string]any {
	ex := s.exclusions()
	board := s.onBoard("")
	words := topicWords(t)
	type scored struct {
		p *PoolSpeaker
		n int
	}
	var list []scored
	for k, p := range s.pool {
		if board[k] || ex.hit(t.ID, p.Name, now) {
			continue
		}
		text := strings.ToLower(p.Tagline + " " + p.Bio)
		for _, tk := range p.Talks {
			text += " " + strings.ToLower(tk.Title)
		}
		n := 0
		for _, w := range words {
			if strings.Contains(text, w) {
				n++
			}
		}
		if n > 0 {
			list = append(list, scored{p, n})
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].n != list[j].n {
			return list[i].n > list[j].n
		}
		return list[i].p.Name < list[j].p.Name
	})
	var out []map[string]any
	for i, c := range list {
		if i >= candidatesMax {
			break
		}
		out = append(out, map[string]any{"name": c.p.Name, "tagline": c.p.Tagline, "bio": c.p.Bio, "photo": c.p.Photo,
			"linkedin_url": c.p.LinkedIn, "links": c.p.Links, "talks": c.p.Talks})
	}
	return out
}

var themeWords = map[string][]string{
	"sre":      {"sre", "site reliability", "reliability", "incident", "on-call", "observability", "chaos", "resilien", "postmortem", "slo", "monitoring", "devops", "kubernetes", "production"},
	"llm":      {"llm", "large language", "genai", "generative", "gen ai", "agent", "rag", "prompt", "machine learning", "ai engineer", "mlops", "openai", "claude", "inference", "eval"},
	"platform": {"platform engineering", "platform team", "internal developer", "developer platform", "idp", "backstage", "kubernetes", "devops", "developer experience", "devex", "cloud native", "golden path", "infrastructure"},
}

func topicWords(t Topic) []string {
	if w, ok := themeWords[t.ID]; ok {
		return w
	}
	var out []string
	for _, f := range strings.Fields(strings.ToLower(t.Name)) {
		if len(f) > 2 && f != "and" && f != "the" {
			out = append(out, f)
		}
	}
	return out
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// handleCandidates: GET /api/candidates?topic=<id>, after new feeds came in.
func (s *Server) handleCandidates(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.topic(r.FormValue("topic"))
	if !ok {
		http.Error(w, "No such topic", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"topic_id": t.ID, "candidates": s.candidates(t, s.now())})
}
