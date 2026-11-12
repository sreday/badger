package snap

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	if m := Look(speaker, now, team, nil); m.Skip != "" {
		t.Fatalf("3-month-old speaker thread (a year is read now): skip %q", m.Skip)
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
	// A loose end Claude flagged never expires; only a dead end lets it go.
	th := &Thread{Category: "speakers", LastAt: now.AddDate(-1, -2, 0), AnalyzedHistory: "1", HistoryID: "1", Loose: true, AnalyzedBy: "claude"}
	expire(th, now)
	if th.Skip != "" || !th.Open(now) {
		t.Fatalf("14-month-old flagged loose end expired: %q", th.Skip)
	}
	th.DeadEnd = true
	expire(th, now)
	if th.Skip == "" || th.Open(now) {
		t.Fatal("dead end still shows")
	}
	quiet := &Thread{Category: "others", LastAt: now.AddDate(-1, 0, -2), AnalyzedHistory: "1", HistoryID: "1", AnalyzedBy: "claude"}
	if expire(quiet, now); quiet.Skip != "older than a year" {
		t.Fatalf("year-old resolved thread kept: %q", quiet.Skip)
	}
	host := &Thread{Category: "sponsors", LastAt: now.AddDate(0, -5, 0), Loose: true, FollowUp: "2027-04-01", AnalyzedHistory: "1", HistoryID: "1", AnalyzedBy: "claude"}
	expire(host, now)
	if host.Skip != "" || !host.Later(now) || host.Open(now) {
		t.Fatalf("touch base in 6 months: skip %q later %v open %v", host.Skip, host.Later(now), host.Open(now))
	}
	spring := time.Date(2027, 4, 2, 0, 0, 0, 0, time.UTC)
	if !host.Open(spring) || !host.Due(spring) {
		t.Fatal("due in April")
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

// read is the routine's part: Claude's read of an excerpt, faked.
func read(q QueueItem) Analysis {
	a := Analysis{ID: q.ID, History: q.History, Category: "others", Summary: "s", Priority: "low"}
	switch {
	case strings.Contains(q.Excerpt, "think about it"):
		a.Category, a.Loose, a.Label, a.Waiting = "sponsors", true, "Will think about it", "them"
		a.Actions = []Action{{Title: "Nudge Jane", Prompt: "Draft a nudge to Jane."}}
	case strings.Contains(q.Excerpt, "cancel"):
		a.Category, a.Loose, a.Label, a.CancelName, a.CancelEvent = "speakers", true, "Speaker cancelled", "Ann Lee", "SREday NYC"
	}
	return a
}

func call(t *testing.T, h http.Handler, method, path, auth, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	switch {
	case strings.HasPrefix(auth, "Bearer "):
		req.Header.Set("Authorization", auth)
	case auth != "":
		req.SetBasicAuth("", auth)
	}
	if method == "POST" && !strings.HasPrefix(body, "{") {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Requested-With", "fetch")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// routine does what the Claude routine does: fetch the queue until it's
// empty, answer each batch. It returns every excerpt it saw.
func routine(t *testing.T, h http.Handler, token string) []QueueItem {
	t.Helper()
	var seen []QueueItem
	for i := 0; i < 10; i++ {
		rec := call(t, h, "GET", "/api/queue", "Bearer "+token, "")
		if rec.Code != 200 {
			t.Fatalf("queue: %d %s", rec.Code, rec.Body)
		}
		var q struct {
			Instructions string      `json:"instructions"`
			Threads      []QueueItem `json:"threads"`
			Remaining    int         `json:"remaining"`
		}
		json.Unmarshal(rec.Body.Bytes(), &q)
		if !strings.Contains(q.Instructions, "don't forget, to win") {
			t.Fatal("queue without instructions")
		}
		if len(q.Threads) == 0 {
			return seen
		}
		seen = append(seen, q.Threads...)
		var out []Analysis
		for _, it := range q.Threads {
			out = append(out, read(it))
		}
		body, _ := json.Marshal(map[string]any{"model": "claude-opus-5-5", "threads": out})
		if rec := call(t, h, "POST", "/api/answers", "Bearer "+token, string(body)); rec.Code != 200 {
			t.Fatalf("answers: %d %s", rec.Code, rec.Body)
		}
	}
	t.Fatal("queue never emptied")
	return nil
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

	s, err := New(Config{Base: "/snap", DataDir: t.TempDir(), AdminPassword: "pw", Account: "mark@sreday.com",
		Every: time.Hour, TeamDomains: team, TZ: "Europe/London"})
	if err != nil {
		t.Fatal(err)
	}
	s.gmail = NewGmail(func() (string, error) { return "tok", nil })
	s.google.tok = googleToken{RefreshToken: "r", Email: "mark@sreday.com"}
	s.gmail.API = gm.URL
	s.now = func() time.Time { return now }
	s.spDomsAt = now // no GitHub in tests
	s.lineups.at = now
	s.lineups.list = []Lineup{{Repo: "sreday/sreday", Dir: "2026-nyc-q4", Brand: "SREday", City: "NYC", Talks: []Talk{{Names: []string{"Ann Lee"}, Status: "talk", Title: "Pagers"}}}}
	h := s.Handler()

	run := s.Sync(context.Background())
	if run.Error != "" || run.Listed != 4 || run.Changed != 4 || run.Queued != 3 {
		t.Fatalf("run: %+v", run)
	}
	// Before Claude: nothing shows; the rules only sort and queue.
	if sp, _ := s.store.Get("spons"); sp.Open(now) || sp.AnalyzedBy != "rules" || sp.Category != "sponsors" {
		t.Fatalf("rules read: %+v", sp)
	}
	// The API wants the token, not the admin password.
	if rec := call(t, h, "GET", "/api/queue", "Bearer nope", ""); rec.Code != 401 {
		t.Fatalf("queue without token: %d", rec.Code)
	}
	if rec := call(t, h, "GET", "/api/queue", "pw", ""); rec.Code != 401 {
		t.Fatalf("queue with the admin password: %d", rec.Code)
	}
	seen := routine(t, h, s.token)
	if len(seen) != 3 {
		t.Fatalf("Claude saw %d threads", len(seen))
	}
	for _, e := range seen {
		if strings.Contains(e.Excerpt, "> Here's the prospectus") {
			t.Fatalf("quoted text reached Claude: %s", e.Excerpt)
		}
	}
	sp, _ := s.store.Get("spons")
	if !sp.Open(now) || sp.Category != "sponsors" || sp.Label != "Will think about it" || len(sp.Actions) != 1 || sp.AnalyzedBy != "claude-opus-5-5" {
		t.Fatalf("sponsor thread: %+v", sp)
	}
	ca, _ := s.store.Get("cancel")
	if !strings.Contains(ca.StillListed, "2026-nyc-q4") || !ca.Open(now) {
		t.Fatalf("cancel thread: %+v", ca)
	}
	if n, _ := s.store.Get("news"); n.Skip == "" {
		t.Fatal("newsletter kept")
	}
	if o, _ := s.store.Get("oldspeaker"); o.Skip != "" || o.Open(now) || !o.ByClaude() {
		t.Fatalf("2-month-old speaker thread is read, and Claude found nothing at stake: %+v", o)
	}

	// Nothing changed: the queue stays empty, no tokens spent.
	if run = s.Sync(context.Background()); run.Changed != 0 || run.Queued != 0 {
		t.Fatalf("second sync: %+v", run)
	}
	if again := routine(t, h, s.token); len(again) != 0 {
		t.Fatalf("Claude re-read %d threads", len(again))
	}

	// The lineup drops Ann: the cancellation is handled.
	s.lineups.list = []Lineup{{Repo: "sreday/sreday", Dir: "2026-nyc-q4", City: "NYC"}}
	s.Sync(context.Background())
	if ca, _ := s.store.Get("cancel"); ca.IsLoose() {
		t.Fatalf("cancel still loose after the lineup changed: %+v", ca)
	}

	// The page: auth, the tabs, the thread, the copy prompt.
	if rec := call(t, h, "GET", "/", "", ""); rec.Code != 401 {
		t.Fatalf("no password: %d", rec.Code)
	}
	body := call(t, h, "GET", "/?view=sponsors", "pw", "").Body.String()
	for _, want := range []string{"SREday NYC sponsorship", "Will think about it", "Copy for Claude", "Draft a nudge to Jane.", "thread id spons", "Sponsors<span class=\"n\">1</span>", "claude-opus-5-5"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}

	// Done hides it; a new message brings it back, the rules' read shows
	// until Claude has read the new mail, and a stale answer is dropped.
	if rec := call(t, h, "POST", "/admin/thread/spons/done", "pw", "done=1"); rec.Code != 204 {
		t.Fatalf("done: %d", rec.Code)
	}
	if sp, _ := s.store.Get("spons"); sp.Open(now) {
		t.Fatal("done thread still open")
	}
	stale, _ := json.Marshal(map[string]any{"model": "m", "threads": []Analysis{{ID: "spons", History: "10", Loose: true, Label: "old"}}})
	// An hour-old reply isn't Snap's business yet: Marek may still answer it.
	threads["spons"] = append(threads["spons"], fakeMsg{from: "Jane <jane@acme.com>", to: "mark@sreday.com", subject: "Re: SREday NYC sponsorship", at: now.Add(-time.Hour), body: "OK, we'll think about it some more."})
	history["spons"] = "11"
	if run = s.Sync(context.Background()); run.Queued != 0 {
		t.Fatalf("fresh mail queued: %+v", run)
	}
	if sp, _ := s.store.Get("spons"); sp.Done || sp.Open(now) || sp.Messages != 3 || sp.AnalyzedBy != "rules" {
		t.Fatalf("new mail: %+v", sp)
	}
	// Two days on and still unanswered: now it goes to Claude.
	now = now.Add(49 * time.Hour)
	if run = s.Sync(context.Background()); run.Queued != 1 {
		t.Fatalf("unanswered mail not queued: %+v", run)
	}
	rec := call(t, h, "POST", "/api/answers", "Bearer "+s.token, string(stale))
	if !strings.Contains(rec.Body.String(), `"stale":1`) {
		t.Fatalf("stale answer: %s", rec.Body)
	}
	if got := routine(t, h, s.token); len(got) != 1 || got[0].History != "11" {
		t.Fatalf("requeued: %+v", got)
	}
}

func TestCalendarAndReactionsAreNotReplies(t *testing.T) {
	for _, s := range []string{"Accepted: SREday catch-up @ Mon", "Geweigerd: SRE & LLM - Ambassador Catchup", "Updated invitation with note: Sai and Mark", "[Host Copy] New message in SREday SF"} {
		if !automated(map[string]string{"subject": s}, "rod@kyndryl.com") {
			t.Errorf("%q counted as a reply", s)
		}
	}
	if automated(map[string]string{"subject": "Re: Accepted talks for NYC"}, "rod@kyndryl.com") {
		t.Error("a real reply about accepted talks counted as a calendar answer")
	}
	if !automated(map[string]string{}, "markpawlikowski@user.luma-mail.com") {
		t.Error("Luma subdomain not a robot")
	}
	if !reaction.MatchString("👍 Mark Pawlikowski reacted via Gmail On Fri, 30 Oct") {
		t.Error("reaction not seen")
	}
	if Look(&ParsedThread{Subject: "Sponsoring? no, your session", Mails: []Mail{mailFrom("Jo", "jo@x.com", time.Now(), false, "Your Host Copy of the venue logo")}}, time.Now(), team, nil).Sponsor != true {
		// "Sponsoring" in the subject still counts.
		t.Error("sponsor word missed")
	}
	if Look(&ParsedThread{Subject: "SREday NYC - Info for speakers", Mails: []Mail{mailFrom("Jo", "jo@x.com", time.Now(), false, "the venue, my logo and the invoice")}}, time.Now(), team, nil).Sponsor {
		t.Error("venue/logo/invoice made a speaker thread a sponsor one")
	}
}

func TestMoveByHand(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	s, err := New(Config{Base: "/snap", DataDir: t.TempDir(), AdminPassword: "pw", Account: "mark@sreday.com", Every: time.Hour, TeamDomains: team, TZ: "Europe/London"})
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now }
	s.store.Update("x", func(th *Thread) {
		*th = Thread{ID: "x", Subject: "Venue for SREday Paris", HistoryID: "5", ClaudeHistory: "4", LastAt: now.AddDate(0, 0, -20),
			People: []Person{{Name: "Lu", Email: "lu@station-f.co"}}, Category: "sponsors", Loose: true, AnalyzedBy: "claude", AnalyzedHistory: "4", Company: "Station F"}
	})
	s.excerpts["x"] = cached{"5", "<thread id=\"x\">...</thread>\n"}
	h := s.Handler()
	if rec := call(t, h, "POST", "/admin/thread/x/move", "pw", "cat=team"); rec.Code != 400 {
		t.Fatalf("old Team tab accepted: %d", rec.Code)
	}
	if rec := call(t, h, "POST", "/admin/thread/x/move", "pw", "cat=events"); rec.Code != 204 {
		t.Fatalf("move: %d", rec.Code)
	}
	if th, _ := s.store.Get("x"); th.Category != "events" || th.MovedTo != "events" {
		t.Fatalf("not moved: %+v", th)
	}
	// Claude hears about it, in the instructions and as a hint on the thread.
	var q struct {
		Instructions string      `json:"instructions"`
		Threads      []QueueItem `json:"threads"`
	}
	json.Unmarshal(call(t, h, "GET", "/api/queue", "Bearer "+s.token, "").Body.Bytes(), &q)
	if !strings.Contains(q.Instructions, `"Venue for SREday Paris" (Station F) [station-f.co]: sponsors -> events`) {
		t.Fatalf("no lesson: %s", q.Instructions[len(q.Instructions)-300:])
	}
	if len(q.Threads) != 1 || !strings.HasPrefix(q.Threads[0].Excerpt, `Hint: Marek files threads with station-f.co under "events".`) {
		t.Fatalf("no hint: %+v", q.Threads)
	}
	// Claude's next read never moves it back.
	ans, _ := json.Marshal(map[string]any{"model": "m", "threads": []Analysis{{ID: "x", History: "5", Category: "sponsors", Loose: true}}})
	call(t, h, "POST", "/api/answers", "Bearer "+s.token, string(ans))
	if th, _ := s.store.Get("x"); th.Category != "events" {
		t.Fatalf("Claude moved it back: %q", th.Category)
	}
	// The page has the new tabs.
	body := call(t, h, "GET", "/?view=events", "pw", "").Body.String()
	for _, want := range []string{">Events<", ">Other<", `data-cat="sponsors"`, `draggable="true"`, "Wrong tab? Move to"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}
