package snap

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sreday/badger/bridge"
)

// fakeCalls is Snoop as Snap sees it.
type fakeCalls struct{ calls []bridge.Call }

func (f *fakeCalls) Call(id string) (bridge.Call, bool) {
	for _, c := range f.calls {
		if c.ID == id {
			return c, true
		}
	}
	return bridge.Call{}, false
}
func (f *fakeCalls) CallsWith(emails []string, company string) []bridge.Call {
	m := bridge.NewMatcher(emails, company, team)
	var out []bridge.Call
	for _, c := range f.calls {
		if m.Hit(bridge.Emails(c.People), c.Company) {
			out = append(out, c)
		}
	}
	return out
}
func (f *fakeCalls) OpenPromises() []bridge.Call {
	var out []bridge.Call
	for _, c := range f.calls {
		if !c.Upcoming && ((c.Missed && !c.Resolved) || len(c.Open(true)) > 0) {
			out = append(out, c)
		}
	}
	return out
}
func (f *fakeCalls) SearchCalls(q string, n int) []bridge.CallHit {
	var out []bridge.CallHit
	for _, c := range f.calls {
		if strings.Contains(strings.ToLower(c.Summary), strings.ToLower(q)) {
			out = append(out, bridge.CallHit{Call: c, Lines: []string{"[10:02] Jane: " + c.Summary}})
		}
	}
	return out
}
func (f *fakeCalls) CompanyFacts(company string) string {
	for _, c := range f.calls {
		if bridge.Norm(c.Company) == bridge.Norm(company) {
			return c.Facts
		}
	}
	return ""
}

