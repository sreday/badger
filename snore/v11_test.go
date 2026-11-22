package snore

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPickPhoto(t *testing.T) {
	page := `<html><head><meta property="og:image" content="https://cdn.example.org/image/head-400.jpg" /></head>
<body><img src="/logo.svg" alt="Example"><img src="https://cdn.example.org/image/banner.jpg">
<img src="https://cdn.example.org/image/head-400.jpg" alt="Jane  O'Doe" /></body></html>`
	if got := pickPhoto(page, "https://talks.example.org/s/other/123", "Jane O'Doe"); got != "https://cdn.example.org/image/head-400.jpg" {
		t.Fatalf("by alt: %q", got)
	}
	noAlt := `<meta property="og:image" content="/img/jane.png"><img src="/x.png">`
	if got := pickPhoto(noAlt, "https://conf.example.org/speakers/jane-odoe", "Jane O'Doe"); got != "https://conf.example.org/img/jane.png" {
		t.Fatalf("og on their page: %q", got)
	}
	if got := pickPhoto(noAlt, "https://conf.example.org/schedule", "Jane O'Doe"); got != "" {
		t.Fatalf("og on a page not about them: %q", got)
	}
	c := &Card{Key: "jane odoe", Links: []string{"https://conf.example.org/schedule", "https://www.linkedin.com/in/x", "https://talks.example.org/jane-odoe"},
		Talks: []Talk{{URL: "https://a.example.org/1"}, {URL: "https://b.example.org/2"}}}
	if got := strings.Join(photoPagesFor(c), " "); got != "https://talks.example.org/jane-odoe https://conf.example.org/schedule https://a.example.org/1" {
		t.Fatalf("pages = %s", got)
	}
}

func TestFindPhotosFromPage(t *testing.T) {
	s, h := testServer(t)
	s.page = func(u string) (string, error) {
		if u == "https://talks.example.org/kim-face" {
			return `<img src="https://cdn.example.org/kim.jpg" alt="Kim Face">`, nil
		}
		return "", fmt.Errorf("no")
	}
	jobs := queue(t, s, h)
	c := card("Kim Face", "London", 80, "London")
	delete(c, "photo")
	c["sources"] = []string{"https://talks.example.org/kim-face"}
	answer(t, s, h, jobs[0]["id"].(string), []map[string]any{c})
	s.bg.Wait() // the background photo run
	s.mu.Lock()
	defer s.mu.Unlock()
	if cs := s.st.Cards["sre"]; len(cs) != 1 || !photoFile.MatchString(cs[0].PhotoBox) {
		t.Fatalf("photo from page: %+v", cs)
	}
}

