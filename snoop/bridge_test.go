package snoop

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sreday/badger/bridge"
)

// fakeMail is Snap as Snoop sees it.
type fakeMail struct {
	searched []string
	asked    []bridge.DraftRequest
	state    map[string]bridge.DraftState
	cards    []bridge.Card
	sent     *bridge.Sent
	facts    string
	refuse   error
}

func (f *fakeMail) Draft(r bridge.DraftRequest) (string, error) {
	if f.refuse != nil {
		return "", f.refuse
	}
	f.asked = append(f.asked, r)
	ref := fmt.Sprintf("ref%d", len(f.asked))
	if f.state == nil {
		f.state = map[string]bridge.DraftState{}
	}
	f.state[ref] = bridge.DraftState{Status: "working"}
	return ref, nil
}
func (f *fakeMail) DraftState(ref string) bridge.DraftState { return f.state[ref] }
func (f *fakeMail) CardsWith(emails []string, company string) []bridge.Card {
	return f.cards
}
func (f *fakeMail) SentTo(emails []string, since time.Time) (bridge.Sent, bool) {
	if f.sent == nil || !f.sent.At.After(since) {
		return bridge.Sent{}, false
	}
	return *f.sent, true
}
func (f *fakeMail) CompanyFacts(company string) string { return f.facts }
func (f *fakeMail) Search(q string, n int) ([]bridge.MailThread, error) {
	f.searched = append(f.searched, q)
	return []bridge.MailThread{{Subject: "Booth at SREday NYC", Mails: []bridge.MailLine{{Date: "2026-11-01 10:00", From: "Jane Doe <jane@acme.com>", Text: "Can we get the stand by the entrance?"}}}}, nil
}

