package snort

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- A fake GitHub: files per repo, blobs, trees, commits, main ------------------

type fakeGH struct {
	mu       sync.Mutex
	files    map[string]map[string][]byte // repo -> path -> bytes
	blobs    map[string][]byte
	trees    map[string]map[string]string // tree sha -> path -> blob sha (the changes)
	commits  map[string]string            // commit sha -> tree sha
	head     map[string]string            // repo -> commit sha
	made     map[string]int               // repo -> commits made
	moveNext bool                         // the next ref update finds main moved
	n        int
}

func newFakeGH() *fakeGH {
	return &fakeGH{files: map[string]map[string][]byte{}, blobs: map[string][]byte{}, trees: map[string]map[string]string{}, commits: map[string]string{}, head: map[string]string{}, made: map[string]int{}}
}

func (f *fakeGH) put(repo, p string, data []byte) {
	if f.files[repo] == nil {
		f.files[repo] = map[string][]byte{}
		f.head[repo] = "c0"
		f.commits["c0"] = "t0"
	}
	f.files[repo][p] = data
}

func (f *fakeGH) id(kind string) string {
	f.n++
	sum := sha1.Sum([]byte(kind + string(rune(f.n))))
	return hex.EncodeToString(sum[:])
}

func (f *fakeGH) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if rest, ok := strings.CutPrefix(r.URL.Path, "/raw/"); ok { // the file server: owner/repo/main/path
		parts := strings.SplitN(rest, "/", 4)
		if len(parts) == 4 {
			if data, ok := f.files[parts[0]+"/"+parts[1]][parts[3]]; ok {
				w.Write(data)
				return
			}
		}
		http.NotFound(w, r)
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/repos/")
	parts := strings.SplitN(p, "/", 3)
	if len(parts) < 3 {
		http.NotFound(w, r)
		return
	}
	repo, rest := parts[0]+"/"+parts[1], parts[2]
	if r.Header.Get("Authorization") == "" && r.Method != "GET" {
		http.Error(w, "no token", 401)
		return
	}
	body, _ := io.ReadAll(r.Body)
	switch {
	case r.Method == "GET" && strings.HasPrefix(rest, "contents"):
		file, _ := url.PathUnescape(strings.TrimPrefix(strings.TrimPrefix(rest, "contents"), "/"))
		if data, ok := f.files[repo][file]; ok {
			w.Write(data)
			return
		}
		// a folder: list what is under it
		type en struct{ Name, Type string }
		var list []en
		seen := map[string]bool{}
		pre := file
		if pre != "" {
			pre += "/"
		}
		for k := range f.files[repo] {
			if !strings.HasPrefix(k, pre) {
				continue
			}
			name, more, dir := strings.Cut(strings.TrimPrefix(k, pre), "/")
			_ = more
			if seen[name] {
				continue
			}
			seen[name] = true
			t := "file"
			if dir {
				t = "dir"
			}
			list = append(list, en{name, t})
		}
		if len(list) == 0 {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(list)
	case r.Method == "POST" && rest == "git/blobs":
		var b struct{ Content string }
		json.Unmarshal(body, &b)
		data, _ := base64.StdEncoding.DecodeString(b.Content)
		sha := f.id("b")
		f.blobs[sha] = data
		json.NewEncoder(w).Encode(map[string]string{"sha": sha})
	case r.Method == "GET" && rest == "git/ref/heads/main":
		json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": f.head[repo]}})
	case r.Method == "GET" && strings.HasPrefix(rest, "git/commits/"):
		json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{"sha": f.commits[strings.TrimPrefix(rest, "git/commits/")]}})
	case r.Method == "POST" && rest == "git/trees":
		var t struct {
			Tree []struct{ Path, SHA string }
		}
		json.Unmarshal(body, &t)
		sha := f.id("t")
		f.trees[sha] = map[string]string{}
		for _, e := range t.Tree {
			f.trees[sha][e.Path] = e.SHA
		}
		json.NewEncoder(w).Encode(map[string]string{"sha": sha})
	case r.Method == "POST" && rest == "git/commits":
		var c struct {
			Tree    string
			Parents []string
		}
		json.Unmarshal(body, &c)
		sha := f.id("c")
		f.commits[sha] = c.Tree
		json.NewEncoder(w).Encode(map[string]string{"sha": sha})
	case r.Method == "PATCH" && rest == "git/refs/heads/main":
		if f.moveNext {
			f.moveNext = false
			http.Error(w, "not a fast forward", 422)
			return
		}
		var u struct{ SHA string }
		json.Unmarshal(body, &u)
		for path, blob := range f.trees[f.commits[u.SHA]] {
			f.files[repo][path] = f.blobs[blob]
		}
		f.head[repo] = u.SHA
		f.made[repo]++
		w.Write([]byte(`{}`))
	case r.Method == "GET" && strings.HasPrefix(rest, "actions/runs"):
		w.Write([]byte(`{"workflow_runs":[{"name":"build","status":"completed","conclusion":"success"}]}`))
	default:
		http.NotFound(w, r)
	}
}

