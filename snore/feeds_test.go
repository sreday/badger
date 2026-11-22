package snore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFeedsAndCandidates(t *testing.T) {
	s, h := testServer(t)
	os.WriteFile(filepath.Join(s.dir, "recipe.txt"), []byte("FEED https://feeds.example.org/%s/speakers\n"), 0o600)
	got := ""
	s.feed = func(u string) ([]feedSpeaker, error) {
		got = u
		var out []feedSpeaker
		json.Unmarshal([]byte(`[
 {"fullName": "Pat Pager", "tagLine": "Staff SRE @ Example", "bio": "Pat works on incident response in London.", "profilePicture": "https://img.example.org/pat.jpg",
  "sessions": [{"name": "Pagers at scale"}], "links": [{"url": "https://www.linkedin.com/in/pat-pager/", "linkType": "LinkedIn"}, {"url": "https://pat.example.org", "linkType": "Blog"}]},
 {"fullName": "Ada Speaker", "tagLine": "SRE", "sessions": [{"name": "Reliability"}]},
 {"fullName": "Cook Book", "tagLine": "Chef", "sessions": [{"name": "Soup"}]}]`), &out)
		return out, nil
	}
	w := do(t, h, "POST", "/api/sources", `{"sources": [{"id": "abc123de", "event": "SomeConf London 2026", "city": "London", "country": "UK", "date": "2026-06-01", "url": "https://someconf.example.org"}, {"id": "BAD!", "event": "x"}]}`, bearer(s))
	if got != "https://feeds.example.org/abc123de/speakers" || !strings.Contains(w.Body.String(), `"taken":true,"speakers":3`) || !strings.Contains(w.Body.String(), `"needs an id`) {
		t.Fatalf("sources: %s (fetched %q)", w.Body, got)
	}
	s.mu.Lock()
	tp, _ := s.topic("sre")
	cs := s.candidates(tp, s.now())
	s.mu.Unlock()
	// Ada is on a lineup, Cook doesn't match the topic.
	if len(cs) != 1 || cs[0]["name"] != "Pat Pager" || cs[0]["linkedin_url"] != "https://www.linkedin.com/in/pat-pager/" {
		t.Fatalf("candidates = %+v", cs)
	}
	talks := cs[0]["talks"].([]Talk)
	if talks[0].Event != "SomeConf London 2026" || talks[0].URL != "https://someconf.example.org" {
		t.Fatalf("talks = %+v", talks)
	}
	jobs := queue(t, s, h)
	if jobs[0]["candidates"] == nil || jobs[0]["topic_id"] != "sre" {
		t.Fatalf("job lacks candidates: %+v", jobs[0])
	}
	if w := do(t, h, "GET", "/api/candidates?topic=sre", "", bearer(s)); !strings.Contains(w.Body.String(), "Pat Pager") {
		t.Fatalf("candidates endpoint: %s", w.Body)
	}
	// Pat on the board: no longer a candidate.
	answer(t, s, h, jobs[0]["id"].(string), []map[string]any{card("Pat Pager", "London", 80, "London")})
	s.mu.Lock()
	n := len(s.candidates(tp, s.now()))
	s.mu.Unlock()
	if n != 0 {
		t.Fatalf("a speaker on the board is still a candidate")
	}
}

func TestDailyJobsForTabsWithRoom(t *testing.T) {
	s, _ := testServer(t)
	mon := time.Date(2026, 11, 23, 5, 10, 0, 0, time.UTC)
	full := make([]*Card, MaxPerTopic)
	for i := range full {
		full[i] = &Card{Key: string(rune('a' + i))}
	}
	s.st.Cards["sre"] = full
	s.mu.Lock()
	s.dueJobs(mon.Add(-time.Hour)) // 04:10: nothing yet
	n0 := len(s.jobs)
	s.dueJobs(mon)
	s.dueJobs(mon.Add(time.Hour)) // once a day
	s.mu.Unlock()
	var topics []string
	for _, j := range s.jobs {
		topics = append(topics, j.Topic)
	}
	if n0 != 0 || strings.Contains(strings.Join(topics, ","), "sre") || len(topics) != 3 {
		t.Fatalf("Monday jobs = %v", topics)
	}
}
