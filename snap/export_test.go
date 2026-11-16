package snap

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sreday/badger/luma"
)

func TestExportParse(t *testing.T) {
	for name, want := range map[string]string{
		"SREday San Francisco 2026 Q4":                            "sreday|san francisco|2026",
		"LLMday + Prompt Engineering Conference - London 2026 Q4": "llmday,pec|london|2026",
		"PLATFORMday London":                                      "platformday|london|",
		"SRE Day New York":                                        "sreday|nyc|",
		"Team dinner":                                             "|team dinner|",
	} {
		b, c, y := exParse(name)
		if got := strings.Join(b, ",") + "|" + c + "|" + y; got != want {
			t.Errorf("%q: %s, want %s", name, got, want)
		}
	}
}

// testRecipe is a recipe for the tests: its words and numbers are made up
// here and say nothing about the real one, which is not in this repo.
const testRecipe = `{
	"instructions": "\nTEST RECIPE: clean the lists and mark them.",
	"ask": "List for {sponsor} about {subject} ({thread}).",
	"drafter": "PAGE for {sponsor}: {link}\n",
	"notes": {"ready": "List ready for {sponsor}: {titles} titles, {companies} companies, {score} of 100.", "short": "No list for {sponsor}: {score} under {bar}.", "few": "No list: {people} people."},
	"marks": {"count": "good", "drop": "spare", "hide": "never"},
	"bar": 50, "people": 10, "rounds": 2, "thin": 5, "wide": 3, "back_months": 24, "ahead_months": 24,
	"wider": ["city", "brand"],
	"drop_titles": "(?i)^pupil$",
	"drop_companies": "(?i)^none$"
}`

// people makes n registrations with one title and company.
func people(n int, title, company string) []exPerson {
	out := make([]exPerson, n)
	for i := range out {
		out[i] = exPerson{title: title, company: company}
	}
	return out
}

func TestExportDecide(t *testing.T) {
	titles := map[string]string{"sre": "Site Reliability Engineer", "dev": "Software Engineer", "ae": "Account Executive", "vendor sre": "Site Reliability Engineer"}
	comps := map[string]string{"bank": "Bank", "shop": "Shop", "rival": "Rival"}
	rc, err := parseRecipe([]byte(testRecipe))
	if err != nil {
		t.Fatal(err)
	}
	marks := map[string][]string{"good": {"site reliability engineer"}, "spare": {"Account Executive"}, "never": {"Rival"}}

	// 8 of 20 count (40, the bar is 50): four of the six spare people go, and it is 8 of 16.
	var ps []exPerson
	ps = append(ps, people(8, "sre", "bank")...)
	ps = append(ps, people(6, "dev", "shop")...)
	ps = append(ps, people(6, "ae", "shop")...)
	ps = append(ps, people(5, "vendor sre", "rival")...) // never in
	res := decide(rc, ps, titles, comps, marks)
	if !res.ok || res.leftOut != 4 || res.score < rc.Bar || res.people != 16 {
		t.Fatalf("trimmed: %+v", res)
	}
	if strings.Join(res.companies, ",") != "Bank,Shop" || strings.Join(res.titles, ",") != "Account Executive,Site Reliability Engineer,Software Engineer" {
		t.Fatalf("lists: %v %v", res.titles, res.companies)
	}
	// Over the bar already: nobody is left out.
	if res := decide(rc, append(people(10, "sre", "bank"), people(8, "ae", "shop")...), titles, comps, marks); !res.ok || res.leftOut != 0 || res.score != 55 {
		t.Fatalf("over the bar: %+v", res)
	}
	// Even without every spare person it stays under the bar: not ok.
	res = decide(rc, append(append(people(4, "sre", "bank"), people(14, "dev", "shop")...), people(2, "ae", "shop")...), titles, comps, marks)
	if res.ok || res.leftOut != 2 || res.score != 22 {
		t.Fatalf("under the bar: %+v", res)
	}
}

