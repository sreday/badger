package snap

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

var team = []string{"sreday.com", "conf42.com"}

func TestNewTextDropsQuotesAndSignature(t *testing.T) {
	in := "Hi Mark,\r\nWe'll think about it and get back to you.\r\n\r\n-- \r\nJane\r\nVP Marketing\r\n\r\nOn Mon, 2 Nov 2026 at 10:00, Mark <mark@sreday.com> wrote:\r\n> Would you like a booth?\r\n"
	got := newText(in)
	if got != "Hi Mark,\nWe'll think about it and get back to you." {
		t.Fatalf("newText = %q", got)
	}
	wrapped := "Sounds good.\n\nOn Tue, Nov 3, 2026 at 9:12 AM Mark Pawlikowski <\nmark@sreday.com> wrote:\n\n> earlier"
	if got := newText(wrapped); got != "Sounds good." {
		t.Fatalf("wrapped On..wrote: %q", got)
	}
	outlook := "Yes please.\n\n________________________________\nFrom: Mark\nSent: Monday"
	if got := newText(outlook); got != "Yes please." {
		t.Fatalf("outlook: %q", got)
	}
}

func TestAutomated(t *testing.T) {
	cases := []struct {
		h    map[string]string
		from string
		want bool
	}{
		{map[string]string{}, "jane@acme.com", false},
		{map[string]string{"list-unsubscribe": "<mailto:x>"}, "jane@acme.com", true},
		{map[string]string{"precedence": "bulk"}, "jane@acme.com", true},
		{map[string]string{"auto-submitted": "auto-replied"}, "jane@acme.com", true},
		{map[string]string{"auto-submitted": "no"}, "jane@acme.com", false},
		{map[string]string{}, "noreply@acme.com", true},
		{map[string]string{}, "calendar-notification@google.com", true},
		{map[string]string{}, "news@acme.com", true},
		{map[string]string{}, "newton@acme.com", false},
	}
	for _, c := range cases {
		if got := automated(c.h, c.from); got != c.want {
			t.Errorf("automated(%v, %s) = %v, want %v", c.h, c.from, got, c.want)
		}
	}
}

func mailFrom(name, addr string, at time.Time, me bool, text string) Mail {
	m := Mail{FromMe: me, At: at, Snippet: text, Text: text}
	m.From.Name, m.From.Address = name, addr
	if me {
		m.From.Address = "mark@sreday.com"
		m.To = append(m.To, m.From)
		m.To[0].Name, m.To[0].Address = name, addr
	}
	return m
}

func TestLookWindows(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	old := now.AddDate(0, -3, 0)
	speaker := &ParsedThread{ID: "a", Subject: "Your talk at SREday", Mails: []Mail{
		mailFrom("Ann", "ann@corp.io", old, true, "Here are the details"),
		mailFrom("Ann", "ann@corp.io", old.Add(time.Hour), false, "Thanks, slides soon"),
	}}
	if m := Look(speaker, now, team, nil); m.Skip != "older than 30 days" {
		t.Fatalf("3-month-old speaker thread: skip %q", m.Skip)
	}
	sponsor := &ParsedThread{ID: "b", Subject: "SREday NYC sponsorship", Mails: []Mail{
		mailFrom("Jane", "jane@acme.com", old, true, "Here's the prospectus"),
		mailFrom("Jane", "jane@acme.com", old.Add(time.Hour), false, "We'll think about it"),
	}}
	m := Look(sponsor, now, team, nil)
	if m.Skip != "" || !m.Sponsor || m.Waiting != "you" || m.LastFrom != "Jane" {
		t.Fatalf("3-month-old sponsor thread: %+v", m)
	}
	// A sponsor's domain counts even without the words.
	quiet := &ParsedThread{ID: "c", Subject: "Coffee?", Mails: []Mail{mailFrom("Bo", "bo@harness.io", old, false, "Coffee next week?")}}
	if m := Look(quiet, now, team, map[string]bool{"harness.io": true}); m.Skip != "" || !m.Sponsor {
		t.Fatalf("sponsor domain: %+v", m)
	}
	yearOld := &ParsedThread{ID: "d", Subject: "Sponsorship", Mails: []Mail{mailFrom("Jo", "jo@x.com", now.AddDate(-1, 0, -2), false, "budget?")}}
	if m := Look(yearOld, now, team, nil); m.Skip != "older than a year" {
		t.Fatalf("year-old: %q", m.Skip)
	}
	// Mark's cold email nobody answered, and robots, aren't loose ends.
	cold := &ParsedThread{ID: "e", Subject: "Sponsor SREday?", Mails: []Mail{mailFrom("Kim", "kim@y.com", now, true, "Hey Kim")}}
	if m := Look(cold, now, team, nil); m.Skip != "no reply from a person" {
		t.Fatalf("cold outreach: %q", m.Skip)
	}
	robot := mailFrom("Luma", "noreply@luma-mail.com", now, false, "New registration")
	robot.Automated = true
	if m := Look(&ParsedThread{ID: "f", Subject: "x", Mails: []Mail{robot}}, now, team, nil); m.Skip != "no reply from a person" {
		t.Fatalf("robot: %q", m.Skip)
	}
	internal := &ParsedThread{ID: "g", Subject: "Venue for Q1", Mails: []Mail{mailFrom("Anna", "anna@sreday.com", now, false, "Can you check?")}}
	if m := Look(internal, now, team, nil); !m.Internal {
		t.Fatalf("internal: %+v", m)
	}
}

