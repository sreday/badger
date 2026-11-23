package snore

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFoldText(t *testing.T) {
	if got := foldText("  Proud  Gold-Sponsor of “Big Conf” 2026! "); got != "proud gold sponsor of big conf 2026" {
		t.Fatalf("foldText = %q", got)
	}
}

func TestSponsorChannel(t *testing.T) {
	for _, c := range []struct {
		card SponsorCard
		want string
	}{
		{SponsorCard{Label: LabelWarmedUp, Owner: "teammate-a", Thread: "https://mail.example/1"}, "teammate"},
		{SponsorCard{Label: LabelRevive, Owner: Organizer, Thread: "https://mail.example/1"}, "thread"},
		{SponsorCard{Label: LabelWarm, People: []SponsorPerson{{Start: true, Email: "a@example.com", Country: "GB"}}}, "mail"},
		{SponsorCard{Label: LabelWarm, People: []SponsorPerson{{Start: true, Email: "a@example.de", Country: "DE"}}, FormURL: "https://example.de/sponsor"}, "form"},
		{SponsorCard{Label: LabelWarm, People: []SponsorPerson{{Start: true, Email: "a@example.pl", Country: "PL"}}}, "linkedin"},
		{SponsorCard{Label: LabelWarm, People: []SponsorPerson{{Start: true, Email: "a@example.com"}}}, "linkedin"}, // country unknown: no first email
		{SponsorCard{Label: LabelWarm, People: []SponsorPerson{{Start: true}}}, "linkedin"},
	} {
		if got := sponsorChannel(&c.card); got != c.want {
			t.Errorf("channel of %+v = %q, want %q", c.card, got, c.want)
		}
	}
}

// sponsorTestServer: one warmed up company and one with an inside contact,
// both pointing to the LLM brand, which has an event four months out.
func sponsorTestServer(t *testing.T) (*Server, http.Handler) {
	s, h := testServer(t)
	now := s.now()
	ago := func(days int) time.Time { return now.Add(-time.Duration(days) * 24 * time.Hour) }
	s.fp.F["x/llmday/2027-testville-q1"] = &Folder{Key: "x/llmday/2027-testville-q1", Brand: "llmday", Name: "Testville", Start: now.AddDate(0, 4, 0)}
	d := &humanData{
		profiles: []humanProfile{
			{slug: "w1", name: "Wanda Warm", title: "SRE", company: "Warmco"},
			{slug: "i1", name: "Ina Inside", title: "Head of Events", company: "Insideco"},
		},
		attendees: []humanAttendee{
			{slug: "w1", email: "w1@warm.example", event: "LLMday City 2026", registered: ago(60)},
			{slug: "i1", email: "i1@inside.example", event: "LLMday City 2026", registered: ago(60), checkedIn: ago(50)},
		},
		events: []humanEvent{{slug: "w1", kind: "interested", by: "teammate-a", at: ago(20)}},
	}
	s.cfg.HumansDB = "x"
	s.readTouches = func(context.Context, string) (*humanData, error) { return d, nil }
	s.syncTouches()
	if err := os.WriteFile(filepath.Join(s.dir, "sponsors-recipe.txt"), []byte("Judge by the test recipe."), 0o600); err != nil {
		t.Fatal(err)
	}
	s.raw = func(u string, _ http.Header) (int, []byte, error) {
		if u == "https://proof.example/sponsors" {
			return http.StatusOK, []byte("<html><body><h2>Gold&nbsp;sponsors</h2><p>Warmco is a proud <b>gold sponsor</b> of Big Conf 2026.</p></body></html>"), nil
		}
		return http.StatusNotFound, nil, nil
	}
	return s, h
}