func TestBridgeToSnap(t *testing.T) {
	s := newTestServer(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	mail := &fakeMail{cards: []bridge.Card{{ID: "t1", Headline: "Acme Gold package undecided", Status: "Jane asked for the price.", Ball: "you", Bucket: "active", URL: "https://scan.example/snap/?view=sponsors&t=t1"}},
		facts: "NYC HQ; ~400 people; observability SaaS."}
	s.SetMail(mail)
	s.store.Update("c1", func(c *Call) {
		*c = Call{ID: "c1", Title: "Acme x SREday", Start: now.Add(-3 * time.Hour), End: now.Add(-2 * time.Hour), EventID: "e1",
			Attendees:   []Attendee{{Name: "Jane Doe", Email: "jane@acme.com"}, {Email: "mark@sreday.com"}},
			ClaudeNotes: &Notes{Summary: "Acme wants a booth at SREday NYC.", NextSteps: []string{"Mark: send the prospectus by Friday", "Jane: check the budget with finance"}, By: "Claude"},
			APILines:    []Line{{At: now.Add(-3 * time.Hour), Speaker: "Jane", Text: "We could do the Gold package if the booth is near the entrance."}},
			Important:   map[int]bool{0: true, 1: true}}
	})
	s.store.Update("missed", func(c *Call) {
		*c = Call{ID: "missed", Title: "Globex intro", MeetingCode: "aaa-bbbb-ccc", Start: now.Add(-26 * time.Hour), End: now.Add(-25 * time.Hour), EventID: "e2", Calendly: true,
			Attendees: []Attendee{{Name: "Bo", Email: "bo@globex.io"}}}
	})
	s.store.Update("next", func(c *Call) {
		*c = Call{ID: "next", Title: "Acme follow-up", Start: now.Add(48 * time.Hour), End: now.Add(49 * time.Hour), EventID: "e3",
			Attendees: []Attendee{{Name: "Jane Doe", Email: "jane@acme.com"}}}
	})
	h := s.Handler()
	req := func(method, path string, form url.Values) *httptest.ResponseRecorder {
		var r *http.Request
		if form != nil {
			r = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("X-Requested-With", "fetch")
		} else {
			r = httptest.NewRequest(method, path, nil)
		}
		r.SetBasicAuth("", "pw")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}

	// What Snoop offers Snap.
	calls := s.CallsWith([]string{"someone@acme.com"}, "") // a colleague: same work domain
	if len(calls) != 2 || calls[0].ID != "next" || !calls[0].Upcoming || calls[1].ID != "c1" {
		t.Fatalf("CallsWith: %+v", calls)
	}
	c1 := calls[1]
	if c1.Summary == "" || len(c1.Steps) != 2 || !c1.Steps[0].Mine || c1.Steps[1].Mine || c1.URL != "https://scan.example/snoop/admin/call/c1" {
		t.Fatalf("the call's view: %+v", c1)
	}
	if got := s.CallsWith([]string{"mark@sreday.com", "x@gmail.com"}, ""); len(got) != 0 {
		t.Fatalf("the team's own address or a stranger's gmail matched %d calls", len(got))
	}
	if p := s.OpenPromises(); len(p) != 2 || p[0].ID != "c1" || p[1].ID != "missed" || !p[1].Missed {
		t.Fatalf("OpenPromises: %+v", p)
	}
	if hits := s.SearchCalls("booth entrance", 5); len(hits) != 1 || hits[0].Call.ID != "c1" || len(hits[0].Lines) == 0 {
		t.Fatalf("SearchCalls: %+v", hits)
	}

	// The tiles offer Resolve in Snap.
	board := func() string {
		return req("GET", "/", nil).Body.String() + req("GET", "/?view=other", nil).Body.String()
	}
	page := board()
	if strings.Count(page, "Resolve in Snap") < 3 || !strings.Contains(page, "/admin/call/c1/snap") {
		t.Fatal("the Needs attention tiles don't offer Resolve in Snap")
	}
	if rec := req("POST", "/admin/call/c1/snap", url.Values{"i": {"0"}}); rec.Code != http.StatusNoContent {
		t.Fatalf("resolve: %d %s", rec.Code, rec.Body)
	}
	if len(mail.asked) != 1 || mail.asked[0].Brief != "Mark: send the prospectus by Friday" || mail.asked[0].People[0].Email != "jane@acme.com" ||
		!strings.Contains(mail.asked[0].Notes, "Acme wants a booth") || mail.asked[0].CallURL != "https://scan.example/snoop/admin/call/c1" {
		t.Fatalf("what Snap was asked: %+v", mail.asked)
	}
	req("POST", "/admin/call/c1/snap", url.Values{"i": {"0"}}) // a second click waits for the first
	if len(mail.asked) != 1 {
		t.Fatal("a second click asked Snap again")
	}
	if page = board(); !strings.Contains(page, "Snap is drafting this") {
		t.Fatal("the tile doesn't say Snap is drafting")
	}
	// The draft lands, then goes out: the tile says so and ticks itself.
	mail.state["ref1"] = bridge.DraftState{Status: "ready", Subject: "Re: SREday NYC", Gmail: "https://mail.google.com/x", Snap: "https://scan.example/snap/?t=t1"}
	if page = board(); !strings.Contains(page, "Draft ready in Gmail") || !strings.Contains(page, "open in Snap") {
		t.Fatal("the tile doesn't show the ready draft")
	}
	mail.state["ref1"] = bridge.DraftState{Status: "sent", Subject: "Re: SREday NYC", At: now}
	req("GET", "/", nil)
	c, _ := s.store.Get("c1")
	if !c.Ticked[0] || c.Ticked[1] || c.Snap[0].Status != "sent" {
		t.Fatalf("after the email went out the step should be ticked: %+v %+v", c.Ticked, c.Snap)
	}
	// A missed sales call resolves itself once Mark has emailed them.
	if m, _ := s.store.Get("missed"); m.Resolved {
		t.Fatal("resolved too early")
	}
	mail.sent = &bridge.Sent{At: now.Add(-time.Hour), Subject: "Sorry we missed each other", URL: "https://mail.google.com/y"}
	req("GET", "/", nil)
	if m, _ := s.store.Get("missed"); !m.Resolved || m.MailedSubject != "Sorry we missed each other" {
		t.Fatalf("a missed call Mark followed up by email should resolve itself: %+v", m.Resolved)
	}
	// The call page: the pre-call brief from Snap, and what Snap knows about them.
	body := req("GET", "/admin/call/next", nil).Body.String()
	for _, want := range []string{"In your mail", "Acme Gold package undecided", "Ball in your park", "https://scan.example/snap/?view=sponsors&amp;t=t1", "NYC HQ; ~400 people"} {
		if !strings.Contains(body, want) {
			t.Errorf("the call page lacks %q", want)
		}
	}
	if in, _ := s.claudeInput(&c); !strings.Contains(in, "via Snap): NYC HQ") {
		t.Error("Claude's reading of the call doesn't get Snap's company facts")
	}
	// Snap is busy: the reason comes back to the page.
	mail.refuse = fmt.Errorf("Three drafts are already in the making.")
	if rec := req("POST", "/admin/call/c1/snap", url.Values{"i": {"1"}}); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "Three drafts") {
		t.Fatalf("a refusal from Snap: %d %s", rec.Code, rec.Body)
	}
}