func TestRulesSpeakerCancel(t *testing.T) {
	now := time.Now()
	p := &ParsedThread{ID: "a", Subject: "My talk at SREday London", Mails: []Mail{
		mailFrom("Ann Lee", "ann@corp.io", now, false, "Sorry, I have to cancel my talk."),
	}}
	a := Rules(p, Look(p, now, team, nil))
	if a.Category != "speakers" || a.CancelName != "Ann Lee" || !a.Loose {
		t.Fatalf("rules: %+v", a)
	}
}

func TestStillListed(t *testing.T) {
	ls := []Lineup{
		{Repo: "sreday/sreday", Dir: "2026-nyc-q4", Brand: "SREday", City: "NYC", Start: time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC),
			Talks: []Talk{{Names: []string{"José O'Neil"}, Status: "talk", Title: "Pagers"}}},
		{Repo: "sreday/sreday", Dir: "2026-london-q4", Brand: "SREday", City: "London",
			Talks: []Talk{{Names: []string{"Jose Maria ONeil"}, Status: "keynote"}}},
	}
	if got := StillListed(ls, "Jose ONeil", "SREday London"); !strings.Contains(got, "2026-london-q4") {
		t.Fatalf("named event first: %q", got)
	}
	if got := StillListed(ls, "josé o'neil", ""); !strings.Contains(got, "2026-nyc-q4") || !strings.Contains(got, "Pagers") {
		t.Fatalf("any lineup: %q", got)
	}
	if got := StillListed(ls, "Ann Lee", "SREday NYC"); got != "" {
		t.Fatalf("not listed: %q", got)
	}
	csvData := "\xef\xbb\xbf,status,name,organization,title\n,talk,Ada L & Grace H,X,T1\n,draft talk,Bob,Y,T2\n,noshow,Cy,Z,T3\n"
	talks := parseTalks([]byte(csvData))
	if len(talks) != 1 || len(talks[0].Names) != 2 || talks[0].Names[1] != "Grace H" {
		t.Fatalf("parseTalks: %+v", talks)
	}
}

func TestExpireAndLater(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	th := &Thread{Category: "speakers", LastAt: now.AddDate(0, 0, -40), AnalyzedHistory: "1", HistoryID: "1", Loose: true}
	expire(th, now)
	if th.Skip == "" {
		t.Fatal("40-day-old speaker thread should step aside")
	}
	host := &Thread{Category: "sponsors", LastAt: now.AddDate(0, -5, 0), Loose: true, FollowUp: "2027-04-01", AnalyzedHistory: "1", HistoryID: "1"}
	expire(host, now)
	if host.Skip != "" || !host.Later(now) || host.Open(now) {
		t.Fatalf("touch base in 6 months: skip %q later %v open %v", host.Skip, host.Later(now), host.Open(now))
	}
	spring := time.Date(2027, 4, 2, 0, 0, 0, 0, time.UTC)
	if !host.Open(spring) || !host.Due(spring) {
		t.Fatal("due in April")
	}
}

func TestBatches(t *testing.T) {
	ex := []string{strings.Repeat("a", 10), strings.Repeat("b", 10), strings.Repeat("c", 30), "d", "e", "f", "g"}
	got := batches(ex, 3, 25)
	want := "[[0 1] [2] [3 4 5] [6]]"
	if fmt.Sprint(got) != want {
		t.Fatalf("batches = %v, want %s", got, want)
	}
}