func queueJobs(t *testing.T, s *Server, h http.Handler) []map[string]any {
	t.Helper()
	w := do(t, h, "GET", "/api/sponsors/queue", "", bearer(s))
	if w.Code != http.StatusOK {
		t.Fatalf("queue: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Instructions string           `json:"instructions"`
		Jobs         []map[string]any `json:"jobs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Instructions, "word-for-word") || !strings.Contains(out.Instructions, "Judge by the test recipe.") {
		t.Fatal("the instructions are missing")
	}
	return out.Jobs
}

func TestSponsorQueueAnswersBoard(t *testing.T) {
	s, h := sponsorTestServer(t)
	if w := do(t, h, "GET", "/api/sponsors/queue", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("queue without the token: %d", w.Code)
	}
	if w := do(t, h, "POST", "/api/sponsors/answers", "{}", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("answers without the token: %d", w.Code)
	}

	jobs := queueJobs(t, s, h)
	if len(jobs) != 1 || jobs[0]["brand"] != "LLMday" {
		t.Fatalf("want one job, for the brand with candidates and an event to offer: %+v", jobs)
	}
	id := jobs[0]["id"].(string)
	if cs := jobs[0]["candidates"].([]any); len(cs) != 2 {
		t.Fatalf("candidates = %+v", cs)
	}
	if evs := jobs[0]["events"].([]any); len(evs) != 1 || evs[0].(map[string]any)["name"] != "LLMday Testville" {
		t.Fatalf("events = %+v", jobs[0]["events"])
	}
	// Handed out: a second ask gets nothing until the lease is over.
	if again := queueJobs(t, s, h); len(again) != 0 {
		t.Fatalf("a handed-out job came again: %+v", again)
	}

	answer := `{"model":"test-model","answers":[{"id":"` + id + `","note":"one good","cards":[
	 {"key":"warm.example","company":"Renamed By Claude","label":"Proven","sells":"Warm things for engineers","kind":"vendor","money":"some",
	  "why":[{"text":"Gold sponsor of a comparable event","quote":"proud gold sponsor of Big Conf 2026","url":"https://proof.example/sponsors","date":"2026-05-01"},
	         {"text":"Made up","quote":"this sentence is nowhere on the page","url":"https://proof.example/sponsors"},
	         {"text":"No page","quote":"proud gold sponsor of Big Conf 2026","url":"http://insecure.example/"}],
	  "offer":["LLMday Testville"],"risk":"small team",
	  "people":[{"name":"Wanda Warm","title":"SRE","known":"answered our teammate","start":true,"country":"gb"},
	            {"name":"Stranger NoProof","title":"CMO"},
	            {"name":"Pat Proof","title":"Field Marketing","proof":"https://warm.example/team","profile":"linkedin.com/in/Pat-Proof","email":"Pat@Warm.example","country":"GB"}],
	  "subject":"LLMday Testville","mail":"Hey Pat, short mail.","note":"Short note.","brief":"Ask Wanda who looks after events."},
	 {"key":"notinjob.example","why":[{"text":"x","quote":"proud gold sponsor of Big Conf 2026","url":"https://proof.example/sponsors"}]}],
	 "refused":[{"key":"inside.example","reason":"sells to a different audience"}]}]}`
	w := do(t, h, "POST", "/api/sponsors/answers", answer, bearer(s))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"kept":1`) || !strings.Contains(w.Body.String(), `"dropped":1`) {
		t.Fatalf("answers: %d %s", w.Code, w.Body.String())
	}
	cards := s.sp.Cards["llmday"]
	if len(cards) != 1 {
		t.Fatalf("cards = %+v", cards)
	}
	c := cards[0]
	if c.Company != "Warmco" || c.Label != LabelWarmedUp || c.Owner != "teammate-a" || c.Level != 4 {
		t.Fatalf("what we hold must come from our own records, not from the answer: %+v", c)
	}
	if len(c.Why) != 1 || !c.Why[0].Checked {
		t.Fatalf("only the claim that stands on its page is kept: %+v", c.Why)
	}
	if len(c.People) != 2 || c.People[1].Name != "Pat Proof" || c.People[1].Email != "pat@warm.example" || c.People[1].Profile != "https://www.linkedin.com/in/pat-proof/" || !c.People[0].Start {
		t.Fatalf("people = %+v", c.People)
	}
	if c.Channel != "teammate" || c.Money != "some" || c.Model != "test-model" {
		t.Fatalf("card = %+v", c)
	}
	if _, ok := s.sp.Passed["inside.example"]; !ok {
		t.Fatal("a refused company is remembered")
	}

	if w := do(t, h, "GET", "/sponsors?b=llmday", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("board without login: %d", w.Code)
	}
	body := do(t, h, "GET", "/sponsors?b=llmday", "", admin).Body.String()
	for _, want := range []string{"Warmco", "Warmed up", "Brief for teammate-a", "Ask Wanda who looks after events.", "Gold sponsor of a comparable event", "money: some", "LLMday Testville", "start here"} {
		if !strings.Contains(body, want) {
			t.Errorf("sponsors page misses %q", want)
		}
	}
	if strings.Contains(body, "Stranger NoProof") || strings.Contains(body, "Made up") {
		t.Error("an unproven person or claim reached the page")
	}

	// Reached out, undo, and crossed out for good.
	if w := do(t, h, "POST", "/admin/sponsor/reached", "key=warm.example&b=llmday", admin); w.Code != http.StatusSeeOther {
		t.Fatalf("reached: %d", w.Code)
	}
	if s.sp.Cards["llmday"][0].Reached.IsZero() {
		t.Fatal("the card should be marked reached")
	}
	do(t, h, "POST", "/admin/sponsor/back", "key=warm.example&b=llmday", admin)
	if !s.sp.Cards["llmday"][0].Reached.IsZero() {
		t.Fatal("undo should clear it")
	}
	if w := do(t, h, "POST", "/admin/sponsor/never", "key=warm.example&b=llmday&reason=too+big", admin); w.Code != http.StatusSeeOther {
		t.Fatalf("never: %d", w.Code)
	}
	if len(s.sp.Cards["llmday"]) != 0 || s.sp.Never["warm.example"].Reason != "too big" {
		t.Fatalf("crossed out: cards %+v never %+v", s.sp.Cards["llmday"], s.sp.Never)
	}
	if w := do(t, h, "POST", "/admin/sponsor/never", "key=nobody.example&b=llmday", admin); w.Code != http.StatusNotFound {
		t.Fatalf("unknown card: %d", w.Code)
	}

	// Next day: neither the crossed-out nor the refused company comes back.
	next := s.now().Add(24 * time.Hour)
	s.now = func() time.Time { return next }
	if jobs := queueJobs(t, s, h); len(jobs) != 0 {
		t.Fatalf("nothing should be left to hand out: %+v", jobs)
	}
}

func TestSponsorCardNeedsProof(t *testing.T) {
	s, h := sponsorTestServer(t)
	jobs := queueJobs(t, s, h)
	id := jobs[0]["id"].(string)
	answer := `{"answers":[{"id":"` + id + `","cards":[{"key":"warm.example","money":"some",
	  "why":[{"text":"Claimed","quote":"nothing like this is on the page at all","url":"https://proof.example/sponsors"}]},
	 {"key":"inside.example","money":"negative",
	  "why":[{"text":"Real","quote":"proud gold sponsor of Big Conf 2026","url":"https://proof.example/sponsors"}]}]}]}`
	w := do(t, h, "POST", "/api/sponsors/answers", answer, bearer(s))
	if !strings.Contains(w.Body.String(), `"kept":0`) || !strings.Contains(w.Body.String(), `"dropped":2`) {
		t.Fatalf("answers: %s", w.Body.String())
	}
	if len(s.sp.Cards["llmday"]) != 0 {
		t.Fatalf("no card without proof, none with bad money news: %+v", s.sp.Cards["llmday"])
	}
	if s.sp.Passed["warm.example"].Reason == "" || s.sp.Passed["inside.example"].Reason != "money: negative" {
		t.Fatalf("passed = %+v", s.sp.Passed)
	}
}

func TestSponsorJobsWaitForFive(t *testing.T) {
	s, h := sponsorTestServer(t)
	early := time.Date(2026, 11, 21, 4, 30, 0, 0, time.UTC)
	s.now = func() time.Time { return early }
	if jobs := queueJobs(t, s, h); len(jobs) != 0 {
		t.Fatalf("before 05:00 UTC nothing is queued: %+v", jobs)
	}
}

func TestSponsorQueueNeedsRecipe(t *testing.T) {
	s, h := sponsorTestServer(t)
	if err := os.Remove(filepath.Join(s.dir, "sponsors-recipe.txt")); err != nil {
		t.Fatal(err)
	}
	w := do(t, h, "GET", "/api/sponsors/queue", "", bearer(s))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "no recipe set") || !strings.Contains(w.Body.String(), `"jobs":[]`) {
		t.Fatalf("without a recipe nothing is handed out: %d %s", w.Code, w.Body.String())
	}
}