func TestClaudeReadsInContext(t *testing.T) {
	s := newTestServer(t)
	now := s.now()
	mail := &fakeMail{}
	s.SetMail(mail)
	mk := func(id string, ago time.Duration, change func(c *Call)) {
		s.store.Update(id, func(c *Call) {
			*c = Call{ID: id, Title: "Acme " + id, Start: now.Add(-ago), End: now.Add(-ago + time.Hour), EventID: id,
				Attendees: []Attendee{{Name: "Jane Doe", Email: "jane@acme.com"}, {Email: "mark@sreday.com"}},
				APILines: []Line{{At: now.Add(-ago), Speaker: "Jane", Text: "We would like the Gold package for SREday NYC and need the floor plan before we sign anything at all."},
					{At: now.Add(-ago), Speaker: "Mark", Text: "Understood, the floor plan goes out tomorrow morning together with the prospectus and the list of last year's sponsors."}}}
			change(c)
		})
	}
	mk("old", 200*time.Hour, func(c *Call) {
		c.TLDR, c.Points = "Jane Doe runs marketing at Acme.", []string{"Jane: budget is signed off in January"}
		c.ClaudeNotes = &Notes{Summary: "First chat.", NextSteps: []string{"Mark: send the prospectus"}, Sided: true, Ours: 1}
	})
	mk("new", 3*time.Hour, func(c *Call) {})
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+s.claude.token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	var q struct {
		Instructions string            `json:"instructions"`
		Lookups      map[string]string `json:"lookups"`
		Calls        []claudeItem      `json:"calls"`
	}
	json.Unmarshal(get("/api/claude/queue").Body.Bytes(), &q)
	var in, read string
	for _, c := range q.Calls {
		if c.ID == "new" {
			in, read = c.Input, c.Read
		}
	}
	for _, want := range []string{"Earlier calls with these people", "budget is signed off in January", "From Mark's mail", "stand by the entrance"} {
		if !strings.Contains(in, want) {
			t.Errorf("the call's context lacks %q", want)
		}
	}
	if len(mail.searched) == 0 || !strings.Contains(mail.searched[0], "jane@acme.com") || strings.Contains(mail.searched[0], "mark@sreday.com") {
		t.Errorf("mail searched: %v", mail.searched)
	}
	if !strings.Contains(q.Instructions, "HOUSE RULES.") || !strings.Contains(q.Lookups["mail"], "/snoop/api/claude/mail?q=") {
		t.Error("the queue lacks the house brief or the lookups")
	}
	if w := get("/api/claude/mail?q=Berlin"); w.Code != 200 || !strings.Contains(w.Body.String(), "Booth at SREday NYC") {
		t.Errorf("mail lookup: %d %s", w.Code, w.Body)
	}
	if w := get("/api/claude/calls?q=budget+January"); w.Code != 200 || !strings.Contains(w.Body.String(), "Jane Doe runs marketing") {
		t.Errorf("calls lookup: %d %s", w.Code, w.Body)
	}
	// Claude reads both; Marek corrects one: it comes back, and the lesson leads every read.
	for _, c := range q.Calls {
		c := c
		s.store.Update(c.ID, func(x *Call) {
			applyClaude(x, ClaudeAnswer{Read: c.Read, Summary: "x", TLDRShort: "Jane, Acme: wants Gold.", TLDR: "Jane Doe runs marketing at Acme and wants the Gold package.", Points: []pointAns{{Short: "a point", More: "the point in full"}}}, "Claude", now.Add(-time.Minute))
		})
	}
	_ = read
	r := httptest.NewRequest("POST", "/admin/call/new/correct", strings.NewReader("text=The+floor+plan+goes+first%2C+not+the+prospectus"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetBasicAuth("", "pw")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	q.Calls = nil
	json.Unmarshal(get("/api/claude/queue").Body.Bytes(), &q)
	if len(q.Calls) != 1 || q.Calls[0].ID != "new" || !strings.Contains(q.Calls[0].Input, "Mark's corrections") || !strings.Contains(q.Instructions, "LESSONS") || !strings.Contains(q.Instructions, "floor plan goes first") {
		t.Errorf("after a correction (%d, %d calls): the call should come back with it, and the lesson should lead", w.Code, len(q.Calls))
	}
	r = httptest.NewRequest("GET", "/admin/call/new", nil)
	r.SetBasicAuth("", "pw")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	for _, want := range []string{"What was said", "a point", `<div class="full" hidden>the point in full</div>`, "Jane, Acme: wants Gold.", "Read more", "Correct the notes", "reading the call again"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("the call page lacks %q", want)
		}
	}
}