// --- Helpers ----------------------------------------------------------------------

func testServer(t *testing.T, gh *fakeGH) *Server {
	t.Helper()
	ts := httptest.NewServer(gh)
	t.Cleanup(ts.Close)
	oldAPI, oldRaw := apiURL, rawURL
	apiURL, rawURL = ts.URL, ts.URL+"/raw"
	t.Cleanup(func() { apiURL, rawURL = oldAPI, oldRaw })
	s, err := New(Config{Base: "/snort", DataDir: t.TempDir(), AdminUser: "admin", AdminPassword: "pw", Feed: "https://feed.invalid/exec?key=k",
		Tokens: "sreday=t1,conf42=t3"})
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC) }
	s.events = map[string][]Event{
		"llmday": {
			{ID: "llmday/2026-london-q4", Brand: "llmday", Name: "LLMday London 2026 Q4", Day: "2026-11-26", Repo: "sreday/llmday", File: "2026-london-q4/_db/talks.csv", City: "London", Year: 2026, Quarter: 4},
			{ID: "llmday/2026-hamburg-q4", Brand: "llmday", Name: "LLMday Hamburg 2026 Q4", Day: "2026-11-18", Repo: "sreday/llmday", File: "past/2026-hamburg-q4/_db/talks.csv", City: "Hamburg", Year: 2026, Quarter: 4},
		},
		"conf42": {{ID: "conf42/devops2027", Brand: "conf42", Name: "Conf42 DevOps 2027", Day: "2027-01-21", Repo: "conf42/src", File: "_db/DEVOPS_2027.csv", Tab: "DEVOPS_2027", Suffix: "devops"}},
	}
	return s
}

