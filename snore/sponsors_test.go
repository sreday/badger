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

// sponsorTestServer: a company whose engineer answered a teammate and one
// with an inside contact, both on the LLM brand, which has an event four
// months out.
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
			{slug: "w1", email: "wanda.warm@warm.example", event: "LLMday City 2026", registered: ago(60)},
			{slug: "i1", email: "i1@inside.example", event: "LLMday City 2026", registered: ago(60), checkedIn: ago(50)},
		},
		events: []humanEvent{{slug: "w1", kind: "interested", by: "teammate-a", at: ago(20)}},
	}
	s.cfg.HumansDB = "x"
	s.readTouches = func(context.Context, string) (*humanData, error) { return d, nil }
	s.syncTouches()
	if err := os.WriteFile(filepath.Join(s.dir, "sponsors-recipe.txt"), []byte("Write by the test recipe."), 0o600); err != nil {
		t.Fatal(err)
	}
	return s, h
}

func queueJobs(t *testing.T, s *Server, h http.Handler) []map[string]any {
	t.Helper()
	w := do(t, h, "GET", "/api/sponsors/queue", "", bearer(s))
	if w.Code != http.StatusOK {
		t.Fatalf("queue: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "@") {
		t.Fatal("an address was handed out with the queue")
	}
	var out struct {
		Instructions string           `json:"instructions"`
		Jobs         []map[string]any `json:"jobs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Instructions, "to_known") || !strings.Contains(out.Instructions, "Write by the test recipe.") {
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
	if w := do(t, h, "GET", "/sponsors?b=llmday", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("board without login: %d", w.Code)
	}

	// The list is there before anything was written for it.
	body := do(t, h, "GET", "/sponsors?b=llmday", "", admin).Body.String()
	for _, want := range []string{"Act now", "offer LLMday Testville", "Warmco", "Insideco", "Wanda Warm replied to teammate-a in Nov 2026", "wanda.warm@warm.example",
		"Head of Events", "The texts come with the morning run."} {
		if !strings.Contains(body, want) {
			t.Errorf("the board before the daily read misses %q", want)
		}
	}
	if strings.Contains(body, "Show more") {
		t.Error("two leads need no Show more")
	}

	jobs := queueJobs(t, s, h)
	if len(jobs) != 1 || jobs[0]["brand"] != "LLMday" {
		t.Fatalf("want one job, for the brand with leads and an event to offer: %+v", jobs)
	}
	id := jobs[0]["id"].(string)
	if ls := jobs[0]["leads"].([]any); len(ls) != 2 {
		t.Fatalf("leads = %+v", ls)
	}
	if evs := jobs[0]["events"].([]any); len(evs) != 1 || evs[0].(map[string]any)["name"] != "LLMday Testville" {
		t.Fatalf("events = %+v", jobs[0]["events"])
	}
	// Handed out: a second ask gets nothing until the lease is over.
	if again := queueJobs(t, s, h); len(again) != 0 {
		t.Fatalf("a handed-out job came again: %+v", again)
	}

	answer := `{"model":"test-model","answers":[{"id":"` + id + `","note":"one written","cards":[
	 {"key":"warm.example","name":"Warm Co","company":"Renamed By Claude",
	  "boss":{"name":"Pat Proof","title":"Field Marketing","proof":"https://warm.example/team","profile":"linkedin.com/in/Pat-Proof","country":"gb"},
	  "to_known":{"to":"wanda warm","subject":"LLMday Testville","text":"Hey Wanda, short note to you."},
	  "to_boss":{"subject":"LLMday Testville","text":"Hey Pat, short note to the one who decides."}},
	 {"key":"notinjob.example","to_known":{"text":"x"}}],
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
	if c.Company != "Warmco" || c.Name != "Warm Co" || c.Model != "test-model" || c.Known.To != "Wanda Warm" || !strings.Contains(c.Facts, "Wanda Warm replied") {
		t.Fatalf("what we hold comes from our own records, the name from the company's site: %+v", c)
	}
	if b := c.Boss; b == nil || b.Profile != "https://www.linkedin.com/in/pat-proof/" || b.Country != "GB" || b.Email != "pat.proof@warm.example" ||
		b.Guessed != "first.last, from 1 address we hold" || c.ToBoss.To != "Pat Proof" {
		t.Fatalf("decision maker = %+v, text %+v", c.Boss, c.ToBoss)
	}
	if _, ok := s.sp.Passed["inside.example"]; !ok {
		t.Fatal("a refused company is remembered")
	}

	body = do(t, h, "GET", "/sponsors?b=llmday", "", admin).Body.String()
	for _, want := range []string{"Warm Co", "To Wanda Warm", "Hey Wanda, short note to you.", "To Pat Proof", "Hey Pat, short note to the one who decides.",
		"Decides about sponsoring", "pat.proof@warm.example", "guessed", "Open as email"} {
		if !strings.Contains(body, want) {
			t.Errorf("sponsors page misses %q", want)
		}
	}
	if strings.Contains(body, "Insideco") || strings.Contains(body, "The texts come with the morning run.") {
		t.Error("a refused company stayed on the list, or a written lead still waits")
	}

	// Reached out, undo, and crossed out for good.
	if w := do(t, h, "POST", "/admin/sponsor/reached", "key=warm.example&b=llmday", admin); w.Code != http.StatusSeeOther {
		t.Fatalf("reached: %d", w.Code)
	}
	if s.sp.Cards["llmday"][0].Reached.IsZero() {
		t.Fatal("the lead should be marked reached")
	}
	if body = do(t, h, "GET", "/sponsors?b=llmday", "", admin).Body.String(); !strings.Contains(body, "Reached out already") || !strings.Contains(body, "0 leads") {
		t.Error("a reached lead moves below the list")
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
		t.Fatalf("unknown lead: %d", w.Code)
	}

	// Next day: neither the crossed-out nor the refused company comes back.
	next := s.now().Add(24 * time.Hour)
	s.now = func() time.Time { return next }
	if jobs := queueJobs(t, s, h); len(jobs) != 0 {
		t.Fatalf("nothing should be left to hand out: %+v", jobs)
	}
}

// A lead can be marked reached before anything was written for it.
func TestSponsorReachedBeforeTexts(t *testing.T) {
	s, h := sponsorTestServer(t)
	if w := do(t, h, "POST", "/admin/sponsor/reached", "key=inside.example&b=llmday", admin); w.Code != http.StatusSeeOther {
		t.Fatalf("reached: %d", w.Code)
	}
	if c := s.cardOf("llmday", "inside.example"); c == nil || c.Reached.IsZero() || c.Company != "Insideco" {
		t.Fatalf("card = %+v", c)
	}
	// It is not handed out to be written for any more.
	jobs := queueJobs(t, s, h)
	if len(jobs) != 1 || len(jobs[0]["leads"].([]any)) != 1 {
		t.Fatalf("jobs = %+v", jobs)
	}
}

func TestSponsorCardNeedsText(t *testing.T) {
	s, h := sponsorTestServer(t)
	jobs := queueJobs(t, s, h)
	id := jobs[0]["id"].(string)
	// No text at all; and a text for a decision maker nothing shows to hold the role.
	answer := `{"answers":[{"id":"` + id + `","cards":[{"key":"warm.example","name":"Warmco"},
	 {"key":"inside.example","boss":{"name":"Nobody Shown","title":"CMO"},"to_boss":{"text":"Hey there."}}]}]}`
	w := do(t, h, "POST", "/api/sponsors/answers", answer, bearer(s))
	if !strings.Contains(w.Body.String(), `"kept":0`) || !strings.Contains(w.Body.String(), `"dropped":2`) {
		t.Fatalf("answers: %s", w.Body.String())
	}
	if len(s.sp.Cards["llmday"]) != 0 {
		t.Fatalf("nothing is kept without a text that can be used: %+v", s.sp.Cards["llmday"])
	}
	// Both stay on the list, waiting for the next read.
	body := do(t, h, "GET", "/sponsors?b=llmday", "", admin).Body.String()
	if !strings.Contains(body, "Warmco") || !strings.Contains(body, "Insideco") {
		t.Error("a lead without texts stays on the list")
	}
}

// Ten leads show, the rest come with Show more.
func TestSponsorShowMore(t *testing.T) {
	s, h := testServer(t)
	now := s.now()
	d := &humanData{}
	for i := 0; i < 13; i++ {
		slug := "p" + string(rune('a'+i))
		d.profiles = append(d.profiles, humanProfile{slug: slug, name: "Pat " + strings.ToUpper(slug), title: "Head of Events", company: "Co " + slug})
		d.attendees = append(d.attendees, humanAttendee{slug: slug, email: slug + "@" + slug + ".example", event: "SREday City 2026", registered: now.Add(-40 * 24 * time.Hour)})
	}
	s.cfg.HumansDB = "x"
	s.readTouches = func(context.Context, string) (*humanData, error) { return d, nil }
	s.syncTouches()
	body := do(t, h, "GET", "/sponsors", "", admin).Body.String()
	if n := strings.Count(body, `<section class="card`); n != 10 || !strings.Contains(body, `href="sponsors?b=sreday&n=20"`) || !strings.Contains(body, "13 leads") {
		t.Fatalf("first page: %d rows", n)
	}
	body = do(t, h, "GET", "/sponsors?b=sreday&n=20", "", admin).Body.String()
	if n := strings.Count(body, `<section class="card`); n != 13 || strings.Contains(body, "Show more") {
		t.Fatalf("second page: %d rows", n)
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

// The navbar carries the two big tabs, names only, with one of them on.
// The lists that had their own pages lead to the board now.
func TestNavbarTabs(t *testing.T) {
	_, h := testServer(t)
	speakers, sponsors := `<a class="on" href="./">Speakers</a>`, `<a class="on" href="sponsors">Sponsors</a>`
	for path, want := range map[string]string{"/": speakers, "/log": speakers, "/sponsors": sponsors} {
		body := do(t, h, "GET", path, "", admin).Body.String()
		if !strings.Contains(body, `<nav class="seg big">`) || !strings.Contains(body, want) {
			t.Errorf("%s: the navbar should carry %s", path, want)
		}
	}
	for _, path := range []string{"/warm", "/revive", "/room"} {
		if w := do(t, h, "GET", path, "", admin); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/snore/sponsors" {
			t.Errorf("%s: %d to %q, want a redirect to the board", path, w.Code, w.Header().Get("Location"))
		}
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