// --- A whole run, against a fake Gmail and a fake Claude -------------------

type fakeMsg struct {
	from, to, subject string
	at                time.Time
	sent              bool
	body              string
	headers           map[string]string
}

func fakeGmail(t *testing.T, threads map[string][]fakeMsg, history map[string]string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no", 401)
			return
		}
		if r.URL.Path == "/threads" {
			var refs []map[string]string
			for id := range threads {
				refs = append(refs, map[string]string{"id": id, "historyId": history[id]})
			}
			json.NewEncoder(w).Encode(map[string]any{"threads": refs})
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/threads/")
		msgs, ok := threads[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		full := r.URL.Query().Get("format") == "full"
		var out []map[string]any
		for i, m := range msgs {
			hs := []map[string]string{{"name": "From", "value": m.from}, {"name": "To", "value": m.to}, {"name": "Subject", "value": m.subject}}
			for k, v := range m.headers {
				hs = append(hs, map[string]string{"name": k, "value": v})
			}
			payload := map[string]any{"mimeType": "text/plain", "headers": hs}
			if full {
				payload["body"] = map[string]string{"data": base64.URLEncoding.EncodeToString([]byte(m.body))}
			}
			labels := []string{"INBOX"}
			if m.sent {
				labels = []string{"SENT"}
			}
			out = append(out, map[string]any{"id": fmt.Sprint(id, i), "labelIds": labels, "snippet": m.body,
				"internalDate": strconv.FormatInt(m.at.UnixMilli(), 10), "payload": payload})
		}
		json.NewEncoder(w).Encode(map[string]any{"id": id, "historyId": history[id], "messages": out})
	}))
}

type fakeClaude struct {
	seen  []string
	calls int
}

func (f *fakeClaude) Model() string { return "claude-haiku-5-5" }

func (f *fakeClaude) Analyze(ctx context.Context, today string, ex []string) ([]Analysis, Usage, error) {
	f.calls++
	var out []Analysis
	for _, e := range ex {
		f.seen = append(f.seen, e)
		id := e[strings.Index(e, `id="`)+4:]
		id = id[:strings.Index(id, `"`)]
		a := Analysis{ID: id, Category: "others", Summary: "s", Priority: "low"}
		switch {
		case strings.Contains(e, "think about it"):
			a.Category, a.Loose, a.Label, a.Waiting = "sponsors", true, "Will think about it", "them"
			a.Actions = []Action{{Title: "Nudge Jane", Prompt: "Draft a nudge to Jane."}}
		case strings.Contains(e, "cancel"):
			a.Category, a.Loose, a.Label, a.CancelName, a.CancelEvent = "speakers", true, "Speaker cancelled", "Ann Lee", "SREday NYC"
		}
		out = append(out, a)
	}
	return out, Usage{Requests: 1, Input: 1000, Output: 200, Threads: len(ex)}, nil
}

