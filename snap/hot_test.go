package snap

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestServer(t *testing.T, now time.Time) *Server {
	t.Helper()
	s, err := New(Config{Base: "/snap", DataDir: t.TempDir(), AdminPassword: "pw", Account: "mark@sreday.com", Every: time.Hour, TeamDomains: team, TZ: "Europe/London"})
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now }
	return s
}

// flagged is a loose end Claude flagged, last written by someone else.
func flagged(id, cat, pri string, last time.Time) Thread {
	return Thread{ID: id, Subject: "Subject " + id, HistoryID: "1", ClaudeHistory: "1", AnalyzedHistory: "1", AnalyzedBy: "claude", AnalyzedAt: last,
		LastAt: last, LastFrom: "Jane", Waiting: "you", Category: cat, Priority: pri, Loose: true, Label: "Label " + id,
		People: []Person{{Name: "Jane", Email: "jane@" + id + ".com"}}}
}

func TestHotMembership(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	hot := flagged("a", "sponsors", "high", now.Add(-2*time.Hour))
	if !hot.Hot(now) {
		t.Fatal("fresh, flagged, ball with Mark: should be hot")
	}
	cases := map[string]func(th *Thread){
		"Mark replied":      func(th *Thread) { th.LastFrom = "me" },
		"waiting on them":   func(th *Thread) { th.Waiting = "them" },
		"between others":    func(th *Thread) { th.Waiting = "between" },
		"8 days old":        func(th *Thread) { th.LastAt = now.Add(-8 * day) },
		"snoozed":           func(th *Thread) { th.SnoozeUntil = now.Add(day) },
		"closed":            func(th *Thread) { th.Done = true },
		"dead end":          func(th *Thread) { th.DeadEnd = true },
		"not flagged":       func(th *Thread) { th.Loose = false },
		"only the rules":    func(th *Thread) { th.AnalyzedBy = "rules" },
		"follow-up to come": func(th *Thread) { th.FollowUp = now.Add(30 * day).Format("2006-01-02") },
	}
	for name, change := range cases {
		th := hot
		change(&th)
		if th.Hot(now) {
			t.Errorf("%s: still hot", name)
		}
	}
}

func TestRipeFastLane(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	in := &Thread{LastAt: now.Add(-1 * time.Minute), LastFrom: "Jane", Waiting: "you"}
	if in.Ripe(now) {
		t.Error("a minute old: should settle first")
	}
	in.LastAt = now.Add(-3 * time.Minute)
	if !in.Ripe(now) {
		t.Error("fresh mail from a person should be ripe after the settle")
	}
	speaker := &Thread{LastAt: now.Add(-10 * time.Minute), LastFrom: "Ann", Category: "speakers"}
	if !speaker.Ripe(now) {
		t.Error("a fresh speaker mail should not wait 48 hours any more")
	}
	mine := &Thread{LastAt: now.Add(-3 * time.Hour), LastFrom: "me", Waiting: "them"}
	if mine.Ripe(now) {
		t.Error("Mark's own send is not ripe until it has gone quiet")
	}
	mine.LastAt = now.Add(-15 * 24 * time.Hour)
	if !mine.Ripe(now) {
		t.Error("two weeks quiet: ripe, as before")
	}
	old := &Thread{LastAt: now.Add(-9 * 24 * time.Hour), LastFrom: "Jane", Waiting: "you"}
	if !old.Ripe(now) {
		t.Error("an old unanswered thread: ripe, as before")
	}
	again := &Thread{LastAt: now.Add(-3 * time.Hour), LastFrom: "me", AnalyzedBy: "claude", ClaudeHistory: ""}
	if !again.Ripe(now) {
		t.Error("a thread sent back to Claude on purpose is ripe at once")
	}
}

