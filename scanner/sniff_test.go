package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sreday/badger/pkg"
)

func TestSniffMatchName(t *testing.T) {
	ix := NewGuestIndex([]Guest{
		{ID: "gst-1", FirstName: "Peter", LastName: "Smith", Name: "Peter Smith", Company: "Acme", Title: "SRE"},
		{ID: "gst-2", FirstName: "Peter", LastName: "Jones", Name: "Peter Jones"},
		{ID: "gst-3", FirstName: "Ada", LastName: "Lovelace", Name: "Ada Lovelace", Company: "Analytical", LinkedIn: "https://www.linkedin.com/in/ada"},
		{ID: "gst-4", FirstName: "José", LastName: "Álvarez", Name: "José Álvarez"},
		{ID: "gst-5", FirstName: "Łukasz", LastName: "Nowak", Name: "Łukasz Nowak"},
		{ID: "gst-6", Name: "Mary Jane Watson"}, // no first/last split from Luma
		{ID: "gst-7", FirstName: "Grace", LastName: "Hopper", Name: "Grace Hopper", Company: "Navy"},
	})
	speakers := []Who{{Name: "Grace Hopper", Company: "Navy", Title: "Speaker"}, {Name: "Linus Torvalds", Company: "Linux", Title: "Speaker"}}
	cases := []struct{ typed, want string }{
		{"Ada", "gst-3"},
		{"  ada  ", "gst-3"},
		{"Peter", ""}, // two Peters: nobody
		{"Peter Smith", "gst-1"},
		{"peter s", "gst-1"},
		{"Peter J", "gst-2"},
		{"Peter X", ""},
		{"Jose", "gst-4"},
		{"jose alvarez", "gst-4"},
		{"Lukasz", "gst-5"},
		{"Mary", "gst-6"},
		{"Mary Watson", "gst-6"},
		{"Grace", "gst-7"}, // a speaker who registered is one person, the guest
		{"Linus", "speaker:Linus Torvalds"},
		{"Nobody", ""},
		{"", ""},
	}
	for _, c := range cases {
		w := matchName(c.typed, ix, speakers)
		got := ""
		if w != nil {
			got = w.Guest
			if got == "" {
				got = "speaker:" + w.Name
			}
		}
		if got != c.want {
			t.Errorf("matchName(%q) = %q, want %q", c.typed, got, c.want)
		}
	}
	if w := matchName("Ada", ix, nil); w == nil || w.Company != "Analytical" || w.LinkedIn == "" {
		t.Errorf("Ada's details = %+v", w)
	}
}

func TestSniffParseLiveSchedule(t *testing.T) {
	page := `<html><script id="live-data" type="application/json">{"event":"SREday NYC","days":[[{"room":"main room","items":[
	{"start":"2026-11-07T08:30:00-05:00","end":"2026-11-07T09:30:00-05:00","kind":"break","title":"Doors open"},
	{"start":"2026-11-07T09:30:00-05:00","end":"2026-11-07T10:00:00-05:00","kind":"keynote","title":"Scout <b>models<\/b>","who":"Afshin R","org":"Datadog","url":"../Afshin_R_Datadog_Scout"},
	{"start":"2026-11-07T10:00:00-05:00","end":"2026-11-07T10:30:00-05:00","kind":"talk","title":"Two people","who":"Ada L & Grace H","org":"Acme","url":"../Ada_L_Acme_Two_people.html"}]}]]}</script></html>`
	items, err := parseLiveSchedule([]byte(page))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	if items[0].Key != "Afshin_R_Datadog_Scout" || items[0].Kind != "keynote" || items[0].Room != "main room" || items[0].Title != "Scout <b>models</b>" {
		t.Errorf("first = %+v", items[0])
	}
	if items[1].Key != "Ada_L_Acme_Two_people" || items[1].Start.Hour() != 10 {
		t.Errorf("second = %+v", items[1])
	}
	if sp := speakersOf(items[1].Who, items[1].Org); len(sp) != 2 || sp[1].Name != "Grace H" {
		t.Errorf("speakers = %+v", sp)
	}
	csv := "YouTube,status,name,track,day,organization,title\n,keynote,Ada L,1,1,Acme,\"Hello, world\"\n,draft,X,1,1,Y,Z\n,talk,_Lunch,1,1,,\n,talk,Bo B,2,1,Co,Long\n,talk,Bo B,2,1,Co,Long\n"
	got := parseTalksSchedule([]byte(csv))
	if len(got) != 2 || got[0].Key != "Ada_L_Acme_Hello__world" || got[0].Kind != "keynote" {
		t.Errorf("talks.csv schedule = %+v", got)
	}
	if v := metaValue("brand_domain: \"sreday.com\"\nbase_path: \"https://www.sreday.com/\" # site\n", "base_path"); v != "https://www.sreday.com/" {
		t.Errorf("metaValue = %q", v)
	}
}

