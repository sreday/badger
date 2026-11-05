package snoop

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// All people, companies and calls here are made up.

const geminiNotesHTML = `<html><head><style>.c1{color:red}</style></head><body>
<p class="title"><span>Partnership catch-up</span></p>
<p><span>Invited Alex Example Jordan Sample</span></p>
<h2><span>Summary</span></h2>
<p><span>Alex Example and Jordan Sample agreed on a booth at the spring event.</span></p>
<p><span>Pricing stays as quoted &amp; the logo goes on the website.</span></p>
<h2><span>Details</span></h2>
<ul><li><span>Booth: Jordan wants a corner spot.</span></li></ul>
<p><span>Suggested next steps</span></p>
<ul>
<li><span>&#9744; Alex Example will send the contract by Friday.</span></li>
<li><span>&#9744; Jordan Sample will send the logo.</span></li>
</ul>
<p><span>You should review Gemini's notes to make sure they're accurate.</span></p>
</body></html>`

func TestParseGeminiNotes(t *testing.T) {
	n := ParseGeminiNotes(geminiNotesHTML)
	wantSummary := "Alex Example and Jordan Sample agreed on a booth at the spring event.\n\nPricing stays as quoted & the logo goes on the website."
	if n.Summary != wantSummary {
		t.Errorf("summary = %q", n.Summary)
	}
	want := []string{"Alex Example will send the contract by Friday.", "Jordan Sample will send the logo."}
	if strings.Join(n.NextSteps, "|") != strings.Join(want, "|") {
		t.Errorf("next steps = %q", n.NextSteps)
	}
	if n.By != "Gemini in Meet" {
		t.Errorf("by = %q", n.By)
	}
}