func TestOrderTab(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	mk := func(id, pri string, age time.Duration) Row {
		return Row{Thread: Thread{ID: id, Priority: pri, LastAt: now.Add(-age)}, Fresh: now.Add(-age), State: "open"}
	}
	rows := []Row{
		mk("low-old", "low", 200*day),
		mk("high-old", "high", 90*day),
		mk("med-fresh", "medium", 2*day),
		mk("high-fresh-2", "high", 5*day),
		mk("low-fresh", "low", 1*day),
		mk("high-fresh-1", "high", 1*day),
		mk("med-old", "medium", 60*day),
		mk("high-older", "high", 120*day),
	}
	got := orderTab(rows, now)
	var ids []string
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	want := "high-fresh-1 high-fresh-2 med-fresh low-fresh | high-old high-older med-old low-old"
	have := strings.Join(ids[:4], " ") + " | " + strings.Join(ids[4:], " ")
	if have != want {
		t.Fatalf("order:\n got %s\nwant %s", have, want)
	}
	if !got[4].Sep || got[0].Sep || got[5].Sep {
		t.Error("the divider belongs on the first card after the top block")
	}
	// More than five fresh ones: only five make the top block, the rest
	// fall back into high, medium, low.
	var many []Row
	for i := 0; i < 7; i++ {
		many = append(many, mk(fmt.Sprintf("m%d", i), "medium", time.Duration(i+1)*day))
	}
	many = append(many, mk("h-old", "high", 100*day))
	got = orderTab(many, now)
	if got[5].ID != "h-old" || !got[5].Sep || got[6].ID != "m5" {
		t.Fatalf("after the top five: %s, %s", got[5].ID, got[6].ID)
	}
	// A cancelled speaker still on the site leads, whatever its age.
	listed := mk("listed", "low", 80*day)
	listed.StillListed = "Still on SREday NYC"
	got = orderTab([]Row{mk("x", "medium", day), listed}, now)
	if got[0].ID != "listed" {
		t.Error("a speaker still listed should lead the tab")
	}
}

func TestHotPlateOnThePage(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	s := newTestServer(t, now)
	// Seven hot sponsor threads: five on the plate, two wait in Sponsors.
	for i := 0; i < 7; i++ {
		pri := "medium"
		if i >= 5 {
			pri = "low" // h5 and h6 are the least pressing: they wait
		}
		th := flagged(fmt.Sprintf("h%d", i), "sponsors", pri, now.Add(-time.Duration(i+1)*time.Hour))
		s.store.Update(th.ID, func(x *Thread) { *x = th })
	}
	cold := flagged("cold", "sponsors", "high", now.AddDate(0, 0, -40))
	s.store.Update("cold", func(x *Thread) { *x = cold })
	old := flagged("misc", "others", "medium", now.AddDate(0, 0, -20)) // from the old Other tab
	s.store.Update("misc", func(x *Thread) { *x = old })
	h := s.Handler()
	page := func(path string) string { return call(t, h, "GET", path, "pw", "").Body.String() }
	has := func(body, id string) bool { return strings.Contains(body, `data-id="`+id+`"`) }

	body := page("/")
	if !strings.Contains(body, `class="on hot" data-cat="hot"`) {
		t.Fatal("Hot is not the default tab")
	}
	if strings.Contains(body, ">Other<") || strings.Contains(body, `data-cat="others"`) {
		t.Error("the Other tab is still there")
	}
	for i := 0; i < 5; i++ {
		if !has(body, fmt.Sprintf("h%d", i)) {
			t.Errorf("h%d should be on the plate", i)
		}
	}
	if has(body, "h5") || has(body, "h6") || has(body, "cold") {
		t.Error("the plate holds more than its five hot threads")
	}
	if !strings.Contains(body, "2 more hot threads waiting") || !strings.Contains(body, `data-hot="5"`) {
		t.Error("no word on the two that wait, or no count")
	}
	sp := page("/?view=sponsors")
	if has(sp, "h0") || !has(sp, "h5") || !has(sp, "h6") || !has(sp, "cold") {
		t.Error("Sponsors should hold the two waiting hot threads and the old one, not the plate's")
	}
	if !strings.Contains(sp, "hotmark") {
		t.Error("the waiting ones aren't marked hot")
	}
	if ev := page("/?view=events"); !has(ev, "misc") {
		t.Error("a thread from the old Other tab should sit in Events")
	}
	// Close one on the plate: the next one moves up, the closed one sits in
	// Sponsors' Closed list.
	if rec := call(t, h, "POST", "/admin/thread/h0/state", "pw", "s=closed"); rec.Code != 204 {
		t.Fatalf("close: %d", rec.Code)
	}
	body = page("/")
	if has(body, "h0") || !has(body, "h5") {
		t.Error("closing a hot thread should pull the next one onto the plate")
	}
	if !has(page("/?view=sponsors&st=closed"), "h0") {
		t.Error("the closed one should be in Sponsors, Closed")
	}
	// Snooze another: same, in Snoozed.
	call(t, h, "POST", "/admin/thread/h1/state", "pw", "s=snoozed")
	body = page("/")
	if has(body, "h1") || !has(body, "h6") || strings.Contains(body, "more hot thread") {
		t.Error("snoozing should pull the last waiting one in")
	}
	if !has(page("/?view=sponsors&st=snoozed"), "h1") {
		t.Error("the snoozed one should be in Sponsors, Snoozed")
	}
	// The Active / Snoozed / Closed switch is on Hot too; there Snoozed and
	// Closed list every tab's.
	if hs := page("/?view=hot&st=snoozed"); !has(hs, "h1") || has(hs, "h2") || !strings.Contains(hs, `data-st="closed"`) {
		t.Error("Hot, Snoozed should list the snoozed threads of all tabs")
	}
	if hc := page("/?view=hot&st=closed"); !has(hc, "h0") {
		t.Error("Hot, Closed should list the closed threads of all tabs")
	}
	// Mark answers one in Gmail: off the plate.
	s.store.Update("h2", func(x *Thread) { x.LastFrom, x.LastAt = "me", now })
	if has(page("/"), "h2") {
		t.Error("a thread Mark answered is still hot")
	}
	// A search looks everywhere.
	if q := page("/?q=Subject+cold"); !has(q, "cold") {
		t.Error("search from Hot should find threads in the tabs")
	}
}