func do(t *testing.T, h http.Handler, method, path string, body io.Reader, ctype string, auth bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if auth {
		req.SetBasicAuth("admin", "pw")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func sheetHeader() []string {
	return append([]string(nil), siteHeader...)
}

// a sheet row in the site's columns (the feed already cut the email column)
func sheetRow(name, title, abstract string) []string {
	r := make([]string, len(siteHeader))
	r[1], r[2], r[3], r[4], r[6], r[11], r[12] = "talk", name, "1", "1", name+".png", title, abstract
	return r
}

func pushOf(t *testing.T, s *Server, id string) pushAnswer {
	t.Helper()
	rec := do(t, s.Handler(), "POST", "/admin/push", strings.NewReader("event="+url.QueryEscape(id)), "application/x-www-form-urlencoded", true)
	var a pushAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
		t.Fatalf("push answer %q: %v", rec.Body.String(), err)
	}
	return a
}

// --- The lineup file ------------------------------------------------------------------

func TestLineupFileBytes(t *testing.T) {
	rows := [][]string{
		sheetRow("Jane Example", "Graphs, at scale", "Line one\nline \"two\""),
		make([]string, len(siteHeader)), // a blank row
		sheetRow("Sam Sample", "Plain title", "short"),
	}
	header := append(sheetHeader(), "") // a spare empty column at the end
	for i := range rows {
		rows[i] = append(rows[i], "")
	}
	got, n, err := lineupFile(siteHeader, header, rows)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	s := string(got)
	if strings.HasSuffix(s, "\n") || strings.Count(s, "\r\n") != 2 {
		t.Fatalf("want CRLF between rows and no newline at the end: %q", s)
	}
	if !strings.Contains(s, "\"Line one\nline \"\"two\"\"\"") || !strings.Contains(s, `"Graphs, at scale"`) || strings.Contains(s, `"Plain title"`) {
		t.Fatalf("quoting: %q", s)
	}
	if !strings.HasPrefix(s, strings.Join(siteHeader, ",")+"\r\n") {
		t.Fatalf("header: %q", s)
	}
}

func TestLineupFileRefuses(t *testing.T) {
	ok := [][]string{sheetRow("Jane Example", "T", "A")}
	// an email column that was not cut
	withEmail := append(sheetHeader()[:4:4], append([]string{""}, siteHeader[4:]...)...)
	r := sheetRow("Jane Example", "T", "A")
	r = append(r[:4:4], append([]string{"jane@example.com"}, r[4:]...)...)
	cases := map[string]struct {
		header []string
		rows   [][]string
		want   error
	}{
		"email column":    {withEmail, [][]string{r}, errColumns},
		"email in a cell": {sheetHeader(), [][]string{sheetRow("Jane Example", "T", "write to jane.example@example.org")}, errPrivate},
		"phone in a cell": {sheetHeader(), [][]string{sheetRow("Jane Example", "T", "call +44 20 7946 0000")}, errPrivate},
		"renamed column":  {append(sheetHeader()[:14:14], "about"), ok, errColumns},
		"empty":           {sheetHeader(), [][]string{make([]string, len(siteHeader))}, errEmpty},
	}
	for name, c := range cases {
		if _, _, err := lineupFile(siteHeader, c.header, c.rows); err != c.want {
			t.Errorf("%s: got %v, want %v", name, err, c.want)
		}
	}
	// Years and counts are not phone numbers.
	if _, _, err := lineupFile(siteHeader, sheetHeader(), [][]string{sheetRow("Jane Example", "T", "Since 2019, 10,000 users and +30% growth")}); err != nil {
		t.Errorf("ordinary abstract refused: %v", err)
	}
}

// --- Push -------------------------------------------------------------------------------

func TestPushAndSame(t *testing.T) {
	gh := newFakeGH()
	old := writeCSV(siteHeader, [][]string{sheetRow("Old Speaker", "Old", "old")})
	gh.put("sreday/llmday", "past/2026-hamburg-q4/_db/talks.csv", old)
	s := testServer(t, gh)
	rows := [][]string{sheetRow("Jane Example", "New talk", "about graphs")}
	s.feed = func(ev Event) ([]string, [][]string, error) { return sheetHeader(), rows, nil }

	a := pushOf(t, s, "llmday/2026-hamburg-q4")
	if a.Result != "pushed" || a.Commit == "" || !strings.Contains(a.URL, "github.com/sreday/llmday/commit/") {
		t.Fatalf("push: %+v", a)
	}
	if got := gh.files["sreday/llmday"]["past/2026-hamburg-q4/_db/talks.csv"]; !bytes.Equal(got, writeCSV(siteHeader, rows)) {
		t.Fatalf("committed bytes: %q", got)
	}
	if a2 := pushOf(t, s, "llmday/2026-hamburg-q4"); a2.Result != "same" || gh.made["sreday/llmday"] != 1 {
		t.Fatalf("second push: %+v, commits %d", a2, gh.made["sreday/llmday"])
	}
	// The build state comes from GitHub's runs.
	rec := do(t, s.Handler(), "GET", "/admin/build?event=llmday%2F2026-hamburg-q4", nil, "", true)
	if !strings.Contains(rec.Body.String(), `"live"`) {
		t.Fatalf("build: %s", rec.Body.String())
	}
}

func TestRefusalShowsNothingFromTheSheet(t *testing.T) {
	gh := newFakeGH()
	gh.put("sreday/llmday", "past/2026-hamburg-q4/_db/talks.csv", writeCSV(siteHeader, [][]string{sheetRow("Old Speaker", "Old", "old")}))
	s := testServer(t, gh)
	s.feed = func(ev Event) ([]string, [][]string, error) {
		return sheetHeader(), [][]string{sheetRow("Jane Example", "Secret title", "mail jane@example.com")}, nil
	}
	a := pushOf(t, s, "llmday/2026-hamburg-q4")
	if a.Result != "refused" {
		t.Fatalf("want refused: %+v", a)
	}
	page := do(t, s.Handler(), "GET", "/?b=llmday", nil, "", true).Body.String()
	for _, leak := range []string{"Jane", "Secret", "example.com"} {
		if strings.Contains(a.Note, leak) || strings.Contains(page, leak) {
			t.Fatalf("%q shown to the user: %q", leak, a.Note)
		}
	}
	if gh.made["sreday/llmday"] != 0 {
		t.Fatal("refused push committed")
	}
}

func TestPushRetriesWhenMainMoves(t *testing.T) {
	gh := newFakeGH()
	gh.put("sreday/llmday", "past/2026-hamburg-q4/_db/talks.csv", []byte(strings.Join(siteHeader, ",")))
	s := testServer(t, gh)
	s.feed = func(ev Event) ([]string, [][]string, error) {
		return sheetHeader(), [][]string{sheetRow("Jane Example", "T", "A")}, nil
	}
	gh.moveNext = true
	if a := pushOf(t, s, "llmday/2026-hamburg-q4"); a.Result != "pushed" {
		t.Fatalf("push after main moved: %+v", a)
	}
}

// --- Circles -----------------------------------------------------------------------------

// headshot draws a 1000x1000 disc: the circle colour around a grey "person".
func headshot(ring func(x, y int) color.NRGBA, n int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	c := float64(n) / 2
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			d := math.Hypot(float64(x)-c, float64(y)-c)
			switch {
			case d > c-1:
				// clear corners
			case d < c*0.45:
				img.SetNRGBA(x, y, color.NRGBA{120, 120, 120, 255})
			default:
				img.SetNRGBA(x, y, ring(x, y))
			}
		}
	}
	return img
}