func TestStatus(t *testing.T) {
	start := time.Date(2026, 11, 5, 14, 0, 0, 0, time.UTC)
	base := Call{Start: start, End: start.Add(30 * time.Minute), Owned: true}
	lines := []Line{{At: start, Speaker: "Alex", Text: "hi"}}
	cases := []struct {
		name string
		c    func(c *Call)
		now  time.Time
		want string
	}{
		{"upcoming", func(c *Call) {}, start.Add(-time.Hour), StatusUpcoming},
		{"live", func(c *Call) {}, start.Add(5 * time.Minute), StatusLive},
		{"owned, waiting", func(c *Call) {}, start.Add(time.Hour), StatusWaiting},
		{"owned, gave up", func(c *Call) {}, start.Add(5 * time.Hour), StatusNoTranscript},
		{"transcript, waiting for notes", func(c *Call) { c.APILines = lines }, start.Add(time.Hour), StatusWaiting},
		{"transcript only", func(c *Call) { c.APILines = lines }, start.Add(3 * time.Hour), StatusTranscript},
		{"done", func(c *Call) { c.APILines = lines; c.APINotes = &Notes{Summary: "s"} }, start.Add(time.Hour), StatusDone},
		{"not ours, nothing", func(c *Call) { c.Owned = false }, start.Add(time.Hour), StatusNoTranscript},
		{"not ours, extension done", func(c *Call) {
			c.Owned = false
			c.ExtLines, c.ExtNotes, c.ExtEnded = lines, &Notes{NextSteps: []string{"x"}}, true
		}, start.Add(time.Hour), StatusDone},
		{"cancelled", func(c *Call) { c.Cancelled = true }, start.Add(time.Hour), StatusCancelled},
		{"declined", func(c *Call) { c.Declined = true }, start.Add(time.Hour), StatusDeclined},
	}
	for _, tc := range cases {
		c := base
		tc.c(&c)
		if got, _ := c.Status(tc.now); got != tc.want {
			t.Errorf("%s: status = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestMergeLines(t *testing.T) {
	t0 := time.Date(2026, 11, 5, 14, 0, 0, 0, time.UTC)
	a := Line{At: t0, Speaker: "Alex", Text: "one"}
	b := Line{At: t0.Add(2 * time.Second), Speaker: "Jordan", Text: "two"}
	c := Line{At: t0.Add(time.Second), Speaker: "Alex", Text: "between"}
	got := mergeLines([]Line{a, b}, []Line{b, c, {At: t0, Speaker: "Alex", Text: "  "}})
	if len(got) != 3 || got[1].Text != "between" {
		t.Fatalf("merged = %+v", got)
	}
}

func TestSearch(t *testing.T) {
	calls := []Call{
		{ID: "1", Title: "Booth chat", APILines: []Line{{Speaker: "Alex", Text: "the corner spot"}}},
		{ID: "2", Title: "Venue", Attendees: []Attendee{{Email: "jordan@example.com"}}},
	}
	if r := Search(calls, "CORNER alex"); len(r) != 1 || r[0].ID != "1" {
		t.Errorf("search corner = %+v", r)
	}
	if r := Search(calls, "example.com"); len(r) != 1 || r[0].ID != "2" {
		t.Errorf("search email = %+v", r)
	}
	if r := Search(calls, ""); len(r) != 2 {
		t.Errorf("empty search = %d", len(r))
	}
}

func TestDocHTML(t *testing.T) {
	start := time.Date(2026, 11, 5, 14, 0, 0, 0, time.UTC)
	c := Call{
		Title: "Booth <b>chat</b>", MeetingCode: "abc-defg-hij", Start: start, End: start.Add(time.Hour),
		APILines: []Line{{At: start, Speaker: "Alex", Text: "a < b"}, {At: start, Speaker: "Alex", Text: "more"}},
		APINotes: &Notes{Summary: "S", NextSteps: []string{"first", "second"}, By: "Gemini in Meet"},
		Ticked:   map[int]bool{1: true},
	}
	h := DocHTML(&c, time.UTC, "https://snoop.example/admin/call/x")
	for _, want := range []string{"Booth &lt;b&gt;chat&lt;/b&gt;", "☐ first", "☑ second", "a &lt; b", "Meet transcript, 2 lines"} {
		if !strings.Contains(h, want) {
			t.Errorf("doc lacks %q", want)
		}
	}
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := ConfigFromEnv()
	cfg.Base = "/snoop"
	cfg.DataDir = t.TempDir()
	cfg.AdminPassword = "pw"
	cfg.PublicURL = "https://scan.example"
	cfg.ClientID, cfg.ClientSecret = "id", "secret"
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestIngest(t *testing.T) {
	s := newTestServer(t)
	start := time.Date(2026, 11, 5, 14, 0, 0, 0, time.UTC)
	s.store.Update("evt1", func(c *Call) {
		c.Title, c.MeetingCode, c.Start, c.End = "Booth chat", "abc-defg-hij", start, start.Add(30*time.Minute)
	})
	d := Device{Name: "Mac"}

	// Joined 3 minutes late: lands on the calendar trace.
	id, err := s.ingest(d, ingestReq{MeetingCode: "abc-defg-hij", StartedAt: start.Add(3 * time.Minute),
		Lines: []Line{{At: start.Add(4 * time.Minute), Speaker: "Alex", Text: "hello"}}})
	if err != nil || id != "evt1" {
		t.Fatalf("ingest = %q, %v", id, err)
	}
	// Resent after a blip, then the notes.
	s.ingest(d, ingestReq{MeetingCode: "abc-defg-hij", StartedAt: start.Add(3 * time.Minute),
		Lines: []Line{{At: start.Add(4 * time.Minute), Speaker: "Alex", Text: "hello"}},
		Ended: true, Notes: &Notes{Summary: "S", NextSteps: []string{"do it"}}})
	c, _ := s.store.Get("evt1")
	if len(c.ExtLines) != 1 || c.ExtNotes == nil || c.ExtNotes.By != "Chrome on-device AI" || !c.ExtEnded || c.ExtDevice != "Mac" {
		t.Fatalf("call = %+v", c)
	}

	// A link someone sent: a new ad-hoc trace.
	id, err = s.ingest(d, ingestReq{MeetingCode: "xyz-abcd-efg", Title: "Quick sync", StartedAt: start,
		Lines: []Line{{At: start.Add(20 * time.Minute), Speaker: "Jordan", Text: "bye"}}})
	if err != nil || !strings.HasPrefix(id, "ext_xyz-abcd-efg_") {
		t.Fatalf("ad-hoc ingest = %q, %v", id, err)
	}
	c, _ = s.store.Get(id)
	if c.Title != "Quick sync" || !c.End.Equal(start.Add(20*time.Minute)) {
		t.Errorf("ad-hoc call = %+v", c)
	}

	if _, err := s.ingest(d, ingestReq{MeetingCode: "../../etc", StartedAt: start}); err == nil {
		t.Error("bad meeting code accepted")
	}
}

func TestIngestNeedsToken(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	body := `{"meeting_code":"abc-defg-hij","started_at":"2026-11-05T14:00:00Z"}`
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/ingest", strings.NewReader(body)))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", w.Code)
	}
	token, _ := s.store.NewDevice("Mac")
	r := httptest.NewRequest("POST", "/ingest", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("with token: %d %s", w.Code, w.Body)
	}
}

func TestAdminNeedsPassword(t *testing.T) {
	s := newTestServer(t)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/admin", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("admin without password: %d", w.Code)
	}
	r := httptest.NewRequest("GET", "/admin", nil)
	r.SetBasicAuth("", "pw")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `href="/snoop/admin/actions"`) {
		t.Fatalf("admin: %d", w.Code)
	}
}

