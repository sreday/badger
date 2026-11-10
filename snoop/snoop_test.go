package snoop

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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

// Gemini's notes for a call with nothing to do (seen 2026-11-08): the
// placeholder and the footer links must not turn into tickable steps.
func TestParseGeminiNotesNoSteps(t *testing.T) {
	doc := `<html><body>
<p><span>Summary</span></p>
<p><span>Laptops and local AI hardware.</span></p>
<p><span>Suggested next steps</span></p>
<p><span>No suggested next steps were found for this meeting.</span></p>
<p><span>Want to see more? View the full notes</span></p>
<p><span>Tip: You can always access your full notes from the left sidebar.</span></p>
</body></html>`
	n := ParseGeminiNotes(doc)
	if len(n.NextSteps) != 0 {
		t.Errorf("next steps = %q, want none", n.NextSteps)
	}
	if n.Summary != "Laptops and local AI hardware." {
		t.Errorf("summary = %q", n.Summary)
	}
}

func TestParseCalendly(t *testing.T) {
	desc := "Event Name\nSponsor SREday, LLMday, PLATFORMday\n\nJoin in to discuss sponsorship.\n\nPowered by Calendly.com\n"
	if ok, name := ParseCalendly(desc); !ok || name != "Sponsor SREday, LLMday, PLATFORMday" {
		t.Errorf("calendly = %v %q", ok, name)
	}
	if ok, _ := ParseCalendly("This is a monthly catchup for us to discuss all things SREday"); ok {
		t.Error("a normal invite counted as Calendly")
	}
	if ok, name := ParseCalendly(""); ok || name != "" {
		t.Error("empty description counted as Calendly")
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
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("admin without password: %d", w.Code)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.SetBasicAuth("", "pw")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `id="actions"`) {
		t.Fatalf("admin: %d", w.Code)
	}
}

func TestAdminCalendlyTab(t *testing.T) {
	s := newTestServer(t)
	start := s.now().Add(time.Hour)
	s.store.Update("sales", func(c *Call) {
		c.Title, c.MeetingCode, c.Start, c.End = "Jake and Mark", "aaa-bbbb-ccc", start, start.Add(30*time.Minute)
		c.Calendly, c.CalendlyEvent = true, "Sponsor SREday"
	})
	s.store.Update("team", func(c *Call) {
		c.Title, c.MeetingCode, c.Start, c.End = "Ambassador Catchup", "ddd-eeee-fff", start, start.Add(time.Hour)
	})
	get := func(path string) string {
		r := httptest.NewRequest("GET", path, nil)
		r.SetBasicAuth("", "pw")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w.Body.String()
	}
	if b := get("/"); !strings.Contains(b, "Jake and Mark") || strings.Contains(b, "Ambassador Catchup") || !strings.Contains(b, `class="topic">Sponsor`) {
		t.Error("default tab should list only the Calendly booking")
	}
	s.store.Update("old", func(c *Call) {
		c.Title, c.MeetingCode, c.Start, c.End = "Old sales call", "ggg-hhhh-iii", start.Add(-49*time.Hour), start.Add(-48*time.Hour)
		c.Calendly, c.EventID = true, "ev-old"
	})
	if b := get("/"); !strings.Contains(b, "Old sales call") || strings.Index(b, "Old sales call") > strings.Index(b, "Jake and Mark") || !strings.Contains(b, ">Past<") || !strings.Contains(b, ">Upcoming<") {
		t.Error("Calendly tab: past should come first (left), upcoming second (right)")
	}
	if b := get("/?view=other"); strings.Contains(b, "Jake and Mark") || !strings.Contains(b, "Ambassador Catchup") {
		t.Error("other tab should list only the non-Calendly call")
	}
	s.store.Update("weekly1", func(c *Call) {
		c.Title, c.MeetingCode, c.Start, c.End, c.EventID = "Sheep & Horses", "jjj-kkkk-lll", start, start.Add(time.Hour), "w1"
	})
	s.store.Update("weekly2", func(c *Call) {
		c.Title, c.MeetingCode, c.Start, c.End, c.EventID = "Sheep & Horses", "jjj-kkkk-lll", start.Add(7*24*time.Hour), start.Add(7*24*time.Hour+time.Hour), "w2"
	})
	if b := get("/?view=recurring"); !strings.Contains(b, "Sheep &amp; Horses") || strings.Contains(b, "Ambassador Catchup") || strings.Contains(b, "Jake and Mark") {
		t.Error("recurring tab should list only the repeated meeting")
	}
	if b := get("/?view=other"); strings.Contains(b, "Sheep &amp; Horses") {
		t.Error("a recurring meeting also showed on Other")
	}
	// A Calendly booking that became a weekly series is recurring.
	s.store.Update("sales", func(c *Call) { c.Series = "weekly-sales" })
	if b := get("/?view=recurring"); !strings.Contains(b, "Jake and Mark") {
		t.Error("a repeating Calendly call should be on Recurring")
	}
	if b := get("/"); strings.Contains(b, "Jake and Mark") {
		t.Error("a repeating Calendly call also showed on Calendly")
	}
	s.store.Update("sales", func(c *Call) { c.Series = "" })
	b := get("/")
	// Upcoming calls get Join; past ones don't. Times carry the instant
	// for the browser's own zone.
	if !strings.Contains(b, `class="open join" href="https://meet.google.com/aaa-bbbb-ccc"`) || strings.Contains(b, "meet.google.com/ggg-hhhh-iii") {
		t.Error("Join should be on upcoming calls only")
	}
	if !strings.Contains(b, `<time datetime="`+start.UTC().Format(time.RFC3339)+`" data-local="when">`) {
		t.Error("call time not marked for the browser's zone")
	}
	// Search-as-you-type swaps #results: it must be on the page.
	if !strings.Contains(get("/?q=jake"), `id="results"`) {
		t.Error("no #results on a search")
	}
}

