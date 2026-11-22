package snore

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// testServer: Saturday 21 Nov 2026, 06:00 UTC. Footprint: SREday in two
// cities (one past, one coming), a Conf42 event, and lineups with a known
// speaker on each.
func testServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	s, err := New(Config{Base: "/snore", DataDir: t.TempDir(), AdminUser: "admin", AdminPassword: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 21, 6, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	s.fetch = func(string) ([]byte, string, error) { return []byte("\x89PNG\r\n\x1a\n"), "image/png", nil }
	s.pause = 0
	s.status = func(u string) int {
		if strings.Contains(u, "/gone") {
			return http.StatusNotFound
		}
		return http.StatusOK
	}
	s.fp.F = map[string]*Folder{
		"x/sreday/past/2025-amsterdam-q2": {Brand: "sreday", Name: "Amsterdam", Start: time.Date(2025, 5, 1, 9, 0, 0, 0, time.UTC), Names: []string{"Ada Speaker"}},
		"x/sreday/2026-london-q4":         {Brand: "sreday", Name: "London", Start: time.Date(2026, 12, 3, 9, 0, 0, 0, time.UTC)},
		"x/llmday/2026-bengaluru-q4":      {Brand: "llmday", Name: "Bengaluru", Start: time.Date(2026, 12, 10, 9, 0, 0, 0, time.UTC)},
		"conf42/sre2026":                  {Brand: "conf42", Name: "Site Reliability Engineering (SRE)", Start: time.Date(2027, 4, 2, 0, 0, 0, 0, time.UTC), Names: []string{"Bo Online"}},
		"conf42/web3-2021":                {Brand: "conf42", Name: "Web 3.0", Start: time.Date(2021, 4, 2, 0, 0, 0, 0, time.UTC)},
	}
	t.Cleanup(s.bg.Wait)
	return s, s.Handler()
}

func do(t *testing.T, h http.Handler, method, path, body string, auth func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if strings.HasPrefix(body, "{") {
		req.Header.Set("Content-Type", "application/json")
	} else if body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if auth != nil {
		auth(req)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func bearer(s *Server) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+s.token) }
}

func admin(r *http.Request) { r.SetBasicAuth("admin", "pw") }

func TestTopics(t *testing.T) {
	s, _ := testServer(t)
	ts := s.fp.Topics(s.now())
	var ids []string
	for _, tp := range ts {
		ids = append(ids, tp.ID)
	}
	if got := strings.Join(ids, ","); got != "sre,llm,platform,conf42-site-reliability-engineering" {
		t.Fatalf("topics = %s", got)
	}
	if len(ts[0].Cities) != 2 || ts[0].Cities[0].Name != "Amsterdam" || ts[0].Cities[1].Next != "2026-12-03" {
		t.Fatalf("SRE cities = %+v", ts[0].Cities)
	}
}

func TestAuth(t *testing.T) {
	_, h := testServer(t)
	if w := do(t, h, "GET", "/api/queue", "", nil); w.Code != 401 {
		t.Fatalf("queue without token: %d", w.Code)
	}
	if w := do(t, h, "GET", "/", "", nil); w.Code != 401 {
		t.Fatalf("board without login: %d", w.Code)
	}
	if w := do(t, h, "GET", "/", "", admin); w.Code != 200 {
		t.Fatalf("board: %d %s", w.Code, w.Body)
	}
}

func queue(t *testing.T, s *Server, h http.Handler) []map[string]any {
	t.Helper()
	w := do(t, h, "GET", "/api/queue", "", bearer(s))
	var q struct {
		Jobs      []map[string]any
		Remaining int
	}
	if err := json.Unmarshal(w.Body.Bytes(), &q); err != nil {
		t.Fatal(err, w.Body.String())
	}
	return q.Jobs
}