func TestPassedAt(t *testing.T) {
	now := time.Date(2026, 11, 22, 21, 0, 0, 0, time.UTC)
	if got := passedAt("Lapsed and no change found", now); !got.Equal(now) {
		t.Fatalf("a real refusal counts from now, got %v", got)
	}
	at := passedAt("Not verified: their site could not be read", now)
	if left := sponsorPassFor - now.Sub(at); left != retryAfter {
		t.Fatalf("an unverified refusal should run out after %v, runs out after %v", retryAfter, left)
	}
}

// The navbar carries the two big tabs, names only, with one of them on.
func TestNavbarTabs(t *testing.T) {
	_, h := testServer(t)
	speakers, sponsors := `<a class="on" href="./">Speakers</a>`, `<a class="on" href="sponsors">Sponsors</a>`
	for path, want := range map[string]string{"/": speakers, "/log": speakers, "/sponsors": sponsors, "/warm": sponsors, "/revive": sponsors, "/room": sponsors} {
		body := do(t, h, "GET", path, "", admin).Body.String()
		if !strings.Contains(body, `<nav class="seg big">`) || !strings.Contains(body, want) {
			t.Errorf("%s: the navbar should carry %s", path, want)
		}
	}
}

func TestNameFitsDomain(t *testing.T) {
	for _, c := range []struct {
		domain, name string
		want         bool
	}{
		{"example.ai", "Example AI", true}, {"example.ai", "Otherco", false}, {"example-labs.com", "Example", true},
		{"name:otherco", "Otherco", false}, {"ex.io", "Ex", false},
	} {
		if got := nameFits(c.domain, c.name); got != c.want {
			t.Errorf("nameFits(%q, %q) = %v", c.domain, c.name, got)
		}
	}
}

// A reply in the old conversation only when the first person to write to
// is one it was with; otherwise it is a first message.
func TestThreadNeedsItsPerson(t *testing.T) {
	c := &SponsorCard{Thread: "https://mail.example/t", With: []string{"Ann Old"}, FormURL: "https://x.example/form",
		People: []SponsorPerson{{Name: "Ben New", Start: true}}}
	if got := sponsorChannel(c); got != "form" {
		t.Fatalf("the contact left: channel = %q, want form", got)
	}
	c.People = []SponsorPerson{{Name: "Ann Old", Start: true}}
	if got := sponsorChannel(c); got != "thread" {
		t.Fatalf("the contact is still there: channel = %q", got)
	}
	c.With = nil
	c.People = []SponsorPerson{{Name: "Ben New", Start: true}}
	if got := sponsorChannel(c); got != "thread" {
		t.Fatalf("nobody known in the talk: channel = %q", got)
	}
}