func flat(r, g, b uint8) func(x, y int) color.NRGBA {
	return func(int, int) color.NRGBA { return color.NRGBA{r, g, b, 255} }
}

func rainbow(x, y int) color.NRGBA {
	h := math.Mod(math.Atan2(float64(y)-500, float64(x)-500)*180/math.Pi+360, 360)
	s, v := 0.39, 0.85
	c := v * s
	hp := h / 60
	xx := c * (1 - math.Abs(math.Mod(hp, 2)-1))
	var r, g, b float64
	switch int(hp) {
	case 0:
		r, g = c, xx
	case 1:
		r, g = xx, c
	case 2:
		g, b = c, xx
	case 3:
		g, b = xx, c
	case 4:
		r, b = xx, c
	default:
		r, b = c, xx
	}
	m := v - c
	return color.NRGBA{uint8((r + m) * 255), uint8((g + m) * 255), uint8((b + m) * 255), 255}
}

func TestCircleBrand(t *testing.T) {
	cases := map[string]func(x, y int) color.NRGBA{
		"sreday":      flat(131, 63, 61),
		"llmday":      flat(222, 225, 174),
		"platformday": flat(214, 134, 20),
		"pec":         rainbow,
		"":            flat(128, 128, 130), // grey: no circle colour
	}
	for want, ring := range cases {
		if got := circleBrand(headshot(ring, 1000)); got != want {
			t.Errorf("want %q, got %q", want, got)
		}
	}
}