func TestBridgeToSnoop(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	b64 := func(s string) string { return base64.URLEncoding.EncodeToString([]byte(s)) }
	drafts := `{"drafts":[]}`
	gm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/drafts":
			w.Write([]byte(drafts))
		case "/drafts/d9":
			w.Write([]byte(`{"id":"d9","message":{"id":"m9","threadId":"acme","payload":{"mimeType":"text/plain","headers":[{"name":"Subject","value":"Re: Gold package"},{"name":"To","value":"Jane Doe <jane@acme.com>"}],"body":{"data":"` + b64("Hey Jane, prospectus attached.") + `"}}}}`))
		default:
			json.NewEncoder(w).Encode(map[string]any{"id": strings.TrimPrefix(r.URL.Path, "/threads/"), "historyId": "1", "messages": []any{}})
		}
	}))
	defer gm.Close()
	var fired []string
	fire := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Text string }
		json.NewDecoder(r.Body).Decode(&in)
		mu.Lock()
		fired = append(fired, in.Text)
		mu.Unlock()
		w.Write([]byte(`{}`))
	}))
	defer fire.Close()
	s, err := New(Config{Base: "/snap", DataDir: t.TempDir(), AdminPassword: "pw", Account: "mark@sreday.com", Every: time.Hour, TeamDomains: team, TZ: "Europe/London",
		PublicURL: "https://scan.example", DraftRoutine: "trig_draft", DraftToken: "sk-ant-oat01-x"})
	if err != nil {
		t.Fatal(err)
	}
	clock := now
	s.now = func() time.Time { return clock }
	s.gmail = NewGmail(func() (string, error) { return "tok", nil })
	s.gmail.API = gm.URL
	s.gmail.Gap = 0
	s.fireBase = fire.URL + "/%s"
	s.draftGap = 10 * time.Millisecond

	held := bridge.Call{ID: "c1", Title: "Acme x SREday", URL: "https://scan.example/snoop/admin/call/c1", Company: "Acme", Start: now.Add(-26 * time.Hour), End: now.Add(-25 * time.Hour), Updated: now.Add(-24 * time.Hour),
		Summary: "Acme wants the Gold package at SREday NYC; 4,500 EUR agreed.", Facts: "NYC HQ; ~400 people.",
		People: []bridge.Person{{Name: "Jane Doe", Email: "jane@acme.com"}},
		Steps:  []bridge.Step{{I: 0, Text: "Mark: send the prospectus by Friday", Mine: true}, {I: 1, Text: "Jane: confirm with finance", Done: true}}}
	orphan := bridge.Call{ID: "c2", Title: "Globex intro", URL: "https://scan.example/snoop/admin/call/c2", Company: "Globex", Start: now.Add(-50 * time.Hour), End: now.Add(-49 * time.Hour), Updated: now.Add(-49 * time.Hour),
		People: []bridge.Person{{Name: "Bo", Email: "bo@globex.io"}}, Steps: []bridge.Step{{I: 0, Text: "Mark: send Bo the LLM Day Hamburg deck", Mine: true}}}
	calls := &fakeCalls{calls: []bridge.Call{held, orphan}}
	s.SetCalls(calls)

	acme := flagged("acme", "sponsors", "high", now.Add(-30*time.Hour))
	acme.Company, acme.Label, acme.Subject, acme.Why = "Acme", "Acme Gold package undecided", "Gold package", "Jane asked for the price."
	acme.AnalyzedAt = now.Add(-28 * time.Hour) // Claude read it before the call's notes existed
	acme.Actions = []Action{{Title: "Send the price", Kind: "reply"}}
	s.store.Update("acme", func(x *Thread) { *x = acme })
	lone := flagged("lone", "speakers", "low", now.Add(-40*24*time.Hour))
	lone.People = []Person{{Name: "Zed", Email: "zed@nowhere.dev"}}
	s.store.Update("lone", func(x *Thread) { *x = lone })
	s.excerpts["acme"] = cached{"1", "<thread id=\"acme\">...</thread>\n"}
	h := s.Handler()
	page := func(path string) string { return call(t, h, "GET", path, "pw", "").Body.String() }

	// The card: Related call, one click away in Snoop; none for a stranger.
	body := page("/?t=acme") // fresh and waiting on Mark: it is on the Hot plate
	if !strings.Contains(body, `class="callpill" href="https://scan.example/snoop/admin/call/c1"`) || !strings.Contains(body, "Related call") {
		t.Fatal("no Related call pill on the card")
	}
	if !strings.Contains(page("/?view=speakers&t=lone"), `class="callpill none"`) {
		t.Fatal("a thread with no call should show the greyed pill")
	}
	// A promise from a call with no card and no email since: listed on Hot,
	// with the way into Snoop; the one a card already carries is not.
	hot := page("/")
	if !strings.Contains(hot, "Promised on a call, no email yet") || !strings.Contains(hot, "send Bo the LLMday Hamburg deck") || !strings.Contains(hot, `class="promises glass"`) {
		t.Fatal("the Hot tab should list the Globex promise, brands spelled right")
	}
	strip := hot[strings.Index(hot, `class="promises glass"`):]
	strip = strip[:strings.Index(strip, "</div>")]
	if !strings.Contains(strip, "/snoop/admin/call/c2") || strings.Contains(strip, "/snoop/admin/call/c1") {
		t.Fatal("the strip should link the Globex call only: a card already carries Acme's")
	}
	// New call notes send the thread back to Claude, with the notes in the excerpt.
	s.markRelated()
	s.callsTick(clock)
	a, _ := s.store.Get("acme")
	if !a.Pending() {
		t.Fatal("call notes newer than Claude's read should send the thread back to Claude")
	}
	var q struct {
		Instructions string      `json:"instructions"`
		Threads      []QueueItem `json:"threads"`
	}
	json.Unmarshal(call(t, h, "GET", "/api/queue", "Bearer "+s.token, "").Body.Bytes(), &q)
	if len(q.Threads) != 1 || !strings.Contains(q.Threads[0].Excerpt, "CALLS WITH THE SAME PEOPLE OR COMPANY") || !strings.Contains(q.Threads[0].Excerpt, "4,500 EUR agreed") ||
		!strings.Contains(q.Threads[0].Excerpt, "next step (open): Mark: send the prospectus by Friday") || !strings.Contains(q.Threads[0].Excerpt, "from a call, via Snoop): NYC HQ") {
		t.Fatalf("the excerpt lacks the call: %+v", q.Threads)
	}
	if !strings.Contains(q.Instructions, "calendly.com/sreday/30min") || !strings.Contains(q.Instructions, "CALLS.") {
		t.Fatal("the instructions don't cover calls")
	}
	if !strings.Contains(s.draftPrompt(a, 0, ""), "Mark's call with them on 12 Nov 2026") {
		t.Fatal("a draft request should carry the call")
	}
	// Quick question can search the calls.
	if rec := call(t, h, "GET", "/api/calls?q=gold+package", "Bearer "+s.token, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Acme x SREday") {
		t.Fatalf("calls search: %d %s", rec.Code, rec.Body)
	}

	// What Snap offers Snoop: the pre-call brief...
	cards := s.CardsWith([]string{"jane@acme.com"}, "")
	if len(cards) != 1 || cards[0].Headline != "Acme Gold package undecided" || cards[0].Ball != "you" || cards[0].URL != "https://scan.example/snap/?view=sponsors&t=acme" {
		t.Fatalf("CardsWith: %+v", cards)
	}
	if s.CompanyFacts("acme") != "" {
		t.Fatal("no facts yet")
	}
	// ...and Resolve in Snap: a draft, then where it stands.
	clock = time.Now() // the draft watcher runs on the real clock
	ref, err := s.Draft(bridge.DraftRequest{CallID: "c1", CallTitle: "Acme x SREday", CallURL: held.URL, CallAt: held.Start, People: held.People, Company: "Acme",
		Brief: "Mark: send the prospectus by Friday", Notes: "What happened on the call: Acme wants the Gold package.\n"})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	payload := fired[len(fired)-1]
	mu.Unlock()
	if !strings.HasPrefix(payload, composeMarker) || !strings.Contains(payload, "Follow up on a call") || !strings.Contains(payload, "send the prospectus by Friday") || !strings.Contains(payload, "jane@acme.com") {
		t.Fatalf("what the drafter got: %q", payload)
	}
	if st := s.DraftState(ref); st.Status != "working" {
		t.Fatalf("state: %+v", st)
	}
	mu.Lock()
	drafts = `{"drafts":[{"id":"d9","message":{"id":"m9","threadId":"acme"}}]}`
	mu.Unlock()
	var st bridge.DraftState
	for i := 0; i < 300; i++ {
		if st = s.DraftState(ref); st.Status == "ready" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st.Status != "ready" || st.Subject != "Re: Gold package" || !strings.Contains(st.Gmail, "#drafts/acme") || !strings.Contains(st.Snap, "/snap/?view=sponsors&t=acme") {
		t.Fatalf("ready: %+v", st)
	}
	// The trace is on the thread.
	for i := 0; i < 100; i++ {
		if a, _ = s.store.Get("acme"); len(a.Context) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(a.Context) != 1 || !strings.Contains(a.Context[0], `Drafted from your call "Acme x SREday"`) {
		t.Fatalf("no trace of the call on the thread: %v", a.Context)
	}
	// Mark sends it: Snoop hears "sent".
	s.updateSend("d9", func(p *Send) { p.Sent, p.SentAt = "Hey Jane, prospectus attached.", clock })
	if st = s.DraftState(ref); st.Status != "sent" {
		t.Fatalf("sent: %+v", st)
	}
	s.store.Update("acme", func(x *Thread) { x.LastFrom, x.LastAt = "me", clock })
	if sent, ok := s.SentTo([]string{"jane@acme.com"}, held.End); !ok || sent.Subject != "Gold package" {
		t.Fatalf("SentTo: %+v %v", sent, ok)
	}
	if _, ok := s.SentTo([]string{"bo@globex.io"}, orphan.End); ok {
		t.Fatal("nothing was sent to Globex")
	}

	clock = now
	// A call booked with a card's people parks the card until it's held.
	s.store.Update("acme", func(x *Thread) { x.LastFrom, x.LastAt, x.SnoozeUntil = "Jane", now.Add(-30*time.Hour), time.Time{} })
	calls.calls = append(calls.calls, bridge.Call{ID: "c3", Title: "Acme: contract call", URL: "https://scan.example/snoop/admin/call/c3", Company: "Acme", Upcoming: true,
		Start: now.Add(72 * time.Hour), End: now.Add(73 * time.Hour), People: held.People})
	s.callsTick(clock)
	a, _ = s.store.Get("acme")
	if a.Bucket(clock) != "snoozed" || !a.SnoozeUntil.Equal(now.Add(73*time.Hour)) || !strings.HasPrefix(a.Why, "Call booked for Mon 16 Nov") {
		t.Fatalf("a booked call should park the card: %s until %v, why %q", a.Bucket(clock), a.SnoozeUntil, a.Why)
	}
	if !strings.Contains(page("/?view=sponsors&st=snoozed&t=acme"), "Call booked") {
		t.Fatal("the card should say a call is booked")
	}
	// Woken by hand, it stays awake.
	call(t, h, "POST", "/admin/thread/acme/state", "pw", "s=active")
	s.callsTick(clock)
	if a, _ = s.store.Get("acme"); a.Bucket(clock) != "active" {
		t.Fatal("a card Mark woke by hand was parked again")
	}
}
