package snore

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
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

func TestRepeatedSpeakersAreSkipped(t *testing.T) {
	s, h := testServer(t)
	jobs := queue(t, s, h)
	ids := map[string]string{}
	for _, j := range jobs {
		ids[j["topic"].(string)] = j["id"].(string)
	}
	answer(t, s, h, ids["SRE"], []map[string]any{card("Rep Eat", "London", 70, "London")})
	// The same person for LLM: one person, one tab.
	answer(t, s, h, ids["LLM"], []map[string]any{card("Rep Eat", "Bengaluru", 90, "Bengaluru"), card("Fresh One", "Bengaluru", 60, "Bengaluru")})
	if cs := s.st.Cards["llm"]; len(cs) != 1 || cs[0].Name != "Fresh One" {
		t.Fatalf("llm = %+v", cs)
	}
	// SRE again: the card on the board stays as it was, the new one is added.
	set := func(r *http.Request) { admin(r); r.Header.Set("X-Snore", "1") }
	do(t, h, "POST", "/admin/refresh", url.Values{"topic": {"sre"}}.Encode(), set)
	var id string
	for _, j := range s.jobs {
		if j.Topic == "sre" && j.Done.IsZero() {
			id = j.ID
		}
	}
	answer(t, s, h, id, []map[string]any{card("Rep Eat", "London", 99, "London"), card("New Too", "London", 50, "London")})
	if cs := s.st.Cards["sre"]; len(cs) != 2 || cs[0].Name != "Rep Eat" || cs[0].Match != 70 {
		t.Fatalf("sre = %+v", cs)
	}
	w := do(t, h, "POST", "/api/check", `{"names": ["Rep Eat", "Someone Else"]}`, bearer(s))
	if !strings.Contains(w.Body.String(), `"excluded":["Rep Eat"]`) {
		t.Fatalf("check = %s", w.Body)
	}
	if body := do(t, h, "GET", "/?t=sre", "", admin).Body.String(); !strings.Contains(body, "Scout now") {
		t.Fatalf("no Scout now button")
	}
}

func TestCFPFrogMascotAndSpeakersLeave(t *testing.T) {
	s, h := testServer(t)
	s.cfg.CFPDB, s.cfg.HumansDB = "postgres://cfp@example.org/db", "postgres://h@example.org/db"
	s.readCFP = func(context.Context, string) (map[string]CFP, error) {
		return map[string]CFP{
			"frog fan":  {Event: "SREday Example 2027", Status: "submitted", URL: "https://cfp.ninja/dashboard/events/7/proposals"},
			"now taken": {Event: "SREday Example 2027", Status: "accepted", URL: "https://cfp.ninja/dashboard/events/7/proposals"},
		}, nil
	}
	s.readHumans = func(context.Context, string) (map[string][]humanRow, error) {
		return map[string][]humanRow{"on stage": {{slug: "on-stage", status: "connected", speaker: true}}}, nil
	}
	jobs := queue(t, s, h)
	nophoto := card("Frog Fan", "London", 80, "London")
	delete(nophoto, "photo")
	nophoto["title"], nophoto["company"] = "Staff SRE", "Example Corp"
	for _, c := range []map[string]any{nophoto} {
		c["talks"] = []map[string]string{{"title": "Pagers", "event": "SomeConf", "date": "2026-06-01", "url": "https://example.org/t"}}
	}
	answer(t, s, h, jobs[0]["id"].(string), []map[string]any{nophoto, card("Now Taken", "London", 70, "London"), card("On Stage", "London", 60, "London")})
	s.bg.Wait()
	s.syncHumans()
	if cs := s.st.Cards["sre"]; len(cs) != 1 || cs[0].Name != "Frog Fan" {
		t.Fatalf("speakers should leave: %+v", cs)
	}
	body := do(t, h, "GET", "/?t=sre", "", admin).Body.String()
	for _, want := range []string{`href="https://cfp.ninja/dashboard/events/7/proposals"`, `src="mascot/sreday.png?v=4"`, `<div class="job">Staff SRE</div>`, `<div class="co">Example Corp</div>`, `<ul class="confs">`} {
		if !strings.Contains(body, want) {
			t.Fatalf("board lacks %s", want)
		}
	}
	if strings.Contains(body, ">Invited<") {
		t.Fatalf("Invited button still there")
	}
	if w := do(t, h, "GET", "/mascot/frog.png", "", admin); w.Code != 200 || w.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("mascot: %d", w.Code)
	}
}

func TestDeadTalkLinksStayListedWithoutALink(t *testing.T) {
	s, h := testServer(t)
	s.mu.Lock()
	s.st.Cards["sre"] = []*Card{{Key: "link check", Name: "Link Check", Base: Base{City: "London"}, Cities: []string{"London"},
		Talks: []Talk{
			{Title: "One", Event: "LiveConf 2026", Date: "2026-05-01", URL: "https://example.org/live"},
			{Title: "Two", Event: "A Very Long Conference Name That Would Wrap Onto Two Rows 2025", Date: "2025-05-01", URL: "https://example.org/gone"},
		}}}
	s.mu.Unlock()
	s.checkLinks()
	tk := s.st.Cards["sre"][0].Talks
	if tk[0].Dead || !tk[1].Dead || tk[1].Checked.IsZero() {
		t.Fatalf("talks = %+v", tk)
	}
	body := do(t, h, "GET", "/?t=sre", "", admin).Body.String()
	if !strings.Contains(body, `href="https://example.org/live"`) || strings.Contains(body, `example.org/gone`) {
		t.Fatal("the live link should be a link and the dead one not")
	}
	if !strings.Contains(body, `<span class="dead" title="A Very Long Conference Name That Would Wrap Onto Two Rows 2025">`) {
		t.Fatal("the dead conference should still be listed")
	}
}

