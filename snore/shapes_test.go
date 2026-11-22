package snore

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeWeb serves fixed bodies by address; anything else is a 404.
func fakeWeb(s *Server, pages map[string]string) *[]string {
	var hits []string
	s.raw = func(u string, _ http.Header) (int, []byte, error) {
		hits = append(hits, u)
		if b, ok := pages[u]; ok {
			return http.StatusOK, []byte(b), nil
		}
		return http.StatusNotFound, nil, nil
	}
	return &hits
}

func setRecipe(t *testing.T, s *Server, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.dir, "recipe.txt"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRobotsRules(t *testing.T) {
	r := parseRobots("User-agent: other\nDisallow: /\n\nUser-agent: *\nDisallow: /api/\nAllow: /api/open\nDisallow: /*?*\nCrawl-delay: 10\n")
	for p, want := range map[string]bool{"/": true, "/api/x": false, "/api/open/1": true, "/page?x=1": false, "/schedule/export/schedule.json": true} {
		if r.allowed(p) != want {
			t.Errorf("allowed(%q) = %v", p, !want)
		}
	}
	if r.delay != 10*time.Second {
		t.Errorf("delay = %v", r.delay)
	}
	s, _ := testServer(t)
	hits := fakeWeb(s, map[string]string{"https://a.example.org/robots.txt": "User-agent: *\nDisallow: /private/\n", "https://a.example.org/private/x": "secret"})
	if _, err := s.get("https://a.example.org/private/x", nil, true); err != errDisallowed {
		t.Fatalf("a disallowed path was read: %v", err)
	}
	for _, h := range *hits {
		if strings.Contains(h, "/private/") {
			t.Fatal("the disallowed page was fetched")
		}
	}
}

func readOne(t *testing.T, s *Server, src *Source) {
	t.Helper()
	if err := s.readSource(src, s.now()); err != nil {
		t.Fatalf("read %s: %v", src.Kind, err)
	}
}

func TestShapes(t *testing.T) {
	s, _ := testServer(t)
	setRecipe(t, s, strings.Join([]string{
		"FEED profiles https://p.example.org/%s/speakers",
		"FEED schedule-json https://j.example.org/%s/schedule.json",
		"FEED schedule-xml https://x.example.org/%s/schedule.xml",
		"FEED program-html https://h.example.org/%s/program",
		`PATTERN program-html <div class="s"><b>(?P<name>[^<]+)</b>, <i>(?P<company>[^<]+)</i></div>\s*<h3>(?P<title>[^<]+)</h3>`,
	}, "\n"))
	fakeWeb(s, map[string]string{
		"https://p.example.org/conf1/speakers":      `[{"firstName":"Pia","lastName":"Profile","company":"Example Cloud","bio":"Runs SRE.","imageUrl":"https://img.example.org/p.jpg","linkedInUsername":"piaprofile","proposals":[{"title":"Kubernetes at night"}]}]`,
		"https://j.example.org/conf2/schedule.json": `{"schedule":{"conference":{"days":[{"rooms":{"Main":[{"title":"Observability for all","persons":[{"public_name":"Jo Json","biography":"SRE lead"}]}]}}]}}}`,
		"https://x.example.org/conf3/schedule.xml":  `<schedule><day><room><event><title>Chaos in prod</title><persons><person id="1">Xia Xml</person><person id="2">Yan Xml</person></persons></event></room></day></schedule>`,
		"https://h.example.org/conf4/program":       `<div class="s"><b>Hal Html</b>, <i>Example Bank</i></div> <h3>Incident review &amp; you</h3>`,
	})
	readOne(t, s, &Source{Kind: kindProfiles, ID: "conf1", Event: "ProfConf 2026", Date: "2026-03-01"})
	readOne(t, s, &Source{Kind: kindSchedJSON, ID: "conf2", Event: "JsonConf 2025", Date: "2025-02-01"})
	readOne(t, s, &Source{Kind: kindSchedXML, ID: "conf3", Event: "XmlConf 2026", Date: "2026-02-01"})
	readOne(t, s, &Source{Kind: kindProgram, ID: "conf4", Event: "HtmlConf 2025", Date: "2025-10-01"})
	p := s.pool["pia profile"]
	if p == nil || p.Company != "Example Cloud" || p.LinkedIn != "https://www.linkedin.com/in/piaprofile/" || p.Talks[0].Event != "ProfConf 2026" {
		t.Fatalf("profiles: %+v", p)
	}
	if j := s.pool["jo json"]; j == nil || j.Bio != "SRE lead" || j.Talks[0].Title != "Observability for all" {
		t.Fatalf("schedule json: %+v", j)
	}
	if s.pool["xia xml"] == nil || s.pool["yan xml"] == nil {
		t.Fatal("schedule xml: both persons expected")
	}
	if h := s.pool["hal html"]; h == nil || h.Company != "Example Bank" || h.Talks[0].Title != "Incident review & you" {
		t.Fatalf("program html: %+v", s.pool["hal html"])
	}
}

func TestVideoAndRepoKinds(t *testing.T) {
	s, _ := testServer(t)
	s.cfg.VideoKey, s.cfg.RepoToken = "k", "t"
	setRecipe(t, s, strings.Join([]string{
		"FEED video https://v.example.org/channels?id=%s&key={key}",
		"FEED video-items https://v.example.org/items?list={id}&key={key}",
		"PATH video-list items.*.lists.uploads",
		"PATH video-items items.*.snippet",
		"PATH video-title title",
		"PATH video-text description",
		"PATH video-date publishedAt",
		"PATH video-id rid.vid",
		"LINK video https://v.example.org/watch/{id}",
		`PATTERN video (?m)^(?P<title>[^|\n]+) \| (?P<name>[A-Z][a-z]+ [A-Z][a-z]+)$`,
		"FEED repo-data https://r.example.org/search?q={q}",
		"FEED repo-license https://r.example.org/repos/{repo}/license",
		"FEED repo-raw https://raw.example.org/{repo}/HEAD/{path}",
		"PATH repo-items items.*",
		"PATH repo-name repository.full_name",
		"PATH repo-path path",
		"PATH repo-license-id license.spdx_id",
	}, "\n"))
	fakeWeb(s, map[string]string{
		"https://v.example.org/channels?id=CH1&key=k":              `{"items":[{"lists":{"uploads":"UP1"}}]}`,
		"https://v.example.org/items?list=UP1&key=k":               `{"items":[{"snippet":{"title":"SLOs that work | Vic Video","description":"","publishedAt":"2026-05-01T10:00:00Z","rid":{"vid":"abc"}}}]}`,
		"https://r.example.org/search?q=filename%3Aspeakers.yml":   `{"items":[{"repository":{"full_name":"open/conf"},"path":"data/speakers.yml"},{"repository":{"full_name":"closed/conf"},"path":"speakers.yml"}]}`,
		"https://r.example.org/repos/open/conf/license":            `{"license":{"spdx_id":"MIT"}}`,
		"https://r.example.org/repos/closed/conf/license":          `{"license":{"spdx_id":"NOASSERTION"}}`,
		"https://raw.example.org/open/conf/HEAD/data/speakers.yml": "- name: Ola Open\n  company: Example Co\n  linkedin: olaopen\n- name: Solo\n",
		"https://raw.example.org/closed/conf/HEAD/speakers.yml":    "- name: Cy Closed\n",
	})
	readOne(t, s, &Source{Kind: kindVideo, ID: "CH1", Event: "VidConf"})
	v := s.pool["vic video"]
	if v == nil || v.Talks[0].URL != "https://v.example.org/watch/abc" || v.Talks[0].Date != "2026-05-01" || v.Expires.IsZero() {
		t.Fatalf("video: %+v", v)
	}
	readOne(t, s, &Source{Kind: kindRepo, ID: "filename:speakers.yml", Event: "Open data"})
	if o := s.pool["ola open"]; o == nil || o.Company != "Example Co" || o.LinkedIn != "https://www.linkedin.com/in/olaopen/" {
		t.Fatalf("repo: %+v", s.pool["ola open"])
	}
	if s.pool["cy closed"] != nil {
		t.Fatal("a repo without an open license was read")
	}
	// The video entry goes after 30 days unless read again.
	later := s.now().Add(31 * 24 * time.Hour)
	s.now = func() time.Time { return later }
	s.readSources(false)
	if s.pool["vic video"] != nil {
		t.Fatal("a 30-day-old video entry should be gone")
	}
	if s.pool["ola open"] == nil {
		t.Fatal("other entries stay")
	}
}

func TestDiscoverFromDirectory(t *testing.T) {
	s, _ := testServer(t)
	setRecipe(t, s, strings.Join([]string{
		"FEED profiles https://%s.cfp.example.org/speakers",
		"DIRECTORY https://dir.example.org/all.json name=name link=hyperlink date=date city=location",
		`HOST ^https://([a-z0-9]+)\.cfp\.example\.org profiles $1`,
		"SOURCE schedule-xml fixedconf 2026-02-01 Brussels Fixed Conf 2026",
	}, "\n"))
	fakeWeb(s, map[string]string{
		"https://dir.example.org/all.json": `[{"name":"Cloud Days","hyperlink":"https://cloud26.cfp.example.org/","date":[1780000000000],"location":"Berlin"},
			{"name":"Old Days","hyperlink":"https://old19.cfp.example.org/","date":[1550000000000]},
			{"name":"Elsewhere","hyperlink":"https://other.example.org/","date":[1780000000000]}]`,
		"https://cloud26.cfp.example.org/speakers": `[{"fullName":"Dee Discover","company":"Example AI"}]`,
	})
	s.readSources(true)
	kindsIDs := map[string]bool{}
	for _, src := range s.sources {
		kindsIDs[src.kind()+":"+src.ID] = true
	}
	if !kindsIDs["profiles:cloud26"] || kindsIDs["profiles:old19"] || !kindsIDs["schedule-xml:fixedconf"] || len(s.sources) != 2 {
		t.Fatalf("sources = %v", kindsIDs)
	}
	if d := s.pool["dee discover"]; d == nil || d.Company != "Example AI" {
		t.Fatalf("discovered speaker: %+v", d)
	}
	lg := ""
	for _, e := range s.act {
		lg += e.Text + "\n"
	}
	if !strings.Contains(lg, "New speaker source: Cloud Days 2026 (profiles, 1 speakers)") {
		t.Fatalf("log = %s", lg)
	}
}