func TestHumans(t *testing.T) {
	s, h := testServer(t)
	s.cfg.HumansDB = "postgres://ro@example.org/db"
	s.readHumans = func(context.Context, string) (map[string][]humanRow, error) {
		return map[string][]humanRow{
			"cy keep":   {{slug: "cy-keep-1", status: "connected"}},
			"twin name": {{slug: "twin-a", status: "saved"}, {slug: "twin-b", status: "pending"}},
		}, nil
	}
	jobs := queue(t, s, h)
	answer(t, s, h, jobs[0]["id"].(string), []map[string]any{card("Cy  KEEP", "London", 80, "London"), card("Twin Name", "London", 70, "London"), card("New Face", "London", 60, "London")})
	w := do(t, h, "GET", "/?t=sre", "", admin)
	if !strings.Contains(w.Body.String(), "Humans app status: <b>unavailable</b>") {
		t.Fatalf("before the first read the pill should say unavailable")
	}
	s.bg.Wait()
	s.syncHumans()
	got := map[string]Humans{}
	for _, c := range s.st.Cards["sre"] {
		got[c.Name] = c.Humans
	}
	if h := got["Cy  KEEP"]; h.Status != "connected" || h.Profile != "https://www.linkedin.com/in/cy-keep-1/" {
		t.Fatalf("exact match: %+v", h)
	}
	if h := got["Twin Name"]; h.Status != "2 people with this name" || h.Profile != "" {
		t.Fatalf("twins: %+v", h)
	}
	if h := got["New Face"]; h.Status != "None" {
		t.Fatalf("no match: %+v", h)
	}
	body := do(t, h, "GET", "/?t=sre", "", admin).Body.String()
	for _, want := range []string{`Humans app status: <b>connected</b>`, `Humans app status: <b>None</b>`, `href="https://www.linkedin.com/in/cy-keep-1/"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("board lacks %s", want)
		}
	}
	// The DB goes away: cards keep the last word.
	s.readHumans = func(context.Context, string) (map[string][]humanRow, error) { return nil, fmt.Errorf("down") }
	s.syncHumans()
	if s.st.Cards["sre"][0].Humans.Status == "" || s.humansErr == "" {
		t.Fatalf("an outage wiped the statuses")
	}
}

func TestBoardSectionsAndTabs(t *testing.T) {
	s, h := testServer(t)
	jobs := queue(t, s, h)
	for _, j := range jobs {
		if j["topic"] == "SRE" {
			answer(t, s, h, j["id"].(string), []map[string]any{card("Old Town", "Amsterdam", 95, "Amsterdam"), card("Soon Town", "London", 60, "London")})
		}
	}
	body := do(t, h, "GET", "/?t=sre", "", admin).Body.String()
	lon, ams := strings.Index(body, "<h2>London</h2>"), strings.Index(body, "<h2>Amsterdam</h2>")
	if lon < 0 || ams < 0 || lon > ams {
		t.Fatalf("London (coming) should come before Amsterdam (past): %d %d", lon, ams)
	}
	for _, gone := range []string{`class="ring"`, "Routine trig", "API trigger token", "<select", "v2."} {
		if strings.Contains(body, gone) {
			t.Fatalf("board still shows %q", gone)
		}
	}
	if !strings.Contains(body, `href="?t=conf42"`) || strings.Contains(body, "Site Reliability Engineering</a>") {
		t.Fatalf("Conf42 should be one tab, its topics inside it")
	}
	if !strings.Contains(body, `href="?t=sre&c=London">London</a>`) {
		t.Fatalf("no London pill")
	}
	body = do(t, h, "GET", "/?t=sre&c=London", "", admin).Body.String()
	if !strings.Contains(body, "<h2>London</h2>") || strings.Contains(body, "<h2>Amsterdam</h2>") {
		t.Fatalf("the London pill should show London only")
	}
	body = do(t, h, "GET", "/?t=conf42", "", admin).Body.String()
	if !strings.Contains(body, "Site Reliability Engineering</a>") || strings.Contains(body, "Web 3.0") {
		t.Fatalf("Conf42 tab: upcoming topics only")
	}
	_ = time.Now
}

func TestCardTalksShowConferenceAndYear(t *testing.T) {
	got := cardTalks([]Talk{
		{Title: "A Talk Title", Date: "2026-05-01", URL: "https://example.org/1"},                    // no conference: left out
		{Title: "Other", Event: "Other", Date: "2026-04-01"},                                         // event is the title: left out
		{Title: "Third", Event: "SomeConf Europe", Date: "2026-03-01", URL: "https://example.org/3"}, // year added
		{Title: "Fourth", Event: "BigConf 2025", Date: "2025-10-01", URL: "https://example.org/4"},   // year kept
		{Title: "Fifth", Event: "SomeConf Europe", Date: "2026-02-01", URL: "https://example.org/5"}, // same conference: once
		{Title: "Sixth", Event: "A Podcast", URL: "https://example.org/6"},                           // no year anywhere: left out
	})
	var names []string
	for _, x := range got {
		names = append(names, x.Event)
	}
	if strings.Join(names, "|") != "SomeConf Europe 2026|BigConf 2025" {
		t.Fatalf("conferences = %v", names)
	}
}