func pngOf(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// --- Headshot upload ------------------------------------------------------------------------

func TestUploadSortsAndAdds(t *testing.T) {
	gh := newFakeGH()
	gh.put("sreday/sreday", "README.md", []byte("x"))
	s := testServer(t, gh)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for name, img := range map[string]image.Image{
		"Jane Example.png": headshot(flat(131, 63, 61), 1000),
		"bad/../x?.png":    headshot(flat(131, 63, 61), 1000),
	} {
		fw, _ := mw.CreateFormFile("files", name)
		fw.Write(pngOf(t, img))
	}
	mw.Close()
	rec := do(t, s.Handler(), "POST", "/admin/headshots", &body, mw.FormDataContentType(), true)
	var got struct{ Shots []*shot }
	json.Unmarshal(rec.Body.Bytes(), &got)
	var id string
	for _, sh := range got.Shots {
		switch sh.Upload {
		case "Jane Example.png":
			if sh.Brand != "sreday" || sh.Error != "" {
				t.Fatalf("sorted: %+v", sh)
			}
			id = sh.ID
		default:
			if sh.Error == "" {
				t.Fatalf("unsafe name accepted: %+v", sh)
			}
		}
	}
	req, _ := json.Marshal(map[string]any{"ids": []string{id}})
	rec = do(t, s.Handler(), "POST", "/admin/headshots/add", bytes.NewReader(req), "application/json", true)
	if !strings.Contains(rec.Body.String(), "github.com/sreday/sreday/commit/") {
		t.Fatalf("add: %s", rec.Body.String())
	}
	if gh.files["sreday/sreday"]["speakers/Jane Example.png"] == nil {
		t.Fatal("headshot not in speakers/")
	}
}

// --- Events and auth ------------------------------------------------------------------------

func TestParseEvents(t *testing.T) {
	ev, ok := parseSiteEvent("sreday", "past/", "2026-amsterdam-q4", 2026, 4, "brand_name: SREday\ncity_name: Amsterdam\nstart_time: 2026-10-30T09:15:00+01:00\n")
	if !ok || ev.Name != "SREday Amsterdam 2026 Q4" || ev.File != "past/2026-amsterdam-q4/_db/talks.csv" || ev.Day != "2026-10-30" {
		t.Fatalf("%+v", ev)
	}
	list := parseConf42("events:\n\n  - name: DevOps\n    date: 2027-01-21\n    color: \"#5F3202\"\n    short_url: devops2027\n    db_path: ./_db/DEVOPS_2027.csv\n    sponsors:\n      gold:\n        - x\n  - name: Cast\n    date: 2021-01-01\n    short_url: cast1\n    db_path: ~\nother:\n")
	if len(list) != 1 || list[0].Tab != "DEVOPS_2027" || list[0].Name != "Conf42 DevOps 2027" || list[0].File != "_db/DEVOPS_2027.csv" || list[0].Color != "#5F3202" {
		t.Fatalf("%+v", list)
	}
	if previousLineup("_db/OBSER_2027.csv") != "_db/OBSER_2026.csv" {
		t.Fatal("previous lineup")
	}
}

func TestAdminOnly(t *testing.T) {
	s := testServer(t, newFakeGH())
	if rec := do(t, s.Handler(), "GET", "/", nil, "", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no login: %d", rec.Code)
	}
	rec := do(t, s.Handler(), "GET", "/?b=llmday", nil, "", true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "LLMday London 2026 Q4") {
		t.Fatalf("board: %d", rec.Code)
	}
	// The navbar closes once: two copies of its script made two tab fills.
	if n := strings.Count(rec.Body.String(), "</header>"); n != 1 {
		t.Fatalf("navbar closed %d times", n)
	}
	// Every brand's tiles are on the page; only the chosen one shows.
	body := rec.Body.String()
	if !strings.Contains(body, `data-b="llmday">`) || !strings.Contains(body, `data-b="conf42" hidden`) {
		t.Fatal("brand grids missing or not hidden")
	}
}

func TestFeedErrorsHideTheAddress(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("city") == "Nowhere" {
			w.Write([]byte(`{"ok":false,"error":"notab"}`))
			return
		}
		http.Error(w, "boom", 500)
	}))
	defer ts.Close()
	addr := ts.URL + "/exec?key=sekret"
	if _, _, err := readFeed(addr, Event{Brand: "sreday", City: "Nowhere", Year: 2026, Quarter: 4}); err != errNoTab {
		t.Fatalf("notab: %v", err)
	}
	_, _, err := readFeed(addr, Event{Brand: "sreday", City: "Else", Year: 2026, Quarter: 4})
	if err == nil || strings.Contains(err.Error(), "sekret") || strings.Contains(err.Error(), ts.URL) {
		t.Fatalf("error leaks the address: %v", err)
	}
}