func TestRecurring(t *testing.T) {
	day := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	people := []Attendee{{Email: "mark@sreday.com"}, {Email: "Ana@example.com"}}
	calls := []Call{
		{ID: "a", EventID: "a", Title: "Sheep & Horses", Start: day, Attendees: people},
		{ID: "b", EventID: "b", Title: "sheep  & horses", Start: day.Add(7 * 24 * time.Hour), Attendees: []Attendee{people[1], people[0]}},
		{ID: "c", EventID: "c", Title: "Sheep & Horses", Start: day.Add(14 * 24 * time.Hour), Attendees: people[:1]}, // other people
		{ID: "d", EventID: "d_20261106", Series: "d", Title: "Weekly sync", Start: day},                              // one occurrence of a series
		{ID: "e", EventID: "e", Title: "One-off", Start: day},
		{ID: "f", EventID: "f", Title: "One-off", Start: day.Add(time.Hour)}, // same day twice is not recurring
		{ID: "meet_1", Title: "Meet abc", Start: day},
		{ID: "meet_2", Title: "Meet abc", Start: day.Add(24 * time.Hour)}, // ad-hoc: no invite to compare
	}
	got := Recurring(calls)
	for id, want := range map[string]bool{"a": true, "b": true, "c": false, "d": true, "e": false, "f": false, "meet_1": false, "meet_2": false} {
		if got[id] != want {
			t.Errorf("%s: recurring = %v, want %v", id, got[id], want)
		}
	}
}