func TestSyncEndToEnd(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	threads := map[string][]fakeMsg{
		"spons": {
			{from: "Mark <mark@sreday.com>", to: "Jane <jane@acme.com>", subject: "SREday NYC sponsorship", at: now.AddDate(0, -2, 0), sent: true, body: "Here's the prospectus."},
			{from: "Jane <jane@acme.com>", to: "mark@sreday.com", subject: "Re: SREday NYC sponsorship", at: now.AddDate(0, -2, 1), body: "We'll think about it.\n\nOn Mon, Sep 1, 2026 at 9:00 Mark <mark@sreday.com> wrote:\n> Here's the prospectus."},
		},
		"cancel": {
			{from: "Ann Lee <ann@corp.io>", to: "mark@sreday.com", subject: "My talk", at: now.AddDate(0, 0, -3), body: "Sorry, I need to cancel my talk at SREday NYC."},
		},
		"news": {
			{from: "Weekly <news@vendor.com>", to: "mark@sreday.com", subject: "This week", at: now, body: "big news", headers: map[string]string{"List-Unsubscribe": "<x>"}},
		},
		"oldspeaker": {
			{from: "Bo <bo@x.io>", to: "mark@sreday.com", subject: "Slides for my talk", at: now.AddDate(0, -2, 0), body: "Here are my slides, can you check?"},
		},
	}
	history := map[string]string{"spons": "10", "cancel": "20", "news": "30", "oldspeaker": "40"}
	gm := fakeGmail(t, threads, history)
	defer gm.Close()

	s, err := New(Config{Base: "/snap", DataDir: t.TempDir(), AdminPassword: "pw", Account: "mark@sreday.com", Model: "claude-haiku-5-5",
		Every: 6 * time.Hour, MaxPerRun: 100, TeamDomains: team, TZ: "Europe/London"})
	if err != nil {
		t.Fatal(err)
	}
	s.gmail = NewGmail(func() (string, error) { return "tok", nil })
	s.gmail.API = gm.URL
	fc := &fakeClaude{}
	s.analyzer = fc
	s.now = func() time.Time { return now }
	s.spDomsAt = now // no GitHub in tests
	s.lineups.at = now
	s.lineups.list = []Lineup{{Repo: "sreday/sreday", Dir: "2026-nyc-q4", Brand: "SREday", City: "NYC", Talks: []Talk{{Names: []string{"Ann Lee"}, Status: "talk", Title: "Pagers"}}}}

	run := s.Sync(context.Background())
	if run.Error != "" || run.Listed != 4 || run.Changed != 4 || run.Read != 2 {
		t.Fatalf("run: %+v", run)
	}
	// Only the two live threads reached Claude, quotes stripped.
	if len(fc.seen) != 2 {
		t.Fatalf("Claude saw %d threads", len(fc.seen))
	}
	for _, e := range fc.seen {
		if strings.Contains(e, "> Here's the prospectus") {
			t.Fatalf("quoted text reached Claude: %s", e)
		}
	}
	sp, _ := s.store.Get("spons")
	if !sp.Open(now) || sp.Category != "sponsors" || sp.Label != "Will think about it" || len(sp.Actions) != 1 {
		t.Fatalf("sponsor thread: %+v", sp)
	}
	ca, _ := s.store.Get("cancel")
	if !strings.Contains(ca.StillListed, "2026-nyc-q4") || !ca.Open(now) {
		t.Fatalf("cancel thread: %+v", ca)
	}
	if n, _ := s.store.Get("news"); n.Skip == "" {
		t.Fatal("newsletter kept")
	}
	if o, _ := s.store.Get("oldspeaker"); o.Skip != "older than 30 days" {
		t.Fatalf("2-month-old speaker thread: %q", o.Skip)
	}

	// Nothing changed: the next run costs no tokens.
	calls := fc.calls
	run = s.Sync(context.Background())
	if fc.calls != calls || run.Changed != 0 || run.Read != 0 {
		t.Fatalf("second run re-read: %+v", run)
	}

	// The lineup drops Ann: the cancellation is handled.
	s.lineups.list = []Lineup{{Repo: "sreday/sreday", Dir: "2026-nyc-q4", City: "NYC"}}
	s.Sync(context.Background())
	if ca, _ := s.store.Get("cancel"); ca.IsLoose() {
		t.Fatalf("cancel still loose after the lineup changed: %+v", ca)
	}

	// The page: auth, the tabs, the thread, the copy prompt.
	h := s.Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 401 {
		t.Fatalf("no password: %d", rec.Code)
	}
	req := httptest.NewRequest("GET", "/?view=sponsors", nil)
	req.SetBasicAuth("", "pw")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	for _, want := range []string{"SREday NYC sponsorship", "Will think about it", "Copy for Claude", "Draft a nudge to Jane.", "thread id spons", "Sponsors<span class=\"n\">1</span>"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}

	// Done hides it; a new message brings it back.
	req = httptest.NewRequest("POST", "/admin/thread/spons/done", strings.NewReader(url.Values{"done": {"1"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Requested-With", "fetch")
	req.SetBasicAuth("", "pw")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if sp, _ := s.store.Get("spons"); rec.Code != 204 || sp.Open(now) {
		t.Fatalf("done: %d %+v", rec.Code, sp)
	}
	threads["spons"] = append(threads["spons"], fakeMsg{from: "Jane <jane@acme.com>", to: "mark@sreday.com", subject: "Re: SREday NYC sponsorship", at: now.Add(-time.Hour), body: "OK, we'll think about it some more."})
	history["spons"] = "11"
	s.Sync(context.Background())
	if sp, _ := s.store.Get("spons"); sp.Done || !sp.Open(now) || sp.Messages != 3 {
		t.Fatalf("new mail didn't reopen: %+v", sp)
	}
}