// sniffTest is a server with one event and its schedule, on a clock tests move.
type sniffTest struct {
	t     *testing.T
	srv   *Server
	ts    *httptest.Server
	clock time.Time
	start time.Time
	ss    SniffSession
}

func newSniffTest(t *testing.T) *sniffTest {
	api, raw := githubAPIURL, githubRawURL
	githubAPIURL, githubRawURL = "http://127.0.0.1:1", "http://127.0.0.1:1"
	t.Cleanup(func() { githubAPIURL, githubRawURL = api, raw })
	luma := fakeLuma(t)
	t.Cleanup(luma.Close)
	lumaBaseURL = luma.URL

	st := &sniffTest{t: t}
	srv, err := newServer(Config{DataDir: t.TempDir(), LumaAPIKey: "test-key", AdminPassword: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	srv.now = func() time.Time { return st.clock }
	st.srv = srv
	st.ts = httptest.NewServer(srv.routes())
	t.Cleanup(st.ts.Close)
	st.start = time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	ev := LumaEvent{APIId: "evt-1", Name: "SREday Test", Timezone: "Europe/Amsterdam",
		StartAt: st.start.Format(time.RFC3339), EndAt: st.start.Add(8 * time.Hour).Format(time.RFC3339)}
	if _, err := srv.sniff.SyncSchedule(ev, []SchedItem{
		{Key: "Grace_Hopper_Navy_Bugs", Title: "Bugs", Who: "Grace Hopper", Org: "Navy", Kind: "talk", Room: "Main", Start: st.start.Add(time.Hour), End: st.start.Add(90 * time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	st.ss = srv.sniff.Sessions("evt-1")[0]
	st.clock = st.start.AddDate(0, 0, -7) // a week early: test mode
	return st
}

func (st *sniffTest) post(path string, body any) (int, string) {
	st.t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(st.ts.URL+path, "application/json", bytes.NewReader(b))
	if err != nil {
		st.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

func (st *sniffTest) ask(id, text, name string, anon bool, rl string) int {
	st.t.Helper()
	code, _ := st.post("/q/"+st.ss.Code+"/ask", map[string]any{"id": id, "text": text, "name": name, "anon": anon, "secret": "sec-" + id, "rl": rl})
	return code
}

type feedOut struct {
	V     string
	Phase string
	Qs    []struct {
		ID, T, N string
		V        int
		A, X     bool
		Who      *Who
	}
	Rs []struct {
		S   int
		C   string
		N   string
		Who *Who
	}
	Mine []string
}

func (st *sniffTest) feed(path string) (int, feedOut) {
	st.t.Helper()
	resp, err := http.Get(st.ts.URL + path)
	if err != nil {
		st.t.Fatal(err)
	}
	defer resp.Body.Close()
	var f feedOut
	if resp.StatusCode == 200 {
		if err := json.NewDecoder(resp.Body).Decode(&f); err != nil {
			st.t.Fatal(err)
		}
	}
	return resp.StatusCode, f
}

func (st *sniffTest) file() string {
	data, _ := os.ReadFile(filepath.Join(st.srv.cfg.DataDir, "events", "evt-1", "sniff.jsonl"))
	return string(data)
}

func TestSniffLifecycle(t *testing.T) {
	st := newSniffTest(t)
	code := st.ss.Code
	if len(code) != 4 || strings.ContainsAny(code, "01OILSZB5826G") {
		t.Fatalf("code = %q", code)
	}

	// Test mode: posts work, stay in memory, show as TEST.
	if c := st.ask("q-test-0001", "Testing 1 2 3", "", false, "rl-aaaaaaaa"); c != 200 {
		t.Fatalf("test ask: %d", c)
	}
	if _, f := st.feed("/q/" + code + "/feed"); len(f.Qs) != 1 || !f.Qs[0].X || f.Phase != PhaseTest {
		t.Fatalf("test feed = %+v", f)
	}
	if st.file() != "" {
		t.Fatalf("test post on disk: %s", st.file())
	}

	// Live: the test post is gone.
	st.clock = st.start.Add(30 * time.Minute)
	if _, f := st.feed("/q/" + code + "/feed"); len(f.Qs) != 0 || f.Phase != PhaseLive {
		t.Fatalf("live feed still has test posts: %+v", f)
	}

	// A named question (the one Ada) and an anonymous one.
	if c := st.ask("q-ada-00001", "How do you debug?", "Ada", false, "rl-aaaaaaaa"); c != 200 {
		t.Fatalf("ask: %d", c)
	}
	st.ask("q-ada-00001", "How do you debug?", "Ada", false, "rl-aaaaaaaa") // a resend
	if c := st.ask("q-anon-0001", "Secret question", "Grace Hopper", true, "rl-bbbbbbbb"); c != 200 {
		t.Fatalf("anon ask: %d", c)
	}
	_, f := st.feed("/q/" + code + "/feed")
	if len(f.Qs) != 2 {
		t.Fatalf("questions = %+v", f.Qs)
	}
	for _, q := range f.Qs {
		if q.Who != nil {
			t.Errorf("the audience sees who: %+v", q)
		}
		if q.ID == "q-ada-00001" && q.N != "Ada" {
			t.Errorf("audience name = %q", q.N)
		}
		if q.ID == "q-anon-0001" && q.N != "" {
			t.Errorf("anonymous shows a name: %q", q.N)
		}
	}
	_, sf := st.feed("/sniff/" + st.ss.Token + "/feed")
	for _, q := range sf.Qs {
		if q.ID == "q-ada-00001" && (q.Who == nil || q.Who.Name != "Ada Lovelace" || q.Who.Company != "Analytical") {
			t.Errorf("speaker sees %+v", q)
		}
		if q.ID == "q-anon-0001" && (q.Who != nil || q.N != "") {
			t.Errorf("anonymous to the speaker: %+v", q)
		}
	}
	// On disk, the anonymous question has no name, guest or key at all.
	for _, line := range strings.Split(strings.TrimSpace(st.file()), "\n") {
		if strings.Contains(line, "q-anon-0001") && (strings.Contains(line, "Grace") || strings.Contains(line, `"who"`) || strings.Contains(line, `"k"`) || strings.Contains(line, "rl-")) {
			t.Errorf("anonymous record carries something: %s", line)
		}
		if strings.Contains(line, "rl-") || strings.Contains(line, "127.0.0.1") || strings.Contains(line, "sec-") {
			t.Errorf("rate-limit key, IP or delete secret on disk: %s", line)
		}
	}

	// Votes: once per phone, and taken back.
	for i := 0; i < 2; i++ {
		if c, b := st.post("/q/"+code+"/vote", map[string]any{"q": "q-ada-00001", "k": "voter-111111", "on": true, "rl": "rl-cccccccc"}); c != 200 {
			t.Fatalf("vote: %d %s", c, b)
		}
	}
	st.post("/q/"+code+"/vote", map[string]any{"q": "q-ada-00001", "k": "voter-222222", "on": true, "rl": "rl-dddddddd"})
	st.post("/q/"+code+"/vote", map[string]any{"q": "q-ada-00001", "k": "voter-222222", "on": false, "rl": "rl-dddddddd"})
	_, f = st.feed("/q/" + code + "/feed?k=voter-111111")
	for _, q := range f.Qs {
		if q.ID == "q-ada-00001" && q.V != 1 {
			t.Errorf("votes = %d, want 1", q.V)
		}
	}
	if len(f.Mine) != 1 || f.Mine[0] != "q-ada-00001" {
		t.Errorf("mine = %v", f.Mine)
	}

	// Nothing new: 204, no body.
	if c, _ := st.feed("/q/" + code + "/feed?v=" + f.V); c != http.StatusNoContent {
		t.Errorf("unchanged feed: %d", c)
	}

	// Ratings: one per phone, the latest counts.
	st.post("/q/"+code+"/rate", map[string]any{"k": "rate-key-01", "stars": 1, "text": "ok", "name": "Ada", "rl": "rl-eeeeeeee"})
	st.post("/q/"+code+"/rate", map[string]any{"k": "rate-key-01", "stars": 3, "text": "great", "name": "Ada", "rl": "rl-eeeeeeee"})
	st.post("/q/"+code+"/rate", map[string]any{"k": "rate-key-02", "stars": 2, "anon": true, "name": "Ada", "rl": "rl-ffffffff"})
	if c, _ := st.post("/q/"+code+"/rate", map[string]any{"k": "rate-key-03", "stars": 4, "rl": "rl-ffffffff"}); c != 400 {
		t.Errorf("4 stars accepted: %d", c)
	}
	_, sf = st.feed("/sniff/" + st.ss.Token + "/feed")
	if len(sf.Rs) != 2 {
		t.Fatalf("ratings = %+v", sf.Rs)
	}
	for _, r := range sf.Rs {
		if r.S == 3 && (r.C != "great" || r.Who == nil) || r.S == 2 && (r.Who != nil || r.N != "") || r.S == 1 {
			t.Errorf("rating %+v", r)
		}
	}

	// The speaker marks answered and hides; hidden is off the room's list.
	if c, _ := st.post("/sniff/"+st.ss.Token+"/mod", map[string]any{"q": "q-ada-00001", "mod": "answered"}); c != 200 {
		t.Fatal("mod")
	}
	st.post("/sniff/"+st.ss.Token+"/mod", map[string]any{"q": "q-anon-0001", "mod": "hide"})
	_, f = st.feed("/q/" + code + "/feed")
	if len(f.Qs) != 1 || !f.Qs[0].A {
		t.Errorf("after mod: %+v", f.Qs)
	}

	// Only the asking phone deletes; then the text is gone from the file too.
	st.ask("q-del-00001", "Oops, wrong talk", "", false, "rl-gggggggg")
	if c, _ := st.post("/q/"+code+"/del", map[string]any{"q": "q-del-00001", "secret": "guess", "rl": "rl-gggggggg"}); c != 403 {
		t.Errorf("delete with a wrong secret: %d", c)
	}
	if c, _ := st.post("/q/"+code+"/del", map[string]any{"q": "q-del-00001", "secret": "sec-q-del-00001", "rl": "rl-gggggggg"}); c != 200 {
		t.Errorf("delete: %d", c)
	}
	if strings.Contains(st.file(), "Oops") {
		t.Error("deleted question still in the file")
	}

	// Everything survives a restart.
	re, err := OpenSniffStore(st.srv.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	v := re.View(code, st.clock)
	if len(v.Questions) != 2 || len(v.Ratings) != 2 {
		t.Fatalf("after reload: %d questions, %d ratings", len(v.Questions), len(v.Ratings))
	}
	for _, q := range v.Questions {
		if q.ID == "q-ada-00001" && (q.Votes != 1 || !q.Answered || q.Who == nil) {
			t.Errorf("reloaded %+v", q)
		}
	}

	// A new speaker link: the old one stops working.
	old := st.ss.Token
	if _, err := st.srv.sniff.ResetToken(code); err != nil {
		t.Fatal(err)
	}
	if resp, _ := http.Get(st.ts.URL + "/sniff/" + old); resp.StatusCode != 404 {
		t.Errorf("old speaker link: %d", resp.StatusCode)
	}

	// Closed, plus a day for feedback on the way home, then no more posts.
	st.clock = st.start.Add(8*time.Hour + 6*time.Hour + 2*time.Hour)
	if c := st.ask("q-late-0001", "Late one", "", false, "rl-hhhhhhhh"); c != 200 {
		t.Errorf("ask the evening after: %d", c)
	}
	st.clock = st.start.Add(8*time.Hour + 6*time.Hour + 25*time.Hour)
	if c := st.ask("q-late-0002", "Too late", "", false, "rl-hhhhhhhh"); c != http.StatusGone {
		t.Errorf("ask two days later: %d", c)
	}
}

func TestSniffRateLimits(t *testing.T) {
	st := newSniffTest(t)
	st.clock = st.start.Add(time.Hour)
	// One phone: four questions a minute.
	for i := 0; i < 5; i++ {
		c := st.ask(fmt.Sprintf("q-one-%05d", i), "q", "", false, "rl-onephone")
		if want := 200; i == 4 {
			if c != http.StatusTooManyRequests {
				t.Errorf("5th question in a minute: %d", c)
			}
		} else if c != want {
			t.Errorf("question %d: %d", i, c)
		}
	}
	// A hundred phones behind the venue's one IP: all fine.
	for i := 0; i < 100; i++ {
		if c := st.ask(fmt.Sprintf("q-many-%05d", i), "q", "", false, fmt.Sprintf("rl-phone-%04d", i)); c != 200 {
			t.Fatalf("phone %d behind one IP: %d", i, c)
		}
	}
}

func TestSniffSyncKeepsCodes(t *testing.T) {
	st, err := OpenSniffStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ev := LumaEvent{APIId: "evt-9", Name: "SREday X"}
	st.SyncSchedule(ev, []SchedItem{{Key: "Ada_Acme_Old_title", Title: "Old title", Who: "Ada Lovelace"}, {Key: "Bo_Co_Talk", Title: "Talk", Who: "Bo"}})
	before := map[string]string{}
	for _, s := range st.Sessions("evt-9") {
		before[s.Who] = s.Code
	}
	// Ada's title changed (new key), Bo dropped off the schedule.
	st.SyncSchedule(ev, []SchedItem{{Key: "Ada_Acme_New_title", Title: "New title", Who: "Ada Lovelace"}})
	list := st.Sessions("evt-9")
	if len(list) != 2 {
		t.Fatalf("sessions = %+v", list)
	}
	for _, s := range list {
		if s.Code != before[s.Who] {
			t.Errorf("%s's code changed: %s -> %s", s.Who, before[s.Who], s.Code)
		}
		if s.Who == "Ada Lovelace" && (s.Title != "New title" || s.Gone) || s.Who == "Bo" && !s.Gone {
			t.Errorf("session %+v", s)
		}
	}
}

func TestSniffPagesAndWeight(t *testing.T) {
	st := newSniffTest(t)
	st.clock = st.start.Add(time.Hour)
	st.ask("q-page-0001", "<script>alert(1)</script>", "Ada", false, "rl-pagepage")
	resp, err := http.Get(st.ts.URL + "/q/" + strings.ToLower(st.ss.Code))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(body)
	if resp.StatusCode != 200 || !strings.Contains(page, "Bugs") || !strings.Contains(page, "Grace Hopper") {
		t.Fatalf("audience page: %d", resp.StatusCode)
	}
	if strings.Contains(page, "<script>alert(1)") {
		t.Error("question text not escaped")
	}
	if strings.Contains(page, "Lovelace") || strings.Contains(page, "Analytical") {
		t.Error("the audience page shows a matched surname or company")
	}
	if resp.Header.Get("X-Robots-Tag") == "" || resp.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Error("audience page is not private")
	}
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	w.Write(body)
	w.Close()
	if gz.Len() > 12<<10 {
		t.Errorf("audience page is %d bytes gzipped, budget 12 KB", gz.Len())
	}
	t.Logf("audience page: %d bytes, %d gzipped", len(body), gz.Len())

	// A plain form (no JavaScript) posts and comes back to the page.
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := noRedirect.PostForm(st.ts.URL+"/q/"+st.ss.Code+"/ask", map[string][]string{"text": {"From a form"}, "name": {"Peter"}})
	if err != nil || r.StatusCode != http.StatusSeeOther || !strings.Contains(r.Header.Get("Location"), "sent=1") {
		t.Fatalf("form post: %v %v", r, err)
	}

	for _, p := range []string{"", "/qr.png", "/slide.png", "/export.csv"} {
		resp, err := http.Get(st.ts.URL + "/sniff/" + st.ss.Token + p)
		if err != nil || resp.StatusCode != 200 {
			t.Errorf("speaker %q: %v %v", p, resp.StatusCode, err)
		}
		if p == "/export.csv" {
			b, _ := io.ReadAll(resp.Body)
			if !strings.Contains(string(b), "Ada Lovelace") || !strings.Contains(string(b), "From a form") {
				t.Errorf("export = %s", b)
			}
		}
		resp.Body.Close()
	}
	if resp, _ := http.Get(st.ts.URL + "/q/ZZZZ"); resp.StatusCode != 404 {
		t.Errorf("unknown code: %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("GET", st.ts.URL+"/admin/sniff/evt-1", nil)
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != 401 {
		t.Errorf("admin without password: %d", resp.StatusCode)
	}
	req.SetBasicAuth("admin", "pw")
	resp, err = http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("admin: %v %v", resp, err)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "/q/"+st.ss.Code) {
		t.Error("admin page lacks the session")
	}
}

func TestSniffSlide(t *testing.T) {
	img, err := pkg.GenerateSniffSlide(pkg.SniffSlide{Brand: "llmday", Title: "A rather long talk title that has to wrap over a couple of lines without being cut", Speaker: "Ada Lovelace & Grace Hopper", URL: "https://scan.sreday.com/q/K7RM", Short: "scan.sreday.com/q/K7RM"})
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 1920 || b.Dy() != 1080 {
		t.Errorf("slide is %v", b)
	}
}

func TestSniffEventLinkAndPIN(t *testing.T) {
	st := newSniffTest(t)
	if len(st.ss.PIN) != 4 {
		t.Fatalf("PIN = %q", st.ss.PIN)
	}
	ec, err := st.srv.sniff.EventCode("evt-1")
	if err != nil || ec == st.ss.Code || len(ec) != 4 {
		t.Fatalf("event code %q, %v", ec, err)
	}
	if again, _ := st.srv.sniff.EventCode("evt-1"); again != ec {
		t.Errorf("event code changed: %q then %q", ec, again)
	}
	// The event page: names and faces, never the talk titles.
	resp, err := http.Get(st.ts.URL + "/q/" + strings.ToLower(ec))
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(page), "SREday Test - Questions and Answers") ||
		!strings.Contains(string(page), "Grace Hopper") || strings.Contains(string(page), "Bugs") ||
		!strings.Contains(string(page), "/q/"+st.ss.Code+"/face.jpg") {
		t.Errorf("event page %d:\n%s", resp.StatusCode, page)
	}
	// The talk page links back; the speaker's QR now opens the event page.
	resp, _ = http.Get(st.ts.URL + "/q/" + st.ss.Code)
	page, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(page), `href="/q/`+ec+`"`) || !strings.Contains(string(page), "I'm the speaker") {
		t.Errorf("talk page has no way back or no PIN box")
	}
	r := httptest.NewRequest("GET", "/", nil)
	if u := st.srv.audienceURL(r, st.ss); !strings.HasSuffix(u, "/q/"+ec) {
		t.Errorf("QR opens %q", u)
	}

	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	pin := func(p string) string {
		resp, err := noFollow.PostForm(st.ts.URL+"/q/"+st.ss.Code+"/pin", map[string][]string{"pin": {p}})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.Header.Get("Location")
	}
	wrong := "0000"
	if st.ss.PIN == wrong {
		wrong = "1111"
	}
	if loc := pin(wrong); !strings.Contains(loc, "pin=") {
		t.Errorf("wrong PIN went to %q", loc)
	}
	if loc := pin(st.ss.PIN); loc != "/sniff/"+st.ss.Token {
		t.Errorf("right PIN went to %q", loc)
	}
	for i := 0; i < 10; i++ {
		pin(wrong)
	}
	if loc := pin(st.ss.PIN); loc == "/sniff/"+st.ss.Token {
		t.Errorf("PIN still open after a guessing spree")
	}
}

func TestSniffSquareFace(t *testing.T) {
	// A grey head sticking out of the top of a purple disc: the picture
	// comes back square, starting at the head, with the disc's colour in
	// the corners where the circle used to end.
	purple := color.NRGBA{0x71, 0x36, 0x60, 255}
	disc := image.NewNRGBA(image.Rect(0, 0, 100, 100))
	shot := image.NewNRGBA(image.Rect(0, 0, 100, 120))
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			if (x-50)*(x-50)+(y-50)*(y-50) <= 50*50 {
				disc.SetNRGBA(x, y, purple)
				shot.SetNRGBA(x, y+20, purple)
			}
		}
	}
	for y := 0; y < 60; y++ {
		for x := 35; x < 65; x++ {
			shot.SetNRGBA(x, y, color.NRGBA{90, 90, 90, 255})
		}
	}
	out := squareFace(shot, disc)
	if b := out.Bounds(); b.Dx() != 88 || b.Dy() != 88 {
		t.Fatalf("size %v, want an 88x88 square", b)
	}
	if c := out.At(44, 1).(color.NRGBA); c != (color.NRGBA{90, 90, 90, 255}) {
		t.Errorf("top of the head = %v", c)
	}
	for _, p := range []image.Point{{0, 0}, {87, 87}, {0, 87}} {
		if c := out.At(p.X, p.Y).(color.NRGBA); c != purple {
			t.Errorf("corner %v = %v, want the disc's colour", p, c)
		}
	}
}