func TestFireOnArrival(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	s := newTestServer(t, now)
	fires, status := 0, http.StatusOK
	var text string
	fire := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+readRoutineDefault || r.Header.Get("Authorization") != "Bearer sk-ant-oat01-rrrrrrrrrrrrrrrrrrrr" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		var in struct{ Text string }
		json.NewDecoder(r.Body).Decode(&in)
		text = in.Text
		fires++
		w.WriteHeader(status)
		w.Write([]byte(`{"type":"routine_fire"}`))
	}))
	defer fire.Close()
	s.fireBase = fire.URL + "/%s"
	fresh := Thread{ID: "f", Subject: "Gold package?", HistoryID: "2", LastAt: now.Add(-3 * time.Minute), LastFrom: "Jane", Waiting: "you", AnalyzedBy: "rules", AnalyzedHistory: "2"}
	s.store.Update("f", func(x *Thread) { *x = fresh })
	s.excerpts["f"] = cached{"2", "<thread id=\"f\">...</thread>\n"}

	s.fireIfHot(now)
	if fires != 0 {
		t.Fatal("fired without a token")
	}
	h := s.Handler()
	if rec := call(t, h, "POST", "/admin/read-token", "pw", "token=short"); rec.Code != 400 {
		t.Fatalf("a bad token was taken: %d", rec.Code)
	}
	if rec := call(t, h, "POST", "/admin/read-token", "pw", "token=sk-ant-oat01-rrrrrrrrrrrrrrrrrrrr"); rec.Code != 204 {
		t.Fatalf("token: %d", rec.Code)
	}
	s.fireIfHot(now)
	if fires != 1 || !strings.HasPrefix(text, hotMarker) {
		t.Fatalf("fresh mail in the queue should start the routine once: %d %q", fires, text)
	}
	s.fireIfHot(now.Add(2 * time.Minute))
	if fires != 1 {
		t.Fatal("fired again within five minutes")
	}
	s.fireIfHot(now.Add(6 * time.Minute))
	if fires != 2 {
		t.Fatal("still waiting after five minutes: should start it again")
	}
	// The routine is at work (it fetched the queue a minute ago): no start.
	later := now.Add(20 * time.Minute)
	s.mu.Lock()
	s.claude.LastFetch = later.Add(-time.Minute)
	s.mu.Unlock()
	s.fireIfHot(later)
	if fires != 2 {
		t.Fatal("started the routine while it was reading")
	}
	// The queue leads with the fresh thread.
	oldOne := Thread{ID: "o", Subject: "Sponsorship", HistoryID: "2", LastAt: now.AddDate(0, 0, -20), LastFrom: "Bo", Waiting: "you", Sponsor: true, Category: "sponsors", AnalyzedBy: "rules", AnalyzedHistory: "2"}
	s.store.Update("o", func(x *Thread) { *x = oldOne })
	s.excerpts["o"] = cached{"2", "<thread id=\"o\">...</thread>\n"}
	if q := s.queue(0); len(q) != 2 || q[0].ID != "f" {
		t.Fatalf("queue order: %+v", q)
	}
	// Once Claude has read it, nothing waits: no start.
	s.store.Update("f", func(x *Thread) { x.ClaudeHistory, x.AnalyzedBy = "2", "claude" })
	s.fireIfHot(now.Add(2 * time.Hour))
	if fires != 2 {
		t.Fatal("fired with no fresh mail waiting")
	}
	if c := s.claudeInfo(); c.Fires != 2 || c.LastProblem != "" {
		t.Fatalf("claude info: %+v", c)
	}
}