func TestExportFlow(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	fired := 0
	fire := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fired++
		w.Write([]byte(`{"type":"routine_fire"}`))
	}))
	defer fire.Close()
	s, err := New(Config{Base: "/snap", DataDir: t.TempDir(), AdminPassword: "pw", Account: "mark@sreday.com", Every: time.Hour, TeamDomains: team, TZ: "Europe/London",
		LumaAPIKey: "luma", PublicURL: "https://scan.example"})
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now }
	s.fireBase = fire.URL + "/%s"
	if s.CanExport() {
		t.Fatal("lists are on without a recipe")
	}
	if err := os.WriteFile(filepath.Join(s.dir, recipeFile), []byte(testRecipe), 0o600); err != nil {
		t.Fatal(err)
	}
	s.listEvents = func(string) ([]luma.Event, error) {
		return []luma.Event{{APIId: "evt-1", Name: "SREday London 2026 Q3", StartAt: "2026-09-18T09:00:00Z", EndAt: "2026-09-18T18:00:00Z"},
			{APIId: "evt-2", Name: "Team dinner", StartAt: "2026-09-19T19:00:00Z"}}, nil
	}
	s.getGuests = func(id, _ string) ([]luma.Guest, error) {
		var g []luma.Guest
		add := func(n int, title, company string) {
			for i := 0; i < n; i++ {
				g = append(g, luma.Guest{ID: fmt.Sprintf("gst-%d", len(g)), Email: fmt.Sprintf("p%d@example.com", len(g)), Title: title, Company: company})
			}
		}
		add(12, "Sr. SRE ", "Bank PLC")
		add(8, "Developer", "shop ltd")
		add(6, "AE", "Rival Inc")
		add(2, "pupil", "Uni")
		add(1, "call me on +44 20 7946 0000", "jane@example.com")
		return g, nil
	}
	s.store.Update("acme", func(th *Thread) {
		*th = Thread{ID: "acme", Subject: "Acme x SREday", HistoryID: "1", ClaudeHistory: "1", LastAt: now.AddDate(0, 0, -5), LastFrom: "Jane", Company: "Acme", Category: "sponsors", Loose: true, AnalyzedBy: "claude", AnalyzedHistory: "1", Label: "Acme asks who attends",
			People: []Person{{Name: "Jane", Email: "jane@acme.com"}}, Actions: []Action{{Title: "Send Acme the anonymized list", Kind: "export", Event: "SREday London"}}}
	})
	h := s.Handler()
	bearer := "Bearer " + s.token
	writeJSON(s.dir, "qq.json", qqConfig{Token: "sk-ant-oat01-qqqqqqqqqqqqqqqqqqqq"})

	if rec := call(t, h, "POST", "/admin/thread/acme/export", "pw", "a=0&draft=0"); rec.Code >= 400 {
		t.Fatalf("export: %d %s", rec.Code, rec.Body)
	}
	for i := 0; i < 200 && fired == 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if fired != 1 {
		t.Fatal("the routine was not started")
	}
	var queue struct {
		Instructions string   `json:"instructions"`
		Questions    []qaItem `json:"questions"`
	}
	json.Unmarshal(call(t, h, "GET", "/api/qa", bearer, "").Body.Bytes(), &queue)
	if len(queue.Questions) != 1 || queue.Questions[0].Mode != "export" || !strings.Contains(queue.Instructions, "TEST RECIPE") {
		t.Fatalf("queue: %+v", queue.Questions)
	}
	ex := queue.Questions[0].Export
	if strings.Join(ex.Titles, "|") != "ae|developer|sr. sre" || strings.Join(ex.Companies, "|") != "bank plc|rival inc|shop ltd|uni" {
		t.Fatalf("asked: %v %v", ex.Titles, ex.Companies)
	}
	body := call(t, h, "GET", "/api/qa", bearer, "").Body.String()
	if strings.Contains(body, "example.com") || strings.Contains(body, "7946") {
		t.Fatal("personal data reached Claude")
	}
	id := queue.Questions[0].ID
	// A batch, then the last one with the marks. An invented key and an address as a value never get in.
	post := func(v string) {
		if rec := call(t, h, "POST", "/api/qa", bearer, v); rec.Code != 200 {
			t.Fatalf("answer: %d %s", rec.Code, rec.Body)
		}
	}
	post(`{"answers":[{"id":"` + id + `","titles":{"sr. sre":"Senior Site Reliability Engineer","developer":"Software Engineer","made up":"Chief Invention Officer"},"companies":{"bank plc":"Bank","uni":""}}]}`)
	if th, _ := s.store.Get("acme"); th.Export.Status != "asking" {
		t.Fatalf("after a batch: %+v", th.Export)
	}
	post(`{"answers":[{"id":"` + id + `","answer":"Done.","titles":{"ae":"Account Executive"},"companies":{"shop ltd":"mail jane@shop.com","rival inc":"Rival"},"good":["Senior Site Reliability Engineer"],"spare":["Account Executive"],"never":["Rival"]}]}`)
	th, _ := s.store.Get("acme")
	if th.Export.Status != "done" {
		t.Fatalf("job: %+v", th.Export)
	}
	l, ok := s.threadExport(th)
	if !ok || len(l.Secret) != secretLen || l.Fit != 60 || strings.Join(l.Titles, "|") != "Senior Site Reliability Engineer|Software Engineer" || strings.Join(l.Companies, "|") != "Bank" {
		t.Fatalf("link: %+v", l)
	}
	if line := s.exportLine(th); line != "PAGE for Acme: https://scan.example/snap/"+l.Secret+"\n" {
		t.Fatalf("drafter line: %q", line)
	}
	// The page, without a login; the downloads; the menu.
	page := call(t, h, "GET", "/"+l.Secret, "", "")
	if page.Code != 200 || !strings.Contains(page.Body.String(), "<li>Bank</li>") || !strings.Contains(page.Body.String(), "Who attends SREday London 2026 Q3") || strings.Contains(page.Body.String(), "Acme") {
		t.Fatalf("page: %d %s", page.Code, page.Body)
	}
	if page.Header().Get("X-Robots-Tag") == "" {
		t.Fatal("the page is not marked private")
	}
	if csv := call(t, h, "GET", "/"+l.Secret+"?dl=titles", "", "").Body.String(); !strings.Contains(csv, "Job title\nSenior Site Reliability Engineer\nSoftware Engineer\n") {
		t.Fatalf("csv: %q", csv)
	}
	if rec := call(t, h, "GET", "/"+l.Secret+"?dl=both", "", ""); rec.Header().Get("Content-Type") != "application/zip" {
		t.Fatal("both: not a zip")
	}
	if rec := call(t, h, "GET", "/abcdefghjk", "", ""); rec.Code != 404 {
		t.Fatalf("a wrong secret: %d", rec.Code)
	}
	if body := call(t, h, "GET", "/?view=sponsors&t=acme", "pw", "").Body.String(); !strings.Contains(body, "List ready for Acme: 2 titles, 1 companies, 60 of 100.") || !strings.Contains(body, "Kill now") {
		t.Fatalf("the card or the Exports menu doesn't show the list: note=%v kill=%v acme=%v", strings.Contains(body, "List ready"), strings.Contains(body, "Kill now"), strings.Contains(body, "Acme x SREday"))
	}
	// Killed: the page says so and the lists are gone from disk.
	call(t, h, "POST", "/admin/export/"+l.ID+"/kill", "pw", "")
	if rec := call(t, h, "GET", "/"+l.Secret, "", ""); rec.Code != http.StatusGone || strings.Contains(rec.Body.String(), "Bank") {
		t.Fatalf("killed page: %d", rec.Code)
	}
	if len(s.exports[0].Titles) != 0 {
		t.Fatal("a killed link kept its lists")
	}
}

