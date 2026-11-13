package snoop

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestDeadEndAndKinds(t *testing.T) {
	s := newTestServer(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	talk := []Line{{At: now.Add(-50 * time.Hour), Speaker: "Jane", Text: "We would like a booth at the conference next spring, with two passes for the team."},
		{At: now.Add(-50 * time.Hour), Speaker: "Mark", Text: "Sounds good, I will send the prospectus and the floor plan tomorrow morning."}}
	mk := func(id string, calendly bool, change func(c *Call)) {
		s.store.Update(id, func(c *Call) {
			*c = Call{ID: id, Title: "Call " + id, MeetingCode: "aaa-bbbb-" + id, Start: now.Add(-50 * time.Hour), End: now.Add(-49 * time.Hour), EventID: id, Calendly: calendly,
				Attendees: []Attendee{{Name: "Jane", Email: "jane@" + id + ".com"}}, APILines: talk, APINotes: &Notes{Summary: "Talked.", NextSteps: []string{"Mark: follow up with " + id}}}
			if change != nil {
				change(c)
			}
		})
	}
	mk("good", true, nil)
	mk("gone", true, func(c *Call) { c.Cancelled = true })
	mk("nope", true, func(c *Call) { c.OthersDeclined, c.APILines, c.APINotes = true, nil, nil })
	mk("misc", false, nil)
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
	results := func(path string) string {
		body := req("GET", path, nil).Body.String()
		i, j := strings.Index(body, `<div id="results">`), strings.Index(body, `<section id="actions"`)
		if i < 0 || j < i {
			t.Fatalf("%s: no results on the page", path)
		}
		return body[i:j]
	}
	has := func(html, id string) bool { return strings.Contains(html, "/admin/call/"+id+`"`) }

	// The navbar: five tabs, four kinds, the search; no switch any more.
	page := req("GET", "/", nil).Body.String()
	nav := page[strings.Index(page, `class="bar navbar"`):strings.Index(page, "<main>")]
	for _, want := range []string{">Calendly<", ">Luma<", ">Recurring<", ">Other<", ">Dead end<", ">All<", ">Declined / Cancelled<", ">Empty<", `id="q"`} {
		if !strings.Contains(nav, want) {
			t.Errorf("the navbar lacks %q", want)
		}
	}
	if strings.Contains(page, "Show declined") {
		t.Error("the old switch is still there")
	}
	if strings.Index(nav, ">Dead end<") > strings.Index(nav, `id="q"`) {
		t.Error("the search should sit under the tabs")
	}
	// All is the default: the calls that count. The others are sorted into their own tabs.
	if r := results("/"); !has(r, "good") || has(r, "gone") || has(r, "nope") || has(r, "misc") {
		t.Fatal("Calendly, All: only the call that counts")
	}
	if r := results("/?kind=cancelled"); !has(r, "gone") || !has(r, "nope") || has(r, "good") { // the old link still works
		t.Fatal("the old Cancelled link should show the merged tab")
	}
	if r := results("/?kind=declined"); !has(r, "nope") || !has(r, "gone") || has(r, "good") {
		t.Fatal("Declined / Cancelled should list both, and not the call that counts")
	}
	if r := results("/?view=other"); !has(r, "misc") || has(r, "good") {
		t.Fatal("Other keeps its name and its calls")
	}
	if r := results("/?view=dead"); !strings.Contains(r, "Nothing in the bin") {
		t.Fatal("an empty bin should say so")
	}
	// Rows can be dragged to the bin.
	if r := results("/"); !strings.Contains(r, `draggable="true" data-dead="/snoop/admin/call/good/dead"`) {
		t.Fatal("a call row isn't draggable to Dead end")
	}
	open := func() string {
		o, _ := s.openActions(now)
		var ids []string
		for _, a := range o {
			ids = append(ids, a.Call.ID)
		}
		return strings.Join(ids, ",")
	}
	if !strings.Contains(open(), "good") {
		t.Fatalf("actions before: %s", open())
	}
	// Into the bin: off its tab, off the actions board, out of what Snap is told.
	if rec := req("POST", "/admin/call/good/dead", url.Values{"dead": {"1"}}); rec.Code != http.StatusNoContent {
		t.Fatalf("dead: %d", rec.Code)
	}
	if has(results("/"), "good") || strings.Contains(open(), "good") {
		t.Fatal("a call in the bin is still on its tab or the actions board")
	}
	for _, p := range s.OpenPromises() {
		if p.ID == "good" {
			t.Fatal("a call in the bin is still offered to Snap as a promise")
		}
	}
	bin := results("/?view=dead")
	if !has(bin, "good") || !strings.Contains(bin, "Bring back to Calendly") {
		t.Fatal("the bin should hold the call, with the way back")
	}
	// A cancelled call from Other goes in too, and says where it came from.
	req("POST", "/admin/call/misc/dead", url.Values{"dead": {"1"}})
	if bin = results("/?view=dead"); !has(bin, "misc") || !strings.Contains(bin, "Bring back to Other") {
		t.Fatal("the bin should say Bring back to Other")
	}
	// And back.
	if rec := req("POST", "/admin/call/good/dead", url.Values{"dead": {"0"}}); rec.Code != http.StatusNoContent {
		t.Fatalf("bring back: %d", rec.Code)
	}
	if !has(results("/"), "good") || has(results("/?view=dead"), "good") || !strings.Contains(open(), "good") {
		t.Fatal("brought back, the call should be on Calendly and the actions board again")
	}
	// Luma events go in the bin by their page.
	s.lumaMu.Lock()
	s.luma = []LumaEvent{{Title: "Conf42 Webinar", URL: "https://lu.ma/abc", Start: now.Add(24 * time.Hour), End: now.Add(25 * time.Hour), Online: true}}
	s.lumaAt = now
	s.lumaMu.Unlock()
	if r := results("/?view=luma"); !strings.Contains(r, "Conf42 Webinar") || !strings.Contains(r, `data-luma="https://lu.ma/abc"`) {
		t.Fatal("the Luma event should be on its tab, draggable")
	}
	if rec := req("POST", "/admin/luma/dead", url.Values{"url": {"https://lu.ma/abc"}, "dead": {"1"}}); rec.Code != http.StatusNoContent {
		t.Fatalf("luma dead: %d", rec.Code)
	}
	if strings.Contains(results("/?view=luma"), "Conf42 Webinar") {
		t.Fatal("a Luma event in the bin is still on the Luma tab")
	}
	if bin = results("/?view=dead"); !strings.Contains(bin, "Conf42 Webinar") || !strings.Contains(bin, "Bring back to Luma") {
		t.Fatal("the bin should hold the Luma event")
	}
	// The bin survives a restart.
	s2, err := New(s.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !s2.dead.has("https://lu.ma/abc") {
		t.Fatal("the bin forgot the Luma event")
	}
	if c, _ := s2.store.Get("misc"); !c.DeadEnd {
		t.Fatal("the bin forgot the call")
	}
}