func TestOAuthState(t *testing.T) {
	s := newTestServer(t)
	st := s.oauthState(s.now())
	if !s.validState(st) {
		t.Error("fresh state rejected")
	}
	if s.validState(s.oauthState(s.now().Add(-time.Hour))) {
		t.Error("old state accepted")
	}
	if s.validState(st[:len(st)-1] + "0") {
		t.Error("forged state accepted")
	}
}

// fakeGoogle answers the handful of Google endpoints sync uses.
type fakeGoogle struct {
	mu      sync.Mutex
	patched []string
	docs    map[string]string // doc id -> HTML written
	start   time.Time
	owner   bool // Marek owns the Meet space
}

func (f *fakeGoogle) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	js := func(v any) { json.NewEncoder(w).Encode(v) }
	p := r.URL.Path
	switch {
	case p == "/token":
		js(map[string]any{"access_token": "at", "expires_in": 3600})
	case strings.HasPrefix(p, "/calendar/calendars/primary/events"):
		js(map[string]any{"items": []map[string]any{{
			"id": "evt1", "status": "confirmed", "summary": "Booth chat",
			"start":          map[string]any{"dateTime": f.start},
			"end":            map[string]any{"dateTime": f.start.Add(30 * time.Minute)},
			"organizer":      map[string]any{"email": "mark@sreday.com", "self": f.owner},
			"attendees":      []map[string]any{{"email": "mark@sreday.com", "self": true, "responseStatus": "accepted"}, {"email": "alex@example.com", "displayName": "Alex Example", "responseStatus": "accepted"}},
			"conferenceData": map[string]any{"conferenceId": "abc-defg-hij", "conferenceSolution": map[string]any{"key": map[string]any{"type": "hangoutsMeet"}}},
		}}})
	case p == "/meet/spaces/abc-defg-hij" && r.Method == "GET":
		js(map[string]any{"name": "spaces/S1", "meetingCode": "abc-defg-hij"})
	case p == "/meet/spaces/S1" && r.Method == "PATCH":
		if !f.owner {
			http.Error(w, `{"error":"PERMISSION_DENIED"}`, 403)
			return
		}
		f.patched = append(f.patched, r.URL.Query().Get("updateMask"))
		js(map[string]any{})
	case p == "/meet/conferenceRecords":
		if strings.Contains(r.URL.Query().Get("filter"), "start_time") {
			js(map[string]any{})
			return
		}
		js(map[string]any{"conferenceRecords": []map[string]any{{
			"name": "conferenceRecords/C1", "space": "spaces/S1",
			"startTime": f.start.Add(2 * time.Minute), "endTime": f.start.Add(28 * time.Minute)}}})
	case p == "/meet/conferenceRecords/C1/transcripts":
		js(map[string]any{"transcripts": []map[string]any{{"name": "conferenceRecords/C1/transcripts/T1", "state": "FILE_GENERATED",
			"docsDestination": map[string]any{"document": "documents/TDOC"}}}})
	case p == "/meet/conferenceRecords/C1/participants":
		js(map[string]any{"participants": []map[string]any{
			{"name": "conferenceRecords/C1/participants/P1", "signedinUser": map[string]any{"displayName": "Alex Example"}},
			{"name": "conferenceRecords/C1/participants/P2", "anonymousUser": map[string]any{"displayName": "Jordan Sample"}}}})
	case p == "/meet/conferenceRecords/C1/transcripts/T1/entries":
		js(map[string]any{"transcriptEntries": []map[string]any{
			{"participant": "conferenceRecords/C1/participants/P1", "text": "Shall we take the corner booth?", "startTime": f.start.Add(3 * time.Minute)},
			{"participant": "conferenceRecords/C1/participants/P2", "text": "Yes, send me the contract.", "startTime": f.start.Add(4 * time.Minute)}}})
	case p == "/meet/conferenceRecords/C1/smartNotes":
		js(map[string]any{"smartNotes": []map[string]any{{"name": "conferenceRecords/C1/smartNotes/N1", "state": "FILE_GENERATED",
			"docsDestination": map[string]any{"document": "documents/NDOC"}}}})
	case p == "/drive/files/NDOC/export":
		io.WriteString(w, geminiNotesHTML)
	case p == "/drive/files" && r.Method == "POST":
		js(map[string]any{"id": "FOLDER"})
	case p == "/upload/files" && r.Method == "POST":
		body, _ := io.ReadAll(r.Body)
		if !bytes.Contains(body, []byte(`"parents":["FOLDER"]`)) {
			http.Error(w, "no parent", 400)
			return
		}
		f.docs["DOC1"] = string(body)
		js(map[string]any{"id": "DOC1"})
	case strings.HasPrefix(p, "/upload/files/DOC1") && r.Method == "PATCH":
		body, _ := io.ReadAll(r.Body)
		f.docs["DOC1"] = string(body)
		js(map[string]any{"id": "DOC1"})
	default:
		http.Error(w, "fake Google: no "+r.Method+" "+p, 404)
	}
}