func TestEmptyCall(t *testing.T) {
	start := time.Date(2026, 11, 5, 14, 0, 0, 0, time.UTC)
	later := start.Add(24 * time.Hour)
	call := func(texts ...string) Call {
		c := Call{EventID: "ev", Start: start, End: start.Add(30 * time.Minute), NoGemini: true}
		for _, s := range texts {
			c.APILines = append(c.APILines, Line{At: start, Speaker: "Alex", Text: s})
		}
		return c
	}
	if c := call("Hey, sorry, I gotta jump. Bye!", "Okay, no worries, bye, talk soon."); !c.Empty(later) {
		t.Error("small talk only should be empty")
	}
	real := call("We agreed the booth goes next to the coffee, the keynote moves to Thursday afternoon, pricing stays at fifteen thousand, " +
		"and the contract needs legal review before Friday so procurement can raise the purchase order and invoice the deposit in December.")
	if real.Empty(later) {
		t.Error("a real discussion counted as empty")
	}
	if c := call("hey, bye"); c.Empty(start.Add(10 * time.Minute)) {
		t.Error("a live call is never empty")
	}
	withNotes := call("hey, bye")
	withNotes.APINotes = &Notes{Summary: "Pricing agreed."}
	if withNotes.Empty(later) {
		t.Error("Gemini notes with a summary count as substance")
	}
	if c := (Call{EventID: "ev", Start: start, End: start.Add(30 * time.Minute)}); c.Empty(later) {
		t.Error("a calendar call with no transcript stays visible")
	}
	if c := (Call{Start: start, End: start.Add(5 * time.Minute)}); !c.Empty(later) {
		t.Error("an ad-hoc Meet with no transcript is empty")
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
	forged := st[:len(st)-1] + "0"
	if st[len(st)-1] == '0' {
		forged = st[:len(st)-1] + "1"
	}
	if s.validState(forged) {
		t.Error("forged state accepted")
	}
}

// fakeGoogle answers the handful of Google endpoints sync uses.
type fakeGoogle struct {
	mu      sync.Mutex
	patched []string
	docs    map[string]string // doc id -> HTML written
	start   time.Time
	owner   bool      // Marek owns the Meet space
	gemini  bool      // the account has Gemini notes
	email   string    // who signs in on Connect Google
	moved   time.Time // rebooked to this start, out of the listing window
	deleted bool      // the event is gone from the calendar
}

func (f *fakeGoogle) event(start time.Time, code string) map[string]any {
	return map[string]any{
		"id": "evt1", "status": "confirmed", "summary": "Booth chat",
		"start":          map[string]any{"dateTime": start},
		"end":            map[string]any{"dateTime": start.Add(30 * time.Minute)},
		"organizer":      map[string]any{"email": "mark@sreday.com", "self": f.owner},
		"attendees":      []map[string]any{{"email": "mark@sreday.com", "self": true, "responseStatus": "accepted"}, {"email": "alex@example.com", "displayName": "Alex Example", "responseStatus": "accepted"}},
		"conferenceData": map[string]any{"conferenceId": code, "conferenceSolution": map[string]any{"key": map[string]any{"type": "hangoutsMeet"}}},
	}
}

func (f *fakeGoogle) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	js := func(v any) { json.NewEncoder(w).Encode(v) }
	p := r.URL.Path
	switch {
	case p == "/token":
		js(map[string]any{"access_token": "at", "refresh_token": "rt-new", "expires_in": 3600})
	case p == "/userinfo":
		if r.Header.Get("Authorization") != "Bearer at" {
			http.Error(w, "no token", 401)
			return
		}
		js(map[string]any{"email": f.email})
	case p == "/calendar/calendars/primary/events/evt1":
		switch {
		case f.deleted:
			http.Error(w, "gone", 410)
		case !f.moved.IsZero():
			js(f.event(f.moved, "new-code-xyz"))
		default:
			js(f.event(f.start, "abc-defg-hij"))
		}
	case p == "/calendar/calendars/primary/events":
		if f.deleted || !f.moved.IsZero() {
			js(map[string]any{})
			return
		}
		js(map[string]any{"items": []map[string]any{f.event(f.start, "abc-defg-hij")}})
	case p == "/meet/spaces/abc-defg-hij" && r.Method == "GET":
		js(map[string]any{"name": "spaces/S1", "meetingCode": "abc-defg-hij"})
	case p == "/meet/spaces/S1" && r.Method == "PATCH":
		if !f.owner {
			http.Error(w, `{"error":"PERMISSION_DENIED"}`, 403)
			return
		}
		mask := r.URL.Query().Get("updateMask")
		if !f.gemini && strings.Contains(mask, "smartNotes") {
			http.Error(w, `{"error":{"code":403,"status":"PERMISSION_DENIED","details":[{"reason":"FEATURE_UNAVAILABLE_TO_USER"}]}}`, 403)
			return
		}
		f.patched = append(f.patched, mask)
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
	old := []string{tokenURL, calendarAPI, meetAPI, driveAPI, uploadAPI, userinfoAPI}
	tokenURL, calendarAPI, meetAPI, driveAPI, uploadAPI, userinfoAPI = srv.URL+"/token", srv.URL+"/calendar", srv.URL+"/meet", srv.URL+"/drive", srv.URL+"/upload", srv.URL+"/userinfo"
	t.Cleanup(func() {
		tokenURL, calendarAPI, meetAPI, driveAPI, uploadAPI, userinfoAPI = old[0], old[1], old[2], old[3], old[4], old[5]
	})
}

func TestSyncEndToEnd(t *testing.T) {
	start := time.Date(2026, 11, 5, 14, 0, 0, 0, time.UTC)
	f := &fakeGoogle{docs: map[string]string{}, start: start, owner: true, gemini: true}
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

// A call rebooked to another week (same event, new Meet link) leaves the
// listing window: its trace follows it instead of going "no transcript".
func TestSyncRebooked(t *testing.T) {
	start := time.Date(2026, 11, 5, 14, 0, 0, 0, time.UTC)
	f := &fakeGoogle{docs: map[string]string{}, start: start, owner: true, gemini: true}
	withFakeGoogle(t, f)
	s := newTestServer(t)
	s.google.tok = googleToken{RefreshToken: "rt", Email: "mark@sreday.com"}
	s.now = func() time.Time { return start.Add(-time.Hour) }
	if err := s.syncOnce(); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.store.Get("evt1"); !c.Armed {
		t.Fatalf("not armed: %+v", c)
	}

	f.moved = start.Add(14 * 24 * time.Hour)
	if err := s.syncOnce(); err != nil {
		t.Fatal(err)
	}
	c, _ := s.store.Get("evt1")
	if !c.Start.Equal(f.moved) || c.MeetingCode != "new-code-xyz" || c.Armed || c.Cancelled {
		t.Fatalf("after the rebooking: %+v", c)
	}
	if st, _ := c.Status(start.Add(time.Hour)); st != StatusUpcoming {
		t.Errorf("old slot passed: status %q, want upcoming", st)
	}

	// Deleted outright: the trace is cancelled.
	f2 := &fakeGoogle{docs: map[string]string{}, start: start, owner: true, gemini: true}
	withFakeGoogle(t, f2)
	s2 := newTestServer(t)
	s2.google.tok = googleToken{RefreshToken: "rt", Email: "mark@sreday.com"}
	s2.now = s.now
	s2.syncOnce()
	f2.deleted = true
	if err := s2.syncOnce(); err != nil {
		t.Fatal(err)
	}
	if c, _ := s2.store.Get("evt1"); !c.Cancelled {
		t.Errorf("deleted event not cancelled: %+v", c)
	}
}

// Connect Google on a fresh Snoop: nothing saved yet when the email is asked.
func TestConnectGoogle(t *testing.T) {
	f := &fakeGoogle{docs: map[string]string{}, email: "someone@example.com"}
	withFakeGoogle(t, f)
	s := newTestServer(t)

	if _, err := s.google.Exchange("code", s.redirectURI(), "mark@sreday.com"); err == nil || !strings.Contains(err.Error(), "signed in as someone@example.com") {
		t.Fatalf("wrong account: %v", err)
	}
	if s.google.Connected() != "" {
		t.Fatal("wrong account got saved")
	}

	f.email = "Mark@sreday.com"
	email, err := s.google.Exchange("code", s.redirectURI(), "mark@sreday.com")
	if err != nil || s.google.Connected() != "Mark@sreday.com" {
		t.Fatalf("connect: %q, %v", email, err)
	}
	reopened, err := NewGoogle("", "", s.cfg.DataDir)
	if err != nil || reopened.tok.RefreshToken != "rt-new" {
		t.Fatalf("token not saved: %+v %v", reopened.tok, err)
	}
}

// No Gemini on the account: transcripts still get switched on, and the
// call doesn't wait for Gemini notes that will never come.
func TestArmWithoutGemini(t *testing.T) {
	start := time.Date(2026, 11, 5, 14, 0, 0, 0, time.UTC)
	f := &fakeGoogle{docs: map[string]string{}, start: start, owner: true}
	withFakeGoogle(t, f)
	s := newTestServer(t)
	s.google.tok = googleToken{RefreshToken: "rt", Email: "mark@sreday.com"}
	s.now = func() time.Time { return start.Add(-time.Hour) }
	if err := s.syncOnce(); err != nil {
		t.Fatal(err)
	}
	c, _ := s.store.Get("evt1")
	if !c.Armed || !c.NoGemini || c.ArmError != "" || len(f.patched) != 1 || strings.Contains(f.patched[0], "smartNotes") {
		t.Fatalf("armed %v, no gemini %v, error %q, patched %v", c.Armed, c.NoGemini, c.ArmError, f.patched)
	}
	if _, why := c.Status(s.now()); !strings.Contains(why, "no Gemini notes") {
		t.Errorf("reason = %q", why)
	}
	c.APILines = []Line{{At: start, Speaker: "Alex", Text: "hi"}}
	if st, _ := c.Status(start.Add(time.Hour)); st != StatusTranscript {
		t.Errorf("status = %q, want transcript only straight away", st)
	}

	// The Workspace upgrade lands: the next try after geminiRetry switches
	// Gemini notes on, and not before.
	f.gemini = true
	s.now = func() time.Time { return start.Add(-time.Hour + 10*time.Minute) }
	s.syncOnce()
	if c, _ := s.store.Get("evt1"); !c.NoGemini {
		t.Fatal("retried before geminiRetry")
	}
	s.now = func() time.Time { return start.Add(-time.Hour + geminiRetry) }
	s.syncOnce()
	c, _ = s.store.Get("evt1")
	if c.NoGemini || !c.Armed || !strings.Contains(f.patched[len(f.patched)-1], "smartNotes") {
		t.Fatalf("after the upgrade: no gemini %v, patched %v", c.NoGemini, f.patched)
	}
}

func pillText(ps []Pill) string {
	var out []string
	for _, p := range ps {
		if p.Brand != "" {
			out = append(out, p.Text+"("+p.Brand+")")
		} else {
			out = append(out, p.Text)
		}
	}
	return strings.Join(out, "|")
}

func TestPills(t *testing.T) {
	desc := "Event Name\nSponsor SREday, LLMday, PLATFORMday\n\nYour Company: Antler/AITX\n\nWe meet to discuss: SREday\n\nArea of interest: US\n\nLooking to sponsor, or host an event?: Host\n\nYour main goals?:\nAttendees List\nBrand Exposure\n\nYour budget range?: No budget\n\nPowered by Calendly.com\n"
	c := Call{Title: "Jake O'Shea and Mark Pawlikowski", Calendly: true, CalendlyEvent: "Sponsor SREday, LLMday, PLATFORMday", CalendlyAnswers: ParseCalendlyAnswers(desc)}
	if got := pillText(c.Pills()); got != "Hosting in US(sreday)|$0" {
		t.Errorf("booking only: %q", got)
	}
	if c.Company() != "Antler/AITX" {
		t.Errorf("company = %q", c.Company())
	}
	c.APINotes = &Notes{Summary: "Jake can host SREday in Toronto at the Antler office, 120 people. Also interested in a booth at SREday NYC."}
	if got := pillText(c.Pills()); got != "Hosting in Toronto(sreday)|$0|Booth" {
		t.Errorf("with notes: %q", got)
	}
	sid := Call{CalendlyAnswers: map[string]string{qIntent: "Sponsor", qDiscuss: "SREday", qBudget: "$5.000 - $10.000", qCompany: "Autoheal AI"}}
	if got := pillText(sid.Pills()); got != "Sponsor SREday(sreday)|$5K-$10K" {
		t.Errorf("budget first after intent: %q", got)
	}
	s := Call{Title: "William and Mark", APINotes: &Notes{Summary: "William wants to sponsor PLATFORMday in NYC with a keynote and swag."}}
	if got := pillText(s.Pills()); got != "Sponsor PLATFORMday NYC(platformday)|Keynote|Logo + swag" {
		t.Errorf("sponsor: %q", got)
	}
	l := Call{Title: "LLMday chat", APINotes: &Notes{Summary: "Talked about LLMday London."}}
	if got := pillText(l.Pills()); got != "LLMday London(llmday)" {
		t.Errorf("llmday: %q", got)
	}
	pec := Call{Title: "PEC London", APINotes: &Notes{Summary: "Sponsoring the Prompt Engineering Conference in London."}}
	if got := pillText(pec.Pills()); got != "Sponsor PEC London(pec)" {
		t.Errorf("pec: %q", got)
	}
	in := Call{Title: "Catching up", Attendees: []Attendee{{Email: "miko@sreday.com"}, {Email: "mark@sreday.com"}}}
	if got := pillText(in.Pills()); got != "Internal" || in.Company() != "" {
		t.Errorf("internal: %q, company %q", got, in.Company())
	}
	m := Call{Title: "Marius Zaharia and Mark Pawlikowski", Attendees: []Attendee{{Email: "mark@sreday.com"}, {Email: "marius.zaharia@socgen.com"}}}
	if got := pillText(m.Pills()); got != "1:1" || m.Company() != "Socgen" {
		t.Errorf("company from email: pills %q, company %q", got, m.Company())
	}
	if got := pillText((&Call{Title: "SRE & LLM - Ambassador Catchup"}).Pills()); got != "Ambassadors|Catch-up" {
		t.Errorf("title fallback: %q", got)
	}
	if n := len((&Call{APINotes: &Notes{Summary: "booth keynote workshop meetup hackathon swag t-shirts sponsor sreday"}}).Pills()); n > 5 {
		t.Errorf("%d pills, want at most 5", n)
	}
	for in, want := range map[string]string{"No budget": "$0", "$5.000 - $10.000": "$5K-$10K", "$1.000 - $3.000": "$1K-$3K",
		"More than $20.000": "$20K+", "$500 - $1.000": "$500-$1K", "Less than $1.000": "<$1K", "$20k+": "$20K+", "$1,500 - $2,500": "$1.5K-$2.5K"} {
		if got := budgetPill(in); got != want {
			t.Errorf("budgetPill(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMissedActions(t *testing.T) {
	s := newTestServer(t)
	now := s.now()
	mk := func(id string, start time.Time, calendly, resolved bool) {
		s.store.Update(id, func(c *Call) {
			c.Title, c.MeetingCode, c.Start, c.End, c.EventID = id, "aaa-bbbb-"+id, start, start.Add(30*time.Minute), id
			c.Calendly, c.Resolved = calendly, resolved
		})
	}
	mk("old", now.Add(-72*time.Hour), true, false)
	mk("new", now.Add(-48*time.Hour), true, false)
	mk("done", now.Add(-24*time.Hour), true, true)
	mk("team", now.Add(-48*time.Hour), false, false) // not a sales call
	mk("soon", now.Add(time.Hour), true, false)      // not over yet
	s.store.Update("notes", func(c *Call) {
		c.Title, c.Start, c.End = "notes", now.Add(-5*time.Hour), now.Add(-4*time.Hour)
		c.APINotes = &Notes{Summary: "x", NextSteps: []string{"Send the deck"}}
		c.APILines = []Line{{At: now.Add(-5 * time.Hour), Speaker: "A", Text: "hi"}}
	})
	ids := func() string {
		open, resolved := s.openActions(now)
		var o, r []string
		for _, a := range open {
			o = append(o, a.Call.ID)
		}
		for _, a := range resolved {
			r = append(r, a.Call.ID)
		}
		return strings.Join(o, ",") + " / " + strings.Join(r, ",")
	}
	if got := ids(); got != "new,old,notes / done" {
		t.Fatalf("actions = %q, want missed (latest first), steps / resolved", got)
	}
	r := httptest.NewRequest("POST", "/admin/call/new/resolved", strings.NewReader("done=1"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Requested-With", "fetch")
	r.SetBasicAuth("", "pw")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if c, _ := s.store.Get("new"); w.Code != 204 || !c.Resolved {
		t.Fatalf("resolve: %d, %+v", w.Code, c.Resolved)
	}
	if got := ids(); got != "old,notes / done,new" {
		t.Errorf("after resolving: %q", got)
	}
	// A ticked next step moves to Resolved too.
	s.store.Update("notes", func(c *Call) { c.Ticked = map[int]bool{0: true} })
	if got := ids(); got != "old / notes,done,new" {
		t.Errorf("after ticking the step: %q", got)
	}
	r = httptest.NewRequest("GET", "/", nil)
	r.SetBasicAuth("", "pw")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	b := w.Body.String()
	if i := strings.Index(b, `id="act-resolved"`); i < 0 || !strings.Contains(b[i:strings.Index(b, `id="act-open"`)], "/admin/call/new/resolved") {
		t.Errorf("a resolved call should sit in the Resolved pane: %d %s", w.Code, b[max(0, len(b)-3000):])
	}
}

func TestOthersDeclined(t *testing.T) {
	s := newTestServer(t)
	start := s.now().Add(-2 * time.Hour)
	ev := func(id string, resp ...string) *CalEvent {
		e := &CalEvent{ID: id, Status: "confirmed", Summary: id, HangoutLink: "https://meet.google.com/abc-defg-hij"}
		e.Start.DateTime, e.End.DateTime = start, start.Add(30*time.Minute)
		e.Attendees = append(e.Attendees, calAttendee{Email: "mark@sreday.com", Self: true, ResponseStatus: "accepted"})
		for i, r := range resp {
			e.Attendees = append(e.Attendees, calAttendee{Email: "g" + strconv.Itoa(i) + "@x.com", ResponseStatus: r})
		}
		return e
	}
	for _, tc := range []struct {
		id   string
		resp []string
		off  bool
	}{
		{"one-declined-others-coming", []string{"declined", "accepted"}, false},
		{"one-declined-other-unanswered", []string{"declined", "needsAction"}, false},
		{"all-declined", []string{"declined", "declined"}, true},
		{"1to1-declined", []string{"declined"}, true},
	} {
		c, err := s.traceEvent(ev(tc.id, tc.resp...))
		if err != nil {
			t.Fatal(err)
		}
		if st, _ := c.Status(s.now()); (st == StatusDeclined) != tc.off || c.Cancelled {
			t.Errorf("%s: status %q, cancelled %v", tc.id, st, c.Cancelled)
		}
	}
}

func TestWalkingAway(t *testing.T) {
	s := newTestServer(t)
	now := s.now()
	mk := func(id, guest string, start time.Time, f func(c *Call)) {
		s.store.Update(id, func(c *Call) {
			c.Title, c.MeetingCode, c.Start, c.End, c.EventID, c.Calendly = id, "aaa-bbbb-"+id, start, start.Add(30*time.Minute), id, true
			c.Attendees = []Attendee{{Email: "mark@sreday.com", Response: "accepted"}, {Email: guest, Response: "declined"}}
			c.OthersDeclined = true
			if f != nil {
				f(c)
			}
		})
	}
	mk("gone", "ana@a.com", now.Add(-24*time.Hour), nil)
	mk("rebooked", "bo@b.com", now.Add(-24*time.Hour), nil)
	s.store.Update("rebooked-new", func(c *Call) {
		c.Title, c.Start, c.End, c.EventID = "new", now.Add(48*time.Hour), now.Add(49*time.Hour), "rebooked-new"
		c.Attendees = []Attendee{{Email: "mark@sreday.com"}, {Email: "Bo@b.com", Response: "accepted"}}
	})
	mk("team", "cy@c.com", now.Add(-24*time.Hour), func(c *Call) { c.Calendly = false })
	open, _ := s.openActions(now)
	var got []string
	for _, a := range open {
		got = append(got, a.Call.ID+": "+a.Text)
	}
	if len(got) != 1 || !strings.HasPrefix(got[0], "gone: ") || !strings.Contains(got[0], "about to walk away, share the offer") {
		t.Errorf("walk-away actions = %q", got)
	}
}

func TestRelDay(t *testing.T) {
	tz, _ := time.LoadLocation("Europe/London")
	now := time.Date(2026, 11, 9, 23, 0, 0, 0, tz)
	for d, want := range map[time.Duration]string{0: "today", 2 * time.Hour: "tomorrow", -24 * time.Hour: "yesterday", -72 * time.Hour: "3 days ago", 14 * 24 * time.Hour: "in 14 days"} {
		if got := relDay(now.Add(d), now, tz); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
	}
}
