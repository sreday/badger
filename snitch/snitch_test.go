package snitch

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var campinas = Event{ID: "sreday/2026-campinas-q4", Brand: "sreday", Name: "SREday Campinas", Day: "2026-12-02", EndDay: "2026-12-03",
	URL: "https://www.sreday.com/2026-campinas-q4/", Base: "https://www.sreday.com/", CFP: "https://cfp.ninja/e/sreday-2026-campinas-q4", Luma: "evt-abc"}

var mlops = Event{ID: "conf42/mlops2026", Brand: "conf42", Name: "Conf42 MLOps 2026", Day: "2026-11-19", EndDay: "2026-11-19", URL: "https://www.conf42.com/mlops2026", Base: "https://www.conf42.com/"}

// testServer: Wednesday 18 Nov 2026, 10:00 UK time, two events known.
func testServer(t *testing.T) (*Server, http.Handler, *time.Time) {
	t.Helper()
	s, err := New(Config{Base: "/snitch", DataDir: t.TempDir(), AdminUser: "admin", AdminPassword: "pw", TZ: "Europe/London"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	s.events.list = []Event{campinas, mlops}
	return s, s.Handler(), &now
}

type reply struct {
	code int
	body string
	hdr  http.Header
}

func call(t *testing.T, h http.Handler, method, path string, form url.Values, set func(*http.Request)) reply {
	t.Helper()
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	r := httptest.NewRequest(method, path, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if set != nil {
		set(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return reply{w.Code, w.Body.String(), w.Header()}
}

func admin(r *http.Request) { r.SetBasicAuth("admin", "pw") }

func TestWeeks(t *testing.T) {
	mon, ok := mondayOf("2026-W47")
	if !ok || mon.Format(dayFmt) != "2026-11-16" {
		t.Fatalf("monday of W47 = %v", mon)
	}
	if id := weekID(time.Date(2026, 11, 22, 23, 0, 0, 0, time.UTC)); id != "2026-W47" {
		t.Fatalf("sunday is in %s", id)
	}
	if mon, _ := mondayOf("2027-W01"); mon.Format(dayFmt) != "2027-01-04" {
		t.Fatalf("2027-W01 starts %v", mon)
	}
	if got := weekLabel(mon); got != "Week 3 of November, 16-20 Nov" {
		t.Fatalf("label %q", got)
	}
	if fmtMin(90) != "1 h 30" || fmtMin(30) != "30 min" || fmtMin(120) != "2 h" {
		t.Fatal("fmtMin")
	}
}

func TestMissionTiming(t *testing.T) {
	get := func(id string) Mission {
		for _, m := range catalogue {
			if m.ID == id {
				return m
			}
		}
		t.Fatalf("no mission %s", id)
		return Mission{}
	}
	day := func(s string) time.Time { return parseDay(s) }
	mon := day("2026-11-16")
	today := day("2026-11-18")
	for id, want := range map[string]string{
		"ip-teasers":  "due",   // 11-18 Nov
		"ip-tomorrow": "later", // 1 Dec
		"ip-li-event": "late",  // closed 2 Nov, the event is still ahead
		"ip-decks":    "later", // after the event
		"ip-general":  "late",
	} {
		if got := get(id).status(campinas, mon, today); got != want {
			t.Errorf("%s in the week of 16 Nov: %q, want %q", id, got, want)
		}
	}
	// "This week" post: the Monday of the event's week.
	if from, to := get("ip-this-week").window(campinas); from.Format(dayFmt) != "2026-11-30" || !from.Equal(to) {
		t.Errorf("this-week window %v %v", from, to)
	}
	// The event is over: nothing to prepare any more, only the after-event work.
	mon, today = day("2026-11-30"), day("2026-12-05")
	for id, want := range map[string]string{"ip-teasers": "", "ip-tomorrow": "", "ip-speaker-posts": "", "ip-decks": "due", "ip-approve-heroes": "due"} {
		if got := get(id).status(campinas, mon, today); got != want {
			t.Errorf("%s after the event: %q, want %q", id, got, want)
		}
	}
	// A week later nothing about it is offered.
	mon, today = day("2026-12-14"), day("2026-12-14")
	for _, m := range catalogue {
		if m.fits("sreday") {
			if got := m.status(campinas, mon, today); got != "" {
				t.Errorf("%s still offered after the event's week: %q", m.ID, got)
			}
		}
	}
	// In-person and Conf42 sets never mix.
	if get("ip-teasers").fits("conf42") || get("c42-teasers").fits("sreday") || !get("c42-teasers").fits("conf42") || !get("ip-teasers").fits("pec") {
		t.Error("sets mixed")
	}
}

func TestLinks(t *testing.T) {
	var teasers, c42 Mission
	for _, m := range catalogue {
		switch m.ID {
		case "ip-teasers":
			teasers = m
		case "c42-posts":
			c42 = m
		}
	}
	if l := teasers.links(campinas); len(l) != 1 || l[0].URL != "https://www.sreday.com/2026-campinas-q4/teasers/" {
		t.Errorf("teasers link %v", l)
	}
	l := c42.links(mlops)
	if len(l) != 3 || l[0].URL != "https://www.conf42.com/mlops2026/teasers#thumbnails" || l[2].URL != "https://www.conf42.com/mlops2026" {
		t.Errorf("conf42 links %v", l)
	}
}

func TestParseEvents(t *testing.T) {
	ev, ok := parseEvent("sreday", "2026-campinas-q4", "brand_name: \"SREday\"\nbase_path: \"https://www.sreday.com/\"\nstart_time: \"2026-12-02T09:30:00-03:00\"\ncity_name: \"Campinas\"\nluma_evt: evt-oc2 # from the url\ncfp_url: https://cfp.ninja/e/sreday-2026-campinas-q4\ndays: 2\nsponsors:\n  - logo: x.png\n    days: 9\n")
	if !ok || ev.Name != "SREday Campinas" || ev.Day != "2026-12-02" || ev.EndDay != "2026-12-03" || ev.URL != "https://www.sreday.com/2026-campinas-q4/" || ev.Luma != "evt-oc2" {
		t.Fatalf("event %+v", ev)
	}
	yml := "base_path: x\nevents:\n\n# 2027\n\n  - name: DevOps\n    date: 2027-01-21\n    short_url: devops2027\n    secret_url: hush-hush\n    cfp_url: https://cfp.ninja/e/conf42-devops-2027\n    luma_url: ~\n    sponsors:\n      gold:\n        - name: nope\n\n  - name: FinOps\n    date: 2027-02-04\n    short_url: finops2027\n\npodcasts:\n  - name: Not an event\n    date: 2027-03-01\n    short_url: pod\n"
	list := parseConf42(yml)
	if len(list) != 2 || list[0].Name != "Conf42 DevOps 2027" || list[0].URL != "https://www.conf42.com/devops2027" || list[0].CFP == "" || list[1].ID != "conf42/finops2027" {
		t.Fatalf("conf42 %+v", list)
	}
	if data, _ := json.Marshal(list); strings.Contains(string(data), "hush") {
		t.Fatal("the secret preview key was read")
	}
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	in := inWindow([]Event{{ID: "a", Day: "2026-11-02", EndDay: "2026-11-02"}, {ID: "b", Day: "2026-11-07", EndDay: "2026-11-07"}, {ID: "c", Day: "2027-05-13", EndDay: "2027-05-13"}, {ID: "d", Day: "2027-06-09", EndDay: "2027-06-09"}}, now)
	if len(in) != 2 || in[0].ID != "b" || in[1].ID != "c" {
		t.Fatalf("window %+v", in)
	}
}

func TestBoardAndWorker(t *testing.T) {
	s, h, now := testServer(t)
	anya := s.st.people[0]
	base := url.Values{"p": {"anya"}, "w": {"2026-W47"}}
	do := func(action string, kv ...string) reply {
		f := url.Values{}
		for k, v := range base {
			f[k] = v
		}
		for i := 0; i+1 < len(kv); i += 2 {
			f.Set(kv[i], kv[i+1])
		}
		return call(t, h, "POST", "/admin/"+action, f, admin)
	}
	if r := call(t, h, "GET", "/", nil, nil); r.code != 401 {
		t.Fatalf("board without the password: %d", r.code)
	}
	// Monday and Tuesday are past (it is Wednesday): closed.
	if r := do("place", "mission", "g-admin", "day", "0"); r.code != 409 {
		t.Fatalf("placing on a past day: %d %s", r.code, r.body)
	}
	if r := do("place", "mission", "ip-teasers", "event", campinas.ID, "day", "2"); r.code != 204 {
		t.Fatalf("place: %d %s", r.code, r.body)
	}
	if r := do("place", "mission", "ip-teasers", "day", "2"); r.code != 400 {
		t.Fatalf("a conference mission with no event: %d", r.code)
	}
	if r := do("place", "mission", "c42-teasers", "event", campinas.ID, "day", "2"); r.code != 400 {
		t.Fatalf("a Conf42 mission on an SREday event: %d", r.code)
	}
	if r := do("lock"); r.code != 409 {
		t.Fatalf("locking a half-empty week: %d", r.code)
	}
	do("place", "mission", "g-admin", "day", "2")
	do("place", "mission", "g-newsletter", "day", "3")
	do("place", "mission", "g-outreach", "day", "4")
	// Days off: Monday and Tuesday to 0 h can't be set (past), so they count as they are.
	w := s.st.week("anya", "2026-W47")
	w.Hours[0], w.Hours[1] = 0, 0
	tease, adminT, news := w.Tasks[0], w.Tasks[1], w.Tasks[2]
	if tease.EventName != "SREday Campinas" || tease.Brand != "sreday" || len(tease.Links) != 1 || !strings.HasSuffix(tease.Links[0].URL, "/2026-campinas-q4/teasers/") {
		t.Fatalf("pill %+v", tease)
	}
	if do("minutes", "task", tease.ID, "delta", "30"); tease.Minutes != 30 {
		t.Fatalf("a mission grew into the next one: %d min", tease.Minutes)
	}
	do("move", "task", adminT.ID, "day", "2", "start", "60")
	if r := do("minutes", "task", tease.ID, "delta", "30"); r.code != 204 || tease.Minutes != 60 {
		t.Fatalf("+30: %d, %d min", r.code, tease.Minutes)
	}
	do("note", "task", tease.ID, "note", "Start with the keynotes")
	// Missions sit where they are dropped, gaps and all, and never overlap.
	if tease.Start != 0 || adminT.Start != 60 {
		t.Fatalf("Wednesday: teasers at %d, admin at %d", tease.Start, adminT.Start)
	}
	if r := do("move", "task", adminT.ID, "day", "2", "start", "200"); r.code != 204 || adminT.Start != 180 {
		t.Fatalf("move with a gap: %d, at %d", r.code, adminT.Start)
	}
	if r := do("resize", "task", tease.ID, "minutes", "400"); r.code != 204 || tease.Minutes != 180 {
		t.Fatalf("stretching into the next mission: %d, %d min", r.code, tease.Minutes)
	}
	do("resize", "task", tease.ID, "minutes", "60")
	if r := do("move", "task", adminT.ID, "day", "2", "start", "30"); r.code != 204 || adminT.Start != 60 {
		t.Fatalf("dropped onto another mission: at %d", adminT.Start)
	}
	do("move", "task", adminT.ID, "day", "2", "start", "240")

	// Not locked yet: the worker sees nothing.
	page := "/" + anya.Token
	cookie := func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: s.cookieName(anya), Value: s.cookieValue(anya)})
	}
	if r := call(t, h, "GET", page, nil, nil); r.code != 200 || !strings.Contains(r.body, "Open my week") {
		t.Fatalf("no PIN page: %d", r.code)
	}
	if r := call(t, h, "POST", page+"/pin", url.Values{"pin": {"000000x"}}, nil); r.code != 303 || !strings.Contains(r.hdr.Get("Location"), "Wrong") {
		t.Fatalf("wrong PIN: %d %s", r.code, r.hdr.Get("Location"))
	}
	if r := call(t, h, "POST", page+"/pin", url.Values{"pin": {anya.PIN}}, nil); r.code != 303 || !strings.Contains(r.hdr.Get("Set-Cookie"), s.cookieValue(anya)) {
		t.Fatalf("right PIN: %d %v", r.code, r.hdr)
	}
	if r := call(t, h, "GET", page, nil, cookie); !strings.Contains(r.body, "No schedule yet") {
		t.Fatal("an unlocked week is visible")
	}
	if r := call(t, h, "POST", page+"/start", url.Values{"task": {tease.ID}}, cookie); r.code != 404 {
		t.Fatalf("starting on an unlocked week: %d", r.code)
	}
	if r := do("lock"); r.code != 204 {
		t.Fatalf("lock: %d %s", r.code, r.body)
	}
	if r := do("minutes", "task", tease.ID, "delta", "30"); r.code != 409 {
		t.Fatalf("editing a locked week: %d", r.code)
	}
	r := call(t, h, "GET", page, nil, cookie)
	if !strings.Contains(r.body, "Share teasers with speakers") || !strings.Contains(r.body, "Start with the keynotes") || !strings.Contains(r.body, "2026-campinas-q4/teasers/") {
		t.Fatal("the locked week is not on the worker's page")
	}
	act := func(a, task string, kv ...string) reply {
		f := url.Values{"task": {task}}
		for i := 0; i+1 < len(kv); i += 2 {
			f.Set(kv[i], kv[i+1])
		}
		return call(t, h, "POST", page+"/"+a, f, cookie)
	}
	step := func(d time.Duration) { *now = now.Add(d) }

	if r := act("start", news.ID); r.code != 409 || !strings.Contains(r.body, "today") {
		t.Fatalf("starting Thursday's mission on Wednesday: %d %s", r.code, r.body)
	}
	if r := act("start", tease.ID); r.code != 204 {
		t.Fatalf("start: %d %s", r.code, r.body)
	}
	if r := act("pause", tease.ID); r.code != 429 {
		t.Fatalf("a click 0 s after the last: %d", r.code)
	}
	step(6 * time.Second)
	if r := act("start", adminT.ID); r.code != 409 || !strings.Contains(r.body, "Pause") {
		t.Fatalf("two missions at once: %d %s", r.code, r.body)
	}
	step(30 * time.Minute)
	if r := act("pause", tease.ID); r.code != 204 {
		t.Fatalf("pause: %d %s", r.code, r.body)
	}
	step(10 * time.Second)
	if r := act("start", adminT.ID); r.code != 204 {
		t.Fatalf("start the second: %d %s", r.code, r.body)
	}
	step(20 * time.Minute)
	if r := act("finish", adminT.ID, "note", ""); r.code != 400 {
		t.Fatalf("finish with no note: %d", r.code)
	}
	if r := act("finish", adminT.ID, "note", "Inbox cleared", "link", "ftp://x"); r.code != 400 {
		t.Fatalf("finish with a bad link: %d", r.code)
	}
	if r := act("finish", adminT.ID, "note", "Inbox cleared", "link", "https://example.com/x"); r.code != 204 {
		t.Fatalf("finish: %d %s", r.code, r.body)
	}
	if !adminT.Done || adminT.Running() || int(adminT.Spent(*now).Minutes()) != 20 {
		t.Fatalf("finished task %+v", adminT)
	}
	step(10 * time.Second)
	if r := act("start", adminT.ID); r.code != 409 {
		t.Fatalf("restarting a finished mission: %d", r.code)
	}
	if got := int(tease.Spent(*now).Minutes()); got != 30 {
		t.Fatalf("teasers logged %d min", got)
	}

	// The plan: Wednesday is started, so it stays; Thursday can still change.
	do("unlock")
	if r := do("move", "task", tease.ID, "day", "3"); r.code != 409 {
		t.Fatalf("moving a mission off a started day: %d", r.code)
	}
	if r := do("place", "mission", "g-meetup", "day", "2"); r.code != 409 {
		t.Fatalf("adding to a started day: %d", r.code)
	}
	if r := do("move", "task", news.ID, "day", "4"); r.code != 204 || news.Day != 4 {
		t.Fatalf("moving Thursday's mission: %d", r.code)
	}
	// Unlocked: the started day stays on the worker's page, the rest is hidden.
	r = call(t, h, "GET", page, nil, cookie)
	if !strings.Contains(r.body, "Share teasers with speakers") || strings.Contains(r.body, "Newsletter") {
		t.Fatal("unlocked: the started day should stay visible, the others not")
	}

	// The log has it all, and the board's Log view draws it.
	var kinds []string
	for _, e := range s.st.readLog("anya", time.Time{}, now.Add(time.Hour)) {
		kinds = append(kinds, e.Kind)
	}
	got := strings.Join(kinds, " ")
	for _, want := range []string{"pin_fail", "pin_ok", "lock", "start", "pause", "finish", "unlock"} {
		if !strings.Contains(got, want) {
			t.Errorf("log misses %s: %s", want, got)
		}
	}
	r = call(t, h, "GET", "/?p=anya&w=2026-W47&view=log", nil, admin)
	if r.code != 200 || !strings.Contains(r.body, "Inbox cleared") || !strings.Contains(r.body, "started Share teasers") {
		t.Fatalf("log view: %d", r.code)
	}
	r = call(t, h, "GET", "/?p=magda&w=2026-W47", nil, admin)
	if r.code != 200 || !strings.Contains(r.body, ">Campinas<") || !strings.Contains(r.body, ">MLOps 2026<") {
		t.Fatalf("schedule view: %d", r.code)
	}
	// Magda gets the same mission: the hint says Anya has it.
	base.Set("p", "magda")
	do("place", "mission", "ip-teasers", "event", campinas.ID, "day", "3")
	r = call(t, h, "GET", "/?p=magda&w=2026-W47", nil, admin)
	if !strings.Contains(r.body, "Anya already has") {
		t.Fatal("no hint about Anya's mission")
	}
}

func TestAwayAndOldClocks(t *testing.T) {
	s, h, now := testServer(t)
	anya := s.st.people[0]
	w := s.st.week("anya", "2026-W47")
	w.Locked = true
	w.Tasks = []*Task{{ID: "t1", Mission: "g-admin", Name: "Admin / other", Day: 2, Minutes: 60}}
	s.st.saveWeek(w)
	cookie := func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: s.cookieName(anya), Value: s.cookieValue(anya)})
	}
	page := "/" + anya.Token
	call(t, h, "GET", page, nil, cookie)
	if r := call(t, h, "POST", page+"/start", url.Values{"task": {"t1"}}, cookie); r.code != 204 {
		t.Fatalf("start: %d %s", r.code, r.body)
	}
	*now = now.Add(time.Minute)
	call(t, h, "POST", page+"/ping", nil, cookie)
	*now = now.Add(3 * time.Hour) // the page was closed, the clock ran
	call(t, h, "POST", page+"/ping", nil, cookie)
	var kinds []string
	for _, e := range s.st.readLog("anya", time.Time{}, now.Add(time.Hour)) {
		kinds = append(kinds, e.Kind)
	}
	if got := strings.Join(kinds, " "); !strings.Contains(got, "away back") {
		t.Fatalf("no away/back in the log: %s", got)
	}
	// The week ends with the clock running: Snitch stops it.
	*now = time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	s.tick()
	if w.Tasks[0].Running() {
		t.Fatal("the clock still runs a week later")
	}
}