func withFakeGoogle(t *testing.T, f *fakeGoogle) {
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	old := []string{tokenURL, calendarAPI, meetAPI, driveAPI, uploadAPI}
	tokenURL, calendarAPI, meetAPI, driveAPI, uploadAPI = srv.URL+"/token", srv.URL+"/calendar", srv.URL+"/meet", srv.URL+"/drive", srv.URL+"/upload"
	t.Cleanup(func() { tokenURL, calendarAPI, meetAPI, driveAPI, uploadAPI = old[0], old[1], old[2], old[3], old[4] })
}

func TestSyncEndToEnd(t *testing.T) {
	start := time.Date(2026, 11, 5, 14, 0, 0, 0, time.UTC)
	f := &fakeGoogle{docs: map[string]string{}, start: start, owner: true}
	withFakeGoogle(t, f)
	s := newTestServer(t)
	s.google.tok = googleToken{RefreshToken: "rt", Email: "mark@sreday.com"}

	// Before the call: the trace appears and the Meet gets armed.
	s.now = func() time.Time { return start.Add(-time.Hour) }
	if err := s.syncOnce(); err != nil {
		t.Fatal(err)
	}
	c, ok := s.store.Get("evt1")
	if !ok || !c.Armed || len(f.patched) != 1 || !strings.Contains(f.patched[0], "smartNotesConfig") {
		t.Fatalf("before the call: %+v, patched %v", c, f.patched)
	}

	// After it: transcript, Gemini notes, Drive doc.
	s.now = func() time.Time { return start.Add(time.Hour) }
	if err := s.syncOnce(); err != nil {
		t.Fatal(err)
	}
	c, _ = s.store.Get("evt1")
	if st, why := c.Status(s.now()); st != StatusDone {
		t.Fatalf("status %q (%s), error %q", st, why, c.Error)
	}
	if len(c.APILines) != 2 || c.APILines[1].Speaker != "Jordan Sample" || c.TranscriptDoc != "TDOC" || c.NotesDoc != "NDOC" {
		t.Errorf("collected: %+v", c)
	}
	if c.DocID != "DOC1" || !strings.Contains(f.docs["DOC1"], "send me the contract") || !strings.Contains(f.docs["DOC1"], "☐ Alex Example will send the contract") {
		t.Errorf("doc %q: %s", c.DocID, f.docs["DOC1"])
	}

	// Ticking a step rewrites the same doc; an unchanged call isn't rewritten.
	s.store.Update("evt1", func(c *Call) { c.Ticked = map[int]bool{0: true} })
	f.docs["DOC1"] = ""
	s.syncOnce()
	if !strings.Contains(f.docs["DOC1"], "☑ Alex Example") {
		t.Errorf("tick not in doc: %q", f.docs["DOC1"])
	}
	f.docs["DOC1"] = "untouched"
	s.syncOnce()
	if f.docs["DOC1"] != "untouched" {
		t.Error("unchanged doc rewritten")
	}
}

func TestSyncNotOurs(t *testing.T) {
	start := time.Date(2026, 11, 5, 14, 0, 0, 0, time.UTC)
	f := &fakeGoogle{docs: map[string]string{}, start: start, owner: false}
	withFakeGoogle(t, f)
	s := newTestServer(t)
	s.google.tok = googleToken{RefreshToken: "rt", Email: "mark@sreday.com"}
	s.now = func() time.Time { return start.Add(-time.Hour) }
	if err := s.syncOnce(); err != nil {
		t.Fatal(err)
	}
	c, _ := s.store.Get("evt1")
	if c.Owned || c.Armed || len(f.patched) != 0 {
		t.Fatalf("someone else's Meet got armed: %+v", c)
	}
	if _, why := c.Status(s.now()); !strings.Contains(why, "Snoop extension") {
		t.Errorf("reason = %q", why)
	}
}