func card(name, city string, match int, cities ...string) map[string]any {
	return map[string]any{"name": name, "base": map[string]string{"city": city, "country": "UK", "sure": "stated"}, "our_cities": cities,
		"match": match, "photo": "https://example.org/" + strings.ReplaceAll(name, " ", "") + ".png", "reach": map[string]int{"linkedin": 5000}}
}

func answer(t *testing.T, s *Server, h http.Handler, id string, cards []map[string]any) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"model": "test", "answers": []map[string]any{{"id": id, "cards": cards}}})
	if w := do(t, h, "POST", "/api/answers", string(b), bearer(s)); w.Code != 200 {
		t.Fatalf("answers: %d %s", w.Code, w.Body)
	}
}

func TestSaturdayQueueAndMerge(t *testing.T) {
	s, h := testServer(t)
	jobs := queue(t, s, h)
	if len(jobs) != perQueue {
		t.Fatalf("got %d jobs, want %d", len(jobs), perQueue)
	}
	if again := queue(t, s, h); len(again) != 1 { // 4 topics: the 3 handed out are leased
		t.Fatalf("second queue: %d jobs", len(again))
	}
	var sre string
	for _, j := range jobs {
		if j["topic"] == "SRE" {
			sre = j["id"].(string)
		}
	}
	var cards []map[string]any
	for i := 0; i < 40; i++ {
		cards = append(cards, card(fmt.Sprintf("Local Person%02d", i), "London", 50+i, "London"))
	}
	cards = append(cards,
		card("Ada Speaker", "London", 99, "London"),                                      // on a lineup
		card("Far Away", "Paris", 98, "Paris"),                                           // not one of our cities
		map[string]any{"name": "No Base", "match": 97, "our_cities": []string{"London"}}, // base unknown
	)
	answer(t, s, h, sre, cards)
	got := s.st.Cards["sre"]
	if len(got) != MaxPerTopic {
		t.Fatalf("kept %d cards", len(got))
	}
	if got[0].Name != "Local Person39" || got[0].Reach.LinkedInSrc != "search" {
		t.Fatalf("first card = %+v", got[0])
	}
	for _, c := range got {
		if c.Name == "Ada Speaker" || c.Name == "Far Away" || c.Name == "No Base" {
			t.Fatalf("%s should be left out", c.Name)
		}
	}
	s.bg.Wait() // photos are copied in the background
	s.mu.Lock()
	box := s.st.Cards["sre"][0].PhotoBox
	s.mu.Unlock()
	if !photoFile.MatchString(box) {
		t.Fatalf("photo not kept: %q", box)
	}
}

func TestActionsAndLineup(t *testing.T) {
	s, h := testServer(t)
	jobs := queue(t, s, h)
	answer(t, s, h, jobs[0]["id"].(string), []map[string]any{card("Cy Keep", "London", 80, "London"), card("Di Drop", "London", 70, "London")})
	set := func(r *http.Request) { admin(r); r.Header.Set("X-Snore", "1") }
	do(t, h, "POST", "/admin/reject", url.Values{"key": {"Di Drop"}, "topic": {"sre"}}.Encode(), set)
	if len(s.st.Cards["sre"]) != 1 {
		t.Fatalf("reject left %d cards", len(s.st.Cards["sre"]))
	}
	// Next week: the rejected one stays out.
	do(t, h, "POST", "/admin/refresh", url.Values{"topic": {"sre"}}.Encode(), set)
	var id string
	for _, j := range s.jobs {
		if j.Topic == "sre" && j.Done.IsZero() {
			id = j.ID
		}
	}
	answer(t, s, h, id, []map[string]any{card("Cy Keep", "London", 80, "London"), card("Di Drop", "London", 90, "London")})
	if cs := s.st.Cards["sre"]; len(cs) != 1 {
		t.Fatalf("after refresh: %+v", cs)
	}
	// Cy joins a lineup: off the board at the next footprint read.
	s.fp.F["x/sreday/2026-london-q4"].Names = []string{"Cy Keep"}
	s.prune(s.now())
	if len(s.st.Cards["sre"]) != 0 || s.st.Graduated["cy keep"] == "" {
		t.Fatalf("lineup speaker still on the board")
	}
	w := do(t, h, "POST", "/api/check", `{"names": ["Cy Keep", "Ada Speaker", "Ada Middle Speaker", "New Face"]}`, bearer(s))
	if !strings.Contains(w.Body.String(), `"Cy Keep","Ada Speaker","Ada Middle Speaker"]`) {
		t.Fatalf("check = %s", w.Body)
	}
}