// The first lists' links had 60 characters and no /snap in front. One that
// Snap still knows is sent on to the page; anything else stays a 404.
func TestExportOldLink(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	s := newTestServer(t, now)
	old := strings.Repeat("a2b3c4d5e6", 6)
	s.mu.Lock()
	s.exports = append(s.exports,
		ExportLink{ID: "x1", Secret: old, Sponsor: "Acme", Created: now, Expires: now.Add(exportLife), Titles: []string{"SRE"}, Companies: []string{"Example"}},
		ExportLink{ID: "x2", Secret: "abcdefghjk", Sponsor: "Acme", Created: now, Expires: now.Add(exportLife)})
	s.mu.Unlock()
	mux := http.NewServeMux()
	mux.Handle("/snap/", http.StripPrefix("/snap", s.Handler()))
	mux.HandleFunc("GET /{secret}", s.OldLink)
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec
	}
	rec := get("/" + old + "?dl=titles")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/snap/"+old+"?dl=titles" {
		t.Fatalf("old link: %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := get("/snap/" + old); rec.Code != http.StatusOK {
		t.Fatalf("the page behind the old link: %d", rec.Code)
	}
	for _, p := range []string{"/abcdefghjk", "/" + strings.Repeat("z", 60), "/" + strings.Repeat("a", 59)} {
		if rec := get(p); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", p, rec.Code)
		}
	}
}