func TestClaudeJobs(t *testing.T) {
	s, h, _ := testServer(t)
	bearer := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+s.token) }
	if r := call(t, h, "GET", "/api/queue", nil, nil); r.code != 401 {
		t.Fatalf("queue without the token: %d", r.code)
	}
	f := url.Values{"p": {"anya"}, "w": {"2026-W47"}}
	call(t, h, "POST", "/admin/place", url.Values{"p": {"anya"}, "w": {"2026-W47"}, "mission": {"g-admin"}, "day": {"2"}}, admin)
	call(t, h, "POST", "/admin/summarise", f, admin)
	call(t, h, "POST", "/admin/summarise", f, admin) // asked twice, queued once
	call(t, h, "POST", "/admin/autofill", f, admin)
	r := call(t, h, "GET", "/api/queue", nil, bearer)
	var q struct {
		Instructions string
		Jobs         []map[string]any
	}
	if err := json.Unmarshal([]byte(r.body), &q); err != nil || len(q.Jobs) != 2 || !strings.Contains(q.Instructions, "autofill") {
		t.Fatalf("queue: %v, %d jobs", err, len(q.Jobs))
	}
	if !strings.Contains(r.body, "ip-teasers") || !strings.Contains(r.body, campinas.ID) {
		t.Fatal("auto-fill got no candidates")
	}
	ans := map[string]any{"model": "test", "answers": []map[string]any{
		{"id": q.Jobs[0]["id"], "summary": "Wed 10:00-10:30 Anya did Admin"},
		{"id": q.Jobs[1]["id"], "tasks": []map[string]any{
			{"mission": "ip-teasers", "event": campinas.ID, "day": 3, "minutes": 95, "note": "first"},
			{"mission": "g-admin", "day": 0, "minutes": 60},                       // a past day: skipped
			{"mission": "nope", "day": 4, "minutes": 60},                          // unknown: skipped
			{"mission": "c42-teasers", "event": campinas.ID, "day": 4},            // wrong set: skipped
			{"mission": "c42-factory", "event": mlops.ID, "day": 4, "minutes": 0}, // default time kept
		}},
	}}
	data, _ := json.Marshal(ans)
	r = call(t, h, "POST", "/api/answers", nil, func(req *http.Request) {
		bearer(req)
		req.Body = http.NoBody
		*req = *httptest.NewRequest("POST", "/api/answers", strings.NewReader(string(data)))
		bearer(req)
	})
	if r.code != 200 || !strings.Contains(r.body, `"taken":2`) {
		t.Fatalf("answers: %d %s", r.code, r.body)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "summaries", "anya", "2026-W47.json")); err != nil {
		t.Fatal("no summary written")
	}
	w := s.st.week("anya", "2026-W47")
	if len(w.Tasks) != 3 || w.Tasks[1].Minutes != 90 || w.Tasks[1].Day != 3 || w.Tasks[2].Mission != "c42-factory" || w.Tasks[2].Minutes != 30 {
		t.Fatalf("auto-fill drafted %d tasks: %+v", len(w.Tasks), w.Tasks)
	}
	if r := call(t, h, "GET", "/api/queue", nil, bearer); !strings.Contains(r.body, `"jobs":[]`) {
		t.Fatal("answered jobs are still queued")
	}
	// Lessons are kept; every mission still starts at half an hour.
	s.mu.Lock()
	s.addJob(&Job{Kind: "lessons", Week: "2026-W46"}, s.now())
	id := s.jobs[len(s.jobs)-1].ID
	s.mu.Unlock()
	data, _ = json.Marshal(map[string]any{"answers": []map[string]any{{"id": id, "lessons": []string{"Teasers take longer"}, "minutes": map[string]int{"ip-teasers": 125, "nope": 60}}}})
	call(t, h, "POST", "/api/answers", nil, func(req *http.Request) {
		*req = *httptest.NewRequest("POST", "/api/answers", strings.NewReader(string(data)))
		bearer(req)
	})
	if m, _ := s.mission("ip-teasers"); m.Minutes != 30 || len(s.lessons.Lines) != 1 {
		t.Fatalf("lessons: %d min, %v", m.Minutes, s.lessons.Lines)
	}
}