// The same country is within reach: a public work country is enough for an
// in-person tab, and a Conf42 speaker who moves there keeps what was known.
func TestSameCountryMovesInPerson(t *testing.T) {
	s, h := testServer(t)
	s.mu.Lock()
	s.st.Cards["conf42-sre"] = []*Card{{Key: normName("Country Only"), Name: "Country Only", Title: "Head of Things", Company: "Example Corp",
		Base: Base{Country: "UK", Sure: "stated"}, LinkedIn: "https://www.linkedin.com/in/example", Match: 80}}
	online := "conf42-sre"
	s.mu.Unlock()
	var sre string
	var listed bool
	for _, j := range queue(t, s, h) {
		if j["topic"] == "SRE" {
			sre = j["id"].(string)
			for _, c := range j["on_conf42"].([]any) {
				listed = listed || c.(map[string]any)["name"] == "Country Only"
			}
		}
	}
	if !listed {
		t.Fatalf("the in-person job does not list the Conf42 speaker")
	}
	answer(t, s, h, sre, []map[string]any{
		{"name": "Country Only", "base": map[string]string{"country": "UK", "sure": "stated"}, "our_cities": []string{"London"}},
		{"name": "No Where", "base": map[string]string{"sure": "stated"}, "our_cities": []string{"London"}},
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	got := s.st.Cards["sre"]
	if len(got) != 1 || got[0].Name != "Country Only" || got[0].Title != "Head of Things" || got[0].LinkedIn == "" || got[0].Match != 80 {
		t.Fatalf("sre cards = %+v", got)
	}
	if len(s.st.Cards[online]) != 0 {
		t.Fatalf("the speaker is still on the Conf42 tab")
	}
}

// An in-person job looks at the upcoming cities with the fewest speakers,
// and who spoke at a conference there leads the candidates.
func TestFocusCities(t *testing.T) {
	s, _ := testServer(t)
	s.mu.Lock()
	defer s.mu.Unlock()
	tp, _ := s.topic("sre")
	f := s.focus(tp)
	if len(f) == 0 || len(f) > focusMax || f[0].Next == "" {
		t.Fatalf("focus = %+v", f)
	}
	city := f[0].Name
	s.sources = append(s.sources, &Source{ID: "x1", Event: "Some Local Conf 2026", City: city})
	s.pool = map[string]*PoolSpeaker{
		normName("Zed Local"):  {Name: "Zed Local", Tagline: "SRE", Talks: []Talk{{Title: "Incident reviews", Event: "Some Local Conf 2026"}}},
		normName("Amy Remote"): {Name: "Amy Remote", Tagline: "SRE, observability, incident response", Talks: []Talk{{Title: "Reliability", Event: "Far Conf 2026"}}},
	}
	cs := s.candidates(tp, s.now(), s.nearFor(tp, "")...)
	if len(cs) != 2 || cs[0]["name"] != "Zed Local" || cs[0]["spoke_in"] != city {
		t.Fatalf("candidates = %+v", cs)
	}
	if d := s.jobData(&Job{ID: "j"}, tp); len(d["focus_cities"].([]map[string]any)) != len(f) {
		t.Fatalf("job focus = %+v", d["focus_cities"])
	}
}

// In person always wins: a card an online job marks for an in-person tab
// lands there.
func TestOnlineJobSendsLocalsInPerson(t *testing.T) {
	s, h := testServer(t)
	var online string
	for i := 0; i < 3 && online == ""; i++ {
		for _, j := range queue(t, s, h) {
			if j["online"] == true {
				online = j["id"].(string)
				if _, ok := j["in_person_cities"].(map[string]any)["sre"]; !ok {
					t.Fatalf("no in-person cities on the online job")
				}
			}
		}
	}
	local := card("Lou Local", "London", 80, "London")
	local["in_person"] = "sre"
	far := map[string]any{"name": "Fay Faraway", "base": map[string]string{"country": "New Zealand", "sure": "stated"}, "match": 70}
	answer(t, s, h, online, []map[string]any{local, far})
	s.mu.Lock()
	defer s.mu.Unlock()
	if got := s.st.Cards["sre"]; len(got) != 1 || got[0].Name != "Lou Local" || got[0].InPerson != "" {
		t.Fatalf("sre cards = %+v", got)
	}
	n := 0
	for id, cs := range s.st.Cards {
		if strings.HasPrefix(id, "conf42-") {
			for _, c := range cs {
				n++
				if c.Name != "Fay Faraway" {
					t.Fatalf("%s is on a Conf42 tab", c.Name)
				}
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d Conf42 cards", n)
	}
}