// TestPreview writes the board with made-up events and pushes to
// $SNORT_PREVIEW, for a look in a browser. Skipped otherwise.
func TestPreview(t *testing.T) {
	out := os.Getenv("SNORT_PREVIEW")
	if out == "" {
		t.Skip("set SNORT_PREVIEW")
	}
	s := testServer(t, newFakeGH())
	s.events["sreday"] = []Event{
		{ID: "sreday/2026-campinas-q4", Brand: "sreday", Name: "SREday Campinas 2026 Q4", Day: "2026-12-02", URL: "https://www.sreday.com/2026-campinas-q4/"},
		{ID: "sreday/2026-chennai-q4", Brand: "sreday", Name: "SREday Chennai 2026 Q4", Day: "2026-12-05", URL: "https://www.sreday.com/2026-chennai-q4/"},
		{ID: "sreday/2027-london-q1", Brand: "sreday", Name: "SREday London 2027 Q1", Day: "2027-03-18", URL: "https://www.sreday.com/2027-london-q1/"},
		{ID: "sreday/2026-amsterdam-q4", Brand: "sreday", Name: "SREday Amsterdam 2026 Q4", Day: "2026-10-30", URL: "https://www.sreday.com/2026-amsterdam-q4/"},
	}
	at := s.now()
	s.st.Pushes["sreday/2026-campinas-q4"] = &Push{At: at.Add(-2 * time.Hour), Result: "pushed", Repo: "sreday/sreday", Commit: "4f1c2e9ab3", Build: "live"}
	s.st.Pushes["sreday/2026-chennai-q4"] = &Push{At: at.Add(-5 * time.Minute), Result: "refused", Note: errNoTab.Error()}
	s.st.Pushes["sreday/2026-amsterdam-q4"] = &Push{At: at.Add(-26 * time.Hour), Result: "same"}
	html := do(t, s.Handler(), "GET", "/?b=sreday", nil, "", true).Body.String()
	html = strings.ReplaceAll(html, `"/assets/`, `"`+os.Getenv("SNORT_ASSETS")+`/`)
	if err := os.WriteFile(out, []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestConf42Push(t *testing.T) {
	gh := newFakeGH()
	header := []string{"Featured", "Track", "Name1", "JobTitle1", "Company1", "Title", "Abstract", "Picture"}
	gh.put("conf42/src", "_db/DEVOPS_2027.csv", writeCSV(header, [][]string{{"", "1", "Old Speaker", "SRE", "Acme", "Old", "old", "Old Speaker_devops.png"}}))
	s := testServer(t, gh)
	rows := [][]string{{"", "1", "Jane Example", "Engineer", "Example Co", "Graphs", "about graphs", "Jane Example_devops.png"}}
	s.feed = func(ev Event) ([]string, [][]string, error) {
		if ev.Tab != "DEVOPS_2027" {
			t.Fatalf("feed asked for %q", ev.Tab)
		}
		return header, rows, nil
	}
	if a := pushOf(t, s, "conf42/devops2027"); a.Result != "pushed" || !strings.Contains(a.URL, "github.com/conf42/src/commit/") {
		t.Fatalf("conf42 push: %+v", a)
	}
	if got := gh.files["conf42/src"]["_db/DEVOPS_2027.csv"]; !bytes.Equal(got, writeCSV(header, rows)) {
		t.Fatalf("committed: %q", got)
	}
	// A Conf42 event whose file isn't on the site yet is refused: its columns are unknown.
	delete(gh.files["conf42/src"], "_db/DEVOPS_2027.csv")
	if a := pushOf(t, s, "conf42/devops2027"); a.Result != "refused" || a.Note != errNoSiteFile.Error() {
		t.Fatalf("no file: %+v", a)
	}
}
