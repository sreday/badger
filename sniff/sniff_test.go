package sniff

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sreday/badger/event"
	"github.com/sreday/badger/luma"
	"github.com/sreday/badger/pkg"
	"github.com/sreday/badger/sites"
)

func TestSniffMatchName(t *testing.T) {
	ix := &guestList{guests: []luma.Guest{
		{ID: "gst-1", FirstName: "Peter", LastName: "Smith", Name: "Peter Smith", Company: "Acme", Title: "SRE"},
		{ID: "gst-2", FirstName: "Peter", LastName: "Jones", Name: "Peter Jones"},
		{ID: "gst-3", FirstName: "Ada", LastName: "Lovelace", Name: "Ada Lovelace", Company: "Analytical", LinkedIn: "https://www.linkedin.com/in/ada"},
		{ID: "gst-4", FirstName: "José", LastName: "Álvarez", Name: "José Álvarez"},
		{ID: "gst-5", FirstName: "Łukasz", LastName: "Nowak", Name: "Łukasz Nowak"},
		{ID: "gst-6", Name: "Mary Jane Watson"}, // no first/last split from Luma
		{ID: "gst-7", FirstName: "Grace", LastName: "Hopper", Name: "Grace Hopper", Company: "Navy"},
	}}
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

// guestList is a fixed guest list.
type guestList struct{ guests []luma.Guest }

func (g *guestList) Guests() []luma.Guest {
	if g == nil {
		return nil
	}
	return g.guests
}

// testHost lends Sniff the event's guests from the fake Luma, fetched once
// per event, the way Snout's cache does.
type testHost struct {
	key   string
	mu    sync.Mutex
	lists map[string]*guestList
}

func (h *testHost) EventGuests(eventID string) GuestList {
	h.mu.Lock()
	defer h.mu.Unlock()
	if gl, ok := h.lists[eventID]; ok {
		return gl
	}
	guests, err := luma.GetGuests(eventID, h.key)
	if err != nil {
		return (*guestList)(nil)
	}
	if h.lists == nil {
		h.lists = map[string]*guestList{}
	}
	h.lists[eventID] = &guestList{guests: guests}
	return h.lists[eventID]
}

// testAdmin is Snout's admin login: Basic auth with the password, any user.
func testAdmin(password string) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if _, pass, ok := r.BasicAuth(); !ok || pass != password {
				w.Header().Set("WWW-Authenticate", `Basic realm="Snout by Badger"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			next(w, r)
		}
	}
}

// fakeLuma is Snout's fake Luma (snout/server_test.go): one event, two guests.
func fakeLuma(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-luma-api-key") != "test-key" {
			http.Error(w, "bad key", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/public/v1/calendar/list-events":
			io.WriteString(w, `{"entries":[{"event":{"api_id":"evt-1","name":"SREday Test","start_at":"`+
				time.Now().Format(time.RFC3339)+`","timezone":"America/Chicago"}}],"next_cursor":null}`)
		case "/public/v1/event/get-guests":
			if r.URL.Query().Get("pagination_cursor") == "" {
				io.WriteString(w, `{"entries":[{"guest":{"api_id":"gst-1","name":"Ada Lovelace","email":"ada@example.com",
					"user_first_name":"Ada","user_last_name":"Lovelace",
					"check_in_qr_code":"https://luma.com/check-in/evt-1?pk=g-aaaaaa1",
					"event_tickets":[{"api_id":"tkt-aaaaaa1"}],
					"registration_answers":[{"label":"Job Title","answer":"CTO"},{"label":"Company","answer":"Analytical"},
					{"label":"What is your LinkedIn profile?","answer":"/in/Ada-L"},{"label":"Terms and Conditions","answer":true}]}}],
					"next_cursor":"page2"}`)
			} else {
				io.WriteString(w, `{"entries":[{"guest":{"api_id":"gst-2","name":"Grace Hopper","email":"grace@example.com",
					"check_in_qr_code":"https://luma.com/check-in/evt-1?pk=g-bbbbbb2","registration_answers":[]}}],"next_cursor":null}`)
			}
		default:
			http.NotFound(w, r)
		}
	}))
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
	api, raw := sites.APIURL, sites.RawURL
	sites.APIURL, sites.RawURL = "http://127.0.0.1:1", "http://127.0.0.1:1"
	t.Cleanup(func() { sites.APIURL, sites.RawURL = api, raw })
	fake := fakeLuma(t)
	t.Cleanup(fake.Close)
	luma.BaseURL = fake.URL

	st := &sniffTest{t: t}
	srv, err := New(Config{DataDir: t.TempDir(), LumaAPIKey: "test-key", Admin: testAdmin("pw")}, &testHost{key: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	srv.now = func() time.Time { return st.clock }
	st.srv = srv
	mux := http.NewServeMux()
	srv.Register(mux)
	st.ts = httptest.NewServer(mux)
	t.Cleanup(st.ts.Close)
	st.start = time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	ev := luma.Event{APIId: "evt-1", Name: "SREday Test", Timezone: "Europe/Amsterdam",
		StartAt: st.start.Format(time.RFC3339), EndAt: st.start.Add(8 * time.Hour).Format(time.RFC3339)}
	if _, err := srv.sniff.SyncSchedule(ev, []SchedItem{
		{Key: "Grace_Hopper_Navy_Bugs", Title: "Bugs", Who: "Grace Hopper", Org: "Navy", Kind: "talk", Room: "Main", Start: st.start.Add(time.Hour), End: st.start.Add(90 * time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	st.ss = srv.sniff.Sessions("evt-1")[0]
	st.clock = st.start.AddDate(0, 0, -7) // a week early: test mode
	// The tests' default client is a speaker phone that has the PIN.
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse(st.ts.URL)
	jar.SetCookies(u, []*http.Cookie{{Name: sniffCookie, Value: sniffCookieValue(), Path: "/"}})
	old := http.DefaultClient.Jar
	http.DefaultClient.Jar = jar
	t.Cleanup(func() { http.DefaultClient.Jar = old })
	return st
}

// Without the PIN a speaker page asks for it, everything else is refused;
// the right PIN opens it and every other speaker's page on that phone.
func TestSniffSpeakerNeedsPIN(t *testing.T) {
	st := newSniffTest(t)
	jar, _ := cookiejar.New(nil)
	phone := &http.Client{Jar: jar}
	get := func(p string) (int, string) {
		resp, err := phone.Get(st.ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if c, page := get("/sniff/" + st.ss.Token); c != 401 || !strings.Contains(page, "Speaker PIN") || strings.Contains(page, "Download questions") {
		t.Errorf("no PIN: %d", c)
	}
	for _, p := range []string{"/feed", "/export.csv", "/qr.png"} {
		if c, _ := get("/sniff/" + st.ss.Token + p); c != 403 {
			t.Errorf("%s without PIN: %d", p, c)
		}
	}
	resp, _ := phone.PostForm(st.ts.URL+"/sniff/"+st.ss.Token+"/pin", map[string][]string{"pin": {"1234"}})
	resp.Body.Close()
	if c, page := get("/sniff/" + st.ss.Token); c != 401 || !strings.Contains(page, "not the speakers") && resp.Request.URL.Query().Get("pin") == "" {
		t.Errorf("wrong PIN let in: %d", c)
	}
	resp, _ = phone.PostForm(st.ts.URL+"/sniff/"+st.ss.Token+"/pin", map[string][]string{"pin": {"1111"}})
	resp.Body.Close()
	if c, page := get("/sniff/" + st.ss.Token); c != 200 || !strings.Contains(page, "Download questions") {
		t.Errorf("1111: %d", c)
	}
	if c, _ := get("/sniff/" + st.ss.Token + "/feed"); c != 200 {
		t.Errorf("feed after PIN: %d", c)
	}
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
		ID, T, N   string
		V          int
		A, X, I, H bool
		Who        *attendee
	}
	Rs []struct {
		S   int
		C   string
		N   string
		Who *attendee
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
	if _, f := st.feed("/q/" + code + "/feed?p=1"); len(f.Qs) != 1 || !f.Qs[0].X || f.Phase != event.PhaseTest {
		t.Fatalf("test feed = %+v", f)
	}
	if st.file() != "" {
		t.Fatalf("test post on disk: %s", st.file())
	}

	// Live: the test post is gone.
	st.clock = st.start.Add(30 * time.Minute)
	if _, f := st.feed("/q/" + code + "/feed?p=1"); len(f.Qs) != 0 || f.Phase != event.PhaseLive {
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
	_, f := st.feed("/q/" + code + "/feed?p=1")
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
	// The speaker sees who asked right away (the attendee the name matched).
	_, sf := st.feed("/sniff/" + st.ss.Token + "/feed")
	for _, q := range sf.Qs {
		if q.ID == "q-ada-00001" && (q.Who == nil || q.Who.First != "Ada" || q.Who.Last != "Lovelace" || q.Who.Company != "Analytical" || q.Who.Email != "ada@example.com") {
			t.Errorf("speaker sees %+v", q.Who)
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

	// The asker's phone hears that the speaker hearted its question (by its
	// own ids only); a phone asking about someone else's id hears nothing.
	st.post("/sniff/"+st.ss.Token+"/mod", map[string]any{"q": "q-ada-00001", "mod": "important"})
	hearts := func(my string) []string {
		resp, err := http.Get(st.ts.URL + "/q/" + code + "/feed?my=" + my)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out struct{ Hearts []string }
		json.NewDecoder(resp.Body).Decode(&out)
		return out.Hearts
	}
	if h := hearts("q-ada-00001,q-other-0001"); len(h) != 1 || h[0] != "q-ada-00001" {
		t.Errorf("asker's hearts = %v", h)
	}
	if h := hearts("q-anon-0001"); len(h) != 0 {
		t.Errorf("hearts for a question not hearted = %v", h)
	}
	st.post("/sniff/"+st.ss.Token+"/mod", map[string]any{"q": "q-ada-00001", "mod": "unimportant"})
	_, f = st.feed("/q/" + code + "/feed?p=1")

	// Nothing new: 204, no body.
	if c, _ := st.feed("/q/" + code + "/feed?p=1&v=" + f.V); c != http.StatusNoContent {
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
		if r.S == 3 && (r.C != "great" || r.N != "Ada") || r.S == 2 && (r.Who != nil || r.N != "") || r.S == 1 {
			t.Errorf("rating %+v", r)
		}
	}

	// Hearts and hides only: no "answered" any more, from the desktop or the phone.
	if c, _ := st.post("/sniff/"+st.ss.Token+"/mod", map[string]any{"q": "q-ada-00001", "mod": "answered"}); c != 400 {
		t.Errorf("answered allowed: %d", c)
	}
	if c, _ := st.post("/sniff/"+st.ss.Token+"/mod", map[string]any{"q": "q-ada-00001", "mod": "important"}); c != 200 {
		t.Fatal("heart")
	}
	st.post("/sniff/"+st.ss.Token+"/mod", map[string]any{"q": "q-anon-0001", "mod": "hide"})
	_, f = st.feed("/q/" + code + "/feed?p=1")
	heart := false
	for _, q := range f.Qs {
		if q.ID == "q-ada-00001" && !q.I || q.ID == "q-anon-0001" && !q.H {
			t.Errorf("after mod: %+v", q)
		}
		heart = heart || q.I
	}
	if len(f.Qs) != 2 || !heart {
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
		if q.ID == "q-ada-00001" && (!q.Important || q.Who == nil) {
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
	ev := luma.Event{APIId: "evt-9", Name: "SREday X"}
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
			if !strings.Contains(string(b), "Ada,Lovelace") || !strings.Contains(string(b), "From a form") || !strings.Contains(string(b), "Peter") {
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
	if !strings.Contains(string(b), "Grace Hopper") || !strings.Contains(string(b), "Open dashboard") {
		t.Error("event page lacks the speaker's card")
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

// One QR per talk: the old event-wide code lists no talks, the talk's QR
// and slide open the talk itself, and the PIN turns the talk's page into
// the presenter's view (hearts and hides only).
func TestSniffOneQRPerTalkAndPresenter(t *testing.T) {
	st := newSniffTest(t)
	st.clock = st.start.Add(90 * time.Minute) // live
	ec, err := st.srv.sniff.EventCode("evt-1")
	if err != nil {
		t.Fatal(err)
	}
	resp, _ := http.Get(st.ts.URL + "/q/" + ec)
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(page), "Scan the QR code on the speaker") || strings.Contains(string(page), "Grace Hopper") {
		t.Errorf("old event code page:\n%s", page)
	}
	r := httptest.NewRequest("GET", "/", nil)
	if u := st.srv.audienceURL(r, st.ss); !strings.HasSuffix(u, "/q/"+st.ss.Code) {
		t.Errorf("QR opens %q", u)
	}

	jar, _ := cookiejar.New(nil)
	phone := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(p string) string {
		resp, err := phone.Get(st.ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return string(b)
	}
	if page := get("/q/" + st.ss.Code); !strings.Contains(page, "I'm the speaker") || strings.Contains(page, `href="/q/`+ec) || strings.Contains(page, "Speaker view:") {
		t.Errorf("audience page: no presenter button, or a way to other talks")
	}
	st.ask("q-room-00001", "Why bugs?", "Ada", false, "rl-aaaaaaaa")
	st.ask("q-room-00002", "And moths?", "", true, "rl-bbbbbbbb")
	mod := func(m string) int {
		b, _ := json.Marshal(map[string]string{"q": "q-room-00002", "mod": m})
		resp, err := phone.Post(st.ts.URL+"/q/"+st.ss.Code+"/mod", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := mod("important"); c != 403 {
		t.Errorf("heart without the PIN: %d", c)
	}
	pin := func(p string) string {
		resp, err := phone.PostForm(st.ts.URL+"/q/"+st.ss.Code+"/pin", map[string][]string{"pin": {p}})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.Header.Get("Location")
	}
	for i := 0; i < 15; i++ { // wrong tries never lock it
		if loc := pin("0000"); !strings.Contains(loc, "pin=") {
			t.Errorf("wrong PIN went to %q", loc)
		}
	}
	if loc := pin("1111"); loc != "/q/"+st.ss.Code {
		t.Errorf("1111 went to %q", loc)
	}
	if page := get("/q/" + st.ss.Code); !strings.Contains(page, "Speaker view:") || strings.Contains(page, "I'm the speaker") {
		t.Errorf("no presenter view after the PIN")
	}
	if c := mod("answered"); c != 400 {
		t.Errorf("answered from the phone: %d", c)
	}
	if c := mod("important"); c != 200 {
		t.Errorf("heart: %d", c)
	}
	if c := mod("hide"); c != 200 {
		t.Errorf("hide: %d", c)
	}
	// The room no longer sees the hidden one; the presenter does, hearted.
	if _, f := st.feed("/q/" + st.ss.Code + "/feed"); len(f.Qs) != 0 { // the room gets nobody's questions
		t.Errorf("room feed: %+v", f.Qs)
	}
	resp, _ = phone.Get(st.ts.URL + "/q/" + st.ss.Code + "/feed?p=1")
	var f feedOut
	json.NewDecoder(resp.Body).Decode(&f)
	resp.Body.Close()
	hearted := false
	for _, q := range f.Qs {
		if q.ID == "q-room-00002" && !q.I && q.H { // the eye dropped the heart
			hearted = true
		}
	}
	if len(f.Qs) != 2 || !hearted {
		t.Errorf("presenter feed: %+v", f.Qs)
	}
	// Log out: back to the question form; hearts need the PIN again.
	if page := get("/q/" + st.ss.Code); !strings.Contains(page, "/q/"+st.ss.Code+"/logout") {
		t.Errorf("speaker view has no Log out")
	}
	lo, err := phone.PostForm(st.ts.URL+"/q/"+st.ss.Code+"/logout", nil)
	if err != nil || lo.StatusCode != http.StatusSeeOther || lo.Header.Get("Location") != "/q/"+st.ss.Code {
		t.Fatalf("logout: %v %v", lo, err)
	}
	lo.Body.Close()
	if page := get("/q/" + st.ss.Code); !strings.Contains(page, "I'm the speaker") || strings.Contains(page, "Speaker view:") {
		t.Errorf("still the speaker view after Log out")
	}
	if c := mod("important"); c != 403 {
		t.Errorf("heart after Log out: %d", c)
	}
}

// A harmful question is thanked like any other but never stored; a
// harmful rating comment loses its words, keeps its stars.
func TestSniffFilterIsQuiet(t *testing.T) {
	st := newSniffTest(t)
	var last string
	post := func(path string, body map[string]any) int {
		b, _ := json.Marshal(body)
		resp, err := http.Post(st.ts.URL+"/q/"+st.ss.Code+"/"+path, "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		out, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		last = string(out)
		return resp.StatusCode
	}
	if c := post("ask", map[string]any{"id": "aaaaaaaaaaaa", "text": "you are a f*cking idiot", "rl": "rlrlrlrlrl"}); c != 200 {
		t.Errorf("harmful ask answered %d", c)
	}
	// The asker's phone is told, to show "You can do better than this".
	if !strings.Contains(last, `"filtered":true`) {
		t.Errorf("harmful ask reply = %s", last)
	}
	if c := post("ask", map[string]any{"id": "bbbbbbbbbbbb", "text": "Fair question", "name": "Sh1thead", "rl": "rlrlrlrlrl"}); c != 200 {
		t.Errorf("harmful name answered %d", c)
	}
	if c := post("ask", map[string]any{"id": "cccccccccccc", "text": "How does eBPF perform?", "rl": "rlrlrlrlrl"}); c != 200 {
		t.Errorf("ask answered %d", c)
	}
	if strings.Contains(last, "filtered") {
		t.Errorf("a fine question reported as filtered: %s", last)
	}
	post("rate", map[string]any{"k": "kkkkkkkkkkkk", "stars": 1, "text": "this talk sucks", "rl": "rlrlrlrlrl"})
	v := st.srv.sniff.View(st.ss.Code, st.srv.now())
	if len(v.Questions) != 1 || v.Questions[0].Text != "How does eBPF perform?" {
		t.Errorf("questions stored: %+v", v.Questions)
	}
	if len(v.Ratings) != 1 || v.Ratings[0].Stars != 1 || v.Ratings[0].Comment != "" {
		t.Errorf("rating stored: %+v", v.Ratings)
	}
}

// Important: the speaker's pick, in their feed.
func TestSniffImportant(t *testing.T) {
	st := newSniffTest(t)
	now := st.srv.now()
	if err := st.srv.sniff.Ask(st.ss.Code, now, "qqqqqqqqqqqq", "Why eBPF?", "", nil, "", 0); err != nil {
		t.Fatal(err)
	}
	if err := st.srv.sniff.Mod(st.ss.Code, now, "qqqqqqqqqqqq", "important"); err != nil {
		t.Fatal(err)
	}
	feed := st.srv.speakerFeed(st.ss)
	if qs := feed["qs"].([]speakerQ); len(qs) != 1 || !qs[0].Heart {
		t.Errorf("not important: %+v", qs)
	}
	st.srv.sniff.Mod(st.ss.Code, now, "qqqqqqqqqqqq", "unimportant")
	if qs := st.srv.speakerFeed(st.ss)["qs"].([]speakerQ); qs[0].Heart {
		t.Errorf("still important")
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

func TestTalkShards(t *testing.T) {
	csv := "status,name,photo,linkedin,linkedin2\n" +
		"talk,Ankit Asthana & Dastan Oryngali,Ankit Asthana & Dastan Oryngali.png,https://www.linkedin.com/in/ankit/,https://www.linkedin.com/in/dastan/\n" +
		"talk,Ada Lovelace,Ada Lovelace.png,javascript:alert(1),\n" +
		"talk,Grace Hopper,Grace Hopper.png,,https://linkedin.com/in/grace\n"
	m := talkShards([]byte(csv), "https://sreday.com")
	duo := m[foldName("Ankit Asthana & Dastan Oryngali")]
	if duo.Photo != "https://sreday.com/speakers/Ankit%20Asthana%20&%20Dastan%20Oryngali.webp" || duo.LinkedIn != "https://www.linkedin.com/in/ankit/" || duo.LinkedIn2 != "https://www.linkedin.com/in/dastan/" {
		t.Errorf("duo = %+v", duo)
	}
	if ada := m[foldName("Ada Lovelace")]; ada.LinkedIn != "" {
		t.Errorf("a script link kept: %+v", ada)
	}
	if g := m[foldName("Grace Hopper")]; g.LinkedIn != "https://linkedin.com/in/grace" || g.LinkedIn2 != "" {
		t.Errorf("grace = %+v", g)
	}
}

func TestSniffHarmful(t *testing.T) {
	bad := []string{
		"you n1gg3r", "N I G G E R", "what a fuuuucking mess", "f.u.c.k this", "Motherfucker", "show us your tits",
		"total bullsh1t talk", "You are an idiot", "this talk sucks", "send nudes", "what a retard", "a$$hole",
		"F A G", "kys", "Nígger", "pornhub link", "you clueless moron",
		"Nigg er", "nig ger", "Nigg.er", "f uck you", "sh it", "c u nt", "fa ggot",
	}
	good := []string{
		"How do you kill a stuck process?", "Do naked pods still make sense?", "Any niggles with eBPF?",
		"Is this a stupid question?", "Scunthorpe datacenter?", "How did you assess the risk?",
		"Does it work in Niger and Nigeria?", "Class of bugs in the analysis?", "Kung fu of cockpit dashboards?",
		"Why the ugly hack in slide 3?", "Choose boring technology?", "Penetration testing tips?",
		"What about the master/slave naming?", "Core dump analysis?", "Is v1.x.x supported?", "Dick Hardt's OAuth work?",
		"Peacocks and shuttlecocks?", "Should I pass the assets?", "Grapes or rapeseed oil?",
		"A picnic until noon?", "Is it on?", "Is hit rate good?", "Pass it on", "Am I as smart as...", "The big ger", "Was it fun? Cool!",
	}
	for _, s := range bad {
		if !harmful(s) {
			t.Errorf("let through: %q", s)
		}
	}
	for _, s := range good {
		if harmful(s) {
			t.Errorf("filtered: %q", s)
		}
	}
}

// TestSniffOldSlugs: new sessions get no /sniff/<first name> link (anyone
// could guess it); one given before keeps opening the page, any case.
func TestSniffOldSlugs(t *testing.T) {
	st := newSniffTest(t)
	if st.ss.Slug != "" {
		t.Errorf("new session got the old-style slug %q", st.ss.Slug)
	}
	st.srv.sniff.mu.Lock()
	st.srv.sniff.sessions[st.ss.Code].Slug = "grace"
	st.srv.sniff.mu.Unlock()
	// Guessable, so it opens only the talk's audience page, never the secret.
	plain := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, p := range []string{"/sniff/grace", "/sniff/GRACE"} {
		resp, err := plain.Get(st.ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/q/"+st.ss.Code {
			t.Errorf("%s: %d %q", p, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	if resp, _ := http.Get(st.ts.URL + "/sniff/nobody"); resp.StatusCode != 404 {
		t.Errorf("unknown name: %d", resp.StatusCode)
	}
}

func TestSniffNiceLinks(t *testing.T) {
	st := newSniffTest(t)
	plain := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(path string, auth bool, cookie string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("GET", st.ts.URL+path, nil)
		if auth {
			req.SetBasicAuth("admin", "pw")
		}
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: sniffCookie, Value: cookie})
		}
		resp, err := plain.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	// The old organizers' link moves to /sniff/<secret>.
	resp := get("/admin/sniff/evt-1", true, "")
	sec, _ := st.srv.sniff.HasEventSecret("evt-1")
	if resp.StatusCode != http.StatusSeeOther || sec == "" || resp.Header.Get("Location") != "/sniff/"+sec {
		t.Fatalf("old admin link: %d %q (secret %q)", resp.StatusCode, resp.Header.Get("Location"), sec)
	}
	if again, _ := st.srv.sniff.EventSecret("evt-1"); again != sec {
		t.Errorf("secret changed: %q then %q", sec, again)
	}
	// The team dashboard is passworded and has every event's questions; the
	// event page is anyone-with-the-link, never indexed, with no team actions.
	if resp := get("/sniff", false, ""); resp.StatusCode != 401 {
		t.Errorf("dashboard without password: %d", resp.StatusCode)
	}
	if resp := get("/admin/sniff/evt-1/feedback.csv", false, ""); resp.StatusCode != 401 {
		t.Errorf("all questions without password: %d", resp.StatusCode)
	}
	for _, p := range []string{"/feedback.csv", "/qr.png", "/slide.png"} {
		if resp := get("/sniff/"+sec+p, false, ""); resp.StatusCode == 200 {
			t.Errorf("event %s still served", p)
		}
	}
	if r, _ := plain.PostForm(st.ts.URL+"/sniff/"+sec+"/add", map[string][]string{"title": {"x"}}); r == nil || r.StatusCode == http.StatusSeeOther {
		t.Errorf("add by link still works: %v", r)
	}
	// The talk's own QR and slide, by name, without the PIN.
	for _, p := range []string{"/grace/qr.png", "/grace/slide.png"} {
		if resp := get("/sniff/"+sec+p, false, ""); resp.StatusCode != 200 {
			t.Errorf("%s: %d", p, resp.StatusCode)
		}
		if resp := get("/sniff/zzzzzzzz22"+p, false, ""); resp.StatusCode != 404 {
			t.Errorf("%s with a wrong secret: %d", p, resp.StatusCode)
		}
	}
	resp = get("/sniff/"+sec, false, "")
	if resp.Header.Get("X-Robots-Tag") == "" || resp.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("event page headers: %v", resp.Header)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(sec) != 10 {
		t.Errorf("secret %q is not 10 characters", sec)
	}
	nice := "/sniff/" + sec + "/grace"
	if resp.StatusCode != 200 || !strings.Contains(string(page), nice) {
		t.Fatalf("event page %d lacks %s", resp.StatusCode, nice)
	}
	// The speaker's page by name: the PIN first, then back to the same link.
	resp = get(nice, false, "")
	page, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 401 || !strings.Contains(string(page), "/sniff/"+st.ss.Token+"/pin") {
		t.Errorf("speaker page without PIN: %d", resp.StatusCode)
	}
	r, err := plain.PostForm(st.ts.URL+"/sniff/"+st.ss.Token+"/pin", map[string][]string{"pin": {sniffPIN}})
	if err != nil || r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != nice {
		t.Errorf("PIN: %v %v", r, err)
	}
	if resp := get(nice, false, sniffCookieValue()); resp.StatusCode != 200 {
		t.Errorf("speaker page by name: %d", resp.StatusCode)
	}
	if resp := get("/sniff/"+sec+"/nobody", false, sniffCookieValue()); resp.StatusCode != 404 {
		t.Errorf("unknown name: %d", resp.StatusCode)
	}
	// The old token link moves to the name link.
	if resp := get("/sniff/"+st.ss.Token, false, ""); resp.StatusCode != http.StatusPermanentRedirect || resp.Header.Get("Location") != nice {
		t.Errorf("token link: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestSniffSpeakerNames(t *testing.T) {
	st := &SniffStore{sessions: map[string]*SniffSession{}}
	at := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	for i, who := range []string{"Grace Hopper", "Grace Kelly", "Grace Hopper", "Ana López & Bob Li", "", "Łukasz Nowak"} {
		code := fmt.Sprintf("C%d", i)
		st.sessions[code] = &SniffSession{Code: code, EventID: "e", Who: who, Title: "Opening Panel", CreatedAt: at.Add(time.Duration(i) * time.Minute)}
	}
	st.sessions["X"] = &SniffSession{Code: "X", EventID: "other", Who: "Grace Hopper"}
	got := st.SpeakerNames("e")
	want := map[string]string{"C0": "grace", "C1": "grace-kelly", "C2": "grace-2", "C3": "ana", "C4": "opening-panel", "C5": "lukasz"}
	for code, name := range want {
		if got[code] != name {
			t.Errorf("%s: got %q, want %q", code, got[code], name)
		}
	}
	if len(got) != len(want) {
		t.Errorf("names for other events: %v", got)
	}
}

func TestSniffNotSpeaker(t *testing.T) {
	for _, c := range []struct {
		who, title string
		out        bool
	}{
		{"Official Opening", "Official welcome - Kyndryl & SREday", true},
		{"Coral Keynote", "Coral Keynote", true},
		{"Intro", "Intro by Mark Pawlikowski", true},
		{"", "Lunch", true},
		{"TBA", "Sponsor talk", true},
		{"Grace Hopper", "Keynote: the future", false},
		{"Kannupriya Kalra & Rory Graves", "LLM4S 1.0", false},
		{"Miko Pawlikowski", "Ten years of Kubernetes lessons", false},
	} {
		if got := notSpeaker(c.who, c.title); got != c.out {
			t.Errorf("notSpeaker(%q, %q) = %v", c.who, c.title, got)
		}
	}
	got := speakerItems([]SchedItem{{Who: "Official Opening", Title: "Welcome"}, {Who: "Ada Lovelace", Title: "Engines"}})
	if len(got) != 1 || got[0].Who != "Ada Lovelace" {
		t.Errorf("speakerItems = %v", got)
	}
}

// Test-mode posts survive a restart (a deploy) and vanish once the event
// is live.
func TestSniffTestPostsSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenSniffStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().AddDate(1, 0, 0)
	ev := luma.Event{APIId: "evt-future", Name: "SREday Future", Timezone: "UTC", StartAt: start.Format(time.RFC3339), EndAt: start.Add(8 * time.Hour).Format(time.RFC3339)}
	if _, err := st.SyncSchedule(ev, []SchedItem{{Key: "k", Title: "Talk", Who: "Grace Hopper", Kind: "talk"}}); err != nil {
		t.Fatal(err)
	}
	code := st.Sessions("evt-future")[0].Code
	now := time.Now()
	if err := st.Ask(code, now, "q-test-000001", "Does this survive?", "Ada", nil, "sec-1", 0); err != nil {
		t.Fatal(err)
	}
	re, err := OpenSniffStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if v := re.View(code, now); len(v.Questions) != 1 || !v.Questions[0].Test {
		t.Fatalf("after a restart: %+v", v.Questions)
	}
	if v := re.View(code, start.Add(time.Hour)); len(v.Questions) != 0 {
		t.Errorf("test post shown live: %+v", v.Questions)
	}
	if _, err := os.Stat(filepath.Join(dir, "events", "evt-future", "sniff_test.jsonl")); !os.IsNotExist(err) {
		t.Errorf("test file kept after go-live: %v", err)
	}
}

// A test post lasts an hour, like a test scan in Snout.
func TestSniffTestPostsLastAnHour(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenSniffStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().AddDate(1, 0, 0)
	ev := luma.Event{APIId: "evt-hour", Name: "SREday Hour", Timezone: "UTC", StartAt: start.Format(time.RFC3339), EndAt: start.Add(8 * time.Hour).Format(time.RFC3339)}
	if _, err := st.SyncSchedule(ev, []SchedItem{{Key: "k", Title: "Talk", Who: "Grace Hopper", Kind: "talk"}}); err != nil {
		t.Fatal(err)
	}
	code := st.Sessions("evt-hour")[0].Code
	now := time.Now()
	st.Ask(code, now.Add(-50*time.Minute), "q-old-000001", "Asked 50 min ago", "", nil, "s1", 0)
	st.Ask(code, now, "q-new-000001", "Asked now", "", nil, "s2", 0)
	if v := st.View(code, now); len(v.Questions) != 2 {
		t.Fatalf("both within the hour: %d", len(v.Questions))
	}
	if v := st.View(code, now.Add(15*time.Minute)); len(v.Questions) != 1 || v.Questions[0].ID != "q-new-000001" {
		t.Fatalf("after the first one's hour: %+v", v.Questions)
	}
	st.ExpireTestPosts(now.Add(61 * time.Minute))
	if v := st.View(code, now.Add(61*time.Minute)); len(v.Questions) != 0 {
		t.Errorf("after both hours: %+v", v.Questions)
	}
	if _, err := os.Stat(filepath.Join(dir, "events", "evt-hour", "sniff_test.jsonl")); !os.IsNotExist(err) {
		t.Errorf("test file kept after its posts expired: %v", err)
	}
}

// The heart and the eye undo each other: down drops the heart, up brings
// a question back from the bottom.
func TestSniffHeartAndEyeExclusive(t *testing.T) {
	st := newSniffTest(t)
	st.clock = st.start.Add(90 * time.Minute)
	st.ask("q-excl-00001", "Up or down?", "", true, "rl-aaaaaaaa")
	get := func() Question {
		for _, q := range st.srv.sniff.View(st.ss.Code, st.clock).Questions {
			if q.ID == "q-excl-00001" {
				return q
			}
		}
		t.Fatal("question gone")
		return Question{}
	}
	mod := func(m string) { st.post("/sniff/"+st.ss.Token+"/mod", map[string]any{"q": "q-excl-00001", "mod": m}) }
	mod("important")
	mod("hide")
	if q := get(); !q.Hidden || q.Important {
		t.Errorf("after heart, eye: hidden %v, heart %v", q.Hidden, q.Important)
	}
	mod("important")
	if q := get(); q.Hidden || !q.Important {
		t.Errorf("after eye, heart: hidden %v, heart %v", q.Hidden, q.Important)
	}
}

// A held feed request answers the moment a question comes in, and with
// nothing new after longPoll.
func TestSniffLongPoll(t *testing.T) {
	st := newSniffTest(t)
	st.clock = st.start.Add(90 * time.Minute)
	was := longPoll
	longPoll = 2 * time.Second
	defer func() { longPoll = was }()
	_, f := st.feed("/sniff/" + st.ss.Token + "/feed")
	done := make(chan int, 1)
	began := time.Now()
	go func() {
		c, _ := st.feed("/sniff/" + st.ss.Token + "/feed?wait=1&v=" + f.V)
		done <- c
	}()
	time.Sleep(200 * time.Millisecond)
	st.ask("q-live-00001", "Is it instant?", "", true, "rl-aaaaaaaa")
	select {
	case c := <-done:
		if c != 200 || time.Since(began) > time.Second {
			t.Errorf("held feed: %d after %v", c, time.Since(began))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("held feed didn't wake on a new question")
	}
	_, f = st.feed("/sniff/" + st.ss.Token + "/feed")
	began = time.Now()
	if c, _ := st.feed("/sniff/" + st.ss.Token + "/feed?wait=1&v=" + f.V); c != http.StatusNoContent || time.Since(began) < 1500*time.Millisecond {
		t.Errorf("nothing new: %d after %v", c, time.Since(began))
	}
}

// Without JavaScript the one form sends the question and the rating.
func TestSniffFormSendsBoth(t *testing.T) {
	st := newSniffTest(t)
	st.clock = st.start.Add(90 * time.Minute)
	plain := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := plain.PostForm(st.ts.URL+"/q/"+st.ss.Code+"/ask", map[string][]string{"text": {"Both at once?"}, "stars": {"2"}, "name": {"Peter"}})
	if err != nil || r.StatusCode != http.StatusSeeOther {
		t.Fatalf("form: %v %v", r, err)
	}
	v := st.srv.sniff.View(st.ss.Code, st.clock)
	if len(v.Questions) != 1 || len(v.Ratings) != 1 || v.Ratings[0].Stars != 2 {
		t.Errorf("questions %d, ratings %+v", len(v.Questions), v.Ratings)
	}
}