func TestNoOtherTab(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	s, err := New(Config{Base: "/snap", DataDir: dir, AdminPassword: "pw", Account: "mark@sreday.com", Every: time.Hour, TeamDomains: team, TZ: "Europe/London"})
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now }
	s.store.Update("x", func(th *Thread) {
		*th = flagged("x", "others", "low", now.AddDate(0, 0, -20))
		th.MovedTo = "others"
	})
	st, err := OpenStore(dir + "/snap")
	if err != nil {
		t.Fatal(err)
	}
	if th, _ := st.Get("x"); th.Category != "events" || th.MovedTo != "" {
		t.Fatalf("an Other thread should load as Events, free to be sorted again: %q %q", th.Category, th.MovedTo)
	}
	if validCategory("others") || !validCategory("events") {
		t.Fatal("categories")
	}
	th := &Thread{}
	applyAnalysis(th, Analysis{Category: "others", Loose: true}, "1", "claude", now)
	if th.Category != "events" {
		t.Fatalf("Claude's old 'others' should land in Events: %q", th.Category)
	}
	if rec := call(t, s.Handler(), "POST", "/admin/thread/x/move", "pw", "cat=others"); rec.Code != 400 {
		t.Fatalf("move to Other accepted: %d", rec.Code)
	}
	if strings.Contains(Instructions, `"others"`) {
		t.Error("the instructions still offer the others category")
	}
}

// Marek 2026-11-14: a sponsor with no budget is always low stakes.
func TestNoBudgetIsLow(t *testing.T) {
	now := time.Now()
	th := &Thread{ID: "nb"}
	applyAnalysis(th, Analysis{Category: "sponsors", Loose: true, Label: "Acme has no budget", Priority: "high", NoBudget: true}, "1", "claude", now)
	if th.Priority != "low" || !th.NoBudget {
		t.Fatalf("sponsor with no budget: priority %q, no_budget %v", th.Priority, th.NoBudget)
	}
	applyAnalysis(th, Analysis{Category: "sponsors", Loose: true, Label: "Acme found budget", Priority: "high"}, "2", "claude", now)
	if th.Priority != "high" || th.NoBudget {
		t.Fatalf("budget appeared: priority %q, no_budget %v", th.Priority, th.NoBudget)
	}
	sp := &Thread{ID: "sp"}
	applyAnalysis(sp, Analysis{Category: "speakers", Loose: true, Label: "Ann cancelled", Priority: "high", NoBudget: true}, "1", "claude", now)
	if sp.Priority != "high" || sp.NoBudget {
		t.Fatalf("only sponsors: priority %q, no_budget %v", sp.Priority, sp.NoBudget)
	}
}