func TestParse(t *testing.T) {
	yml := "events:\n\n  - name: Large Language Models (LLMs)\n    date: 2026-03-05\n    short_url: llms2026\n    db_path: ./_db/LLM_2026.csv\n    sponsors:\n      gold:\n        - x\nsponsors:\n"
	ev := parseConf42(yml)
	if len(ev) != 1 || ev[0].Short != "llms2026" || ev[0].DB != "./_db/LLM_2026.csv" || conf42Topic(ev[0].Name) != "Large Language Models" {
		t.Fatalf("parseConf42 = %+v", ev)
	}
	c42 := talkNames([]byte("Featured,Track,Name1,JobTitle1,Company1,Name2\n,,Ann One,,,Bob Two\n,,Cat Three,,,\n"))
	brand := talkNames([]byte("name,status,title\nDan Four & Eve Five,confirmed,Talk\nGus Six,cancelled,Gone\n"))
	if strings.Join(c42, "|") != "Ann One|Bob Two|Cat Three" || strings.Join(brand, "|") != "Dan Four|Eve Five" {
		t.Fatalf("talkNames: %v / %v", c42, brand)
	}
}

// TestPreview writes the board with made-up cards to $SNORE_PREVIEW, for a
// screenshot before a deploy.
func TestPreview(t *testing.T) {
	out := os.Getenv("SNORE_PREVIEW")
	if out == "" {
		t.Skip("set SNORE_PREVIEW")
	}
	s, h := testServer(t)
	jobs := queue(t, s, h)
	names := []string{"Alex Example", "Sam Sample", "Robin Test", "Kim Demo", "Jo Placeholder", "Lee Mock"}
	var cards []map[string]any
	for i, n := range names {
		c := card(n, "London", 92-i*7, "London")
		delete(c, "photo")
		c["title"], c["company"] = "Staff SRE", "Example Corp"
		c["reach"] = map[string]any{"linkedin": 24000 / (i + 1), "talk_audiences": []map[string]any{{"event": "Some Conf", "attendees": 3000}}, "bluesky": 1200, "github": 800}
		c["parts"] = map[string]int{"reach": 90 - i*8, "topic": 85, "local": 100, "activity": 70 - i*5}
		c["topics"] = []string{"observability", "incidents"}
		c["talks"] = []map[string]string{{"title": "A talk about pagers", "event": "Some Conf", "date": "2026-06-01", "url": "https://example.org"}}
		cards = append(cards, c)
	}
	for _, j := range jobs {
		if j["topic"] == "SRE" {
			answer(t, s, h, j["id"].(string), cards)
		}
	}
	w := do(t, h, "GET", "/?t=sre", "", admin)
	os.WriteFile(out, w.Body.Bytes(), 0o644)
}

func TestRefreshFreesALeasedJob(t *testing.T) {
	s, h := testServer(t)
	if len(queue(t, s, h)) != perQueue {
		t.Fatal("first queue")
	}
	set := func(r *http.Request) { admin(r); r.Header.Set("X-Snore", "1") }
	do(t, h, "POST", "/admin/refresh", url.Values{"topic": {"sre"}}.Encode(), set)
	jobs := queue(t, s, h)
	found := false
	for _, j := range jobs {
		found = found || j["topic"] == "SRE"
	}
	if !found {
		t.Fatalf("SRE not handed out again: %v", jobs)
	}
}
