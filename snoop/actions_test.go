package snoop

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCallActions(t *testing.T) {
	s := newTestServer(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	var fired []string
	fire := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-ant-oat01-test" || !strings.HasSuffix(r.URL.Path, "/trig_01BZoqNHJbV6v6xWAhdBg7en") {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		var in struct{ Text string }
		json.NewDecoder(r.Body).Decode(&in)
		fired = append(fired, in.Text)
		w.Write([]byte(`{"type":"routine_fire"}`))
	}))
	defer fire.Close()
	s.fireBase = fire.URL + "/%s"
	s.store.Update("c1", func(c *Call) {
		*c = Call{ID: "c1", Title: "Acme x SREday", Start: now.Add(-time.Hour), End: now.Add(-30 * time.Minute), EventID: "e1",
			Attendees:   []Attendee{{Name: "Jane Doe", Email: "jane@acme.com"}, {Email: "mark@sreday.com"}},
			ClaudeNotes: &Notes{Summary: "Acme wants a booth.", NextSteps: []string{"Mark: send the prospectus by Fri"}, By: "Claude"},
			Actions:     []CallAction{{Title: "Send Jane the Gold package", Kind: "reply", Prompt: "Email Jane the Gold package."}, {Title: "Lock SREday NYC by Friday", Kind: "reply"}, {Title: "Add Acme to the NYC sponsors page", Kind: "task", Prompt: "After the yes."}}}
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
	page := req("GET", "/admin/call/c1", nil).Body.String()
	for _, want := range []string{"After this call", "Send Jane the Gold package", "Recommended", "Your step", "Snoop: draft a follow-up", "trig_01BZoqNHJbV6v6xWAhdBg7en"} {
		if !strings.Contains(page, want) {
			t.Fatalf("call page lacks %q", want)
		}
	}
	if rec := req("POST", "/admin/call/c1/draft", url.Values{"a": {"0"}}); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("draft before setup: %d", rec.Code)
	}
	if rec := req("POST", "/admin/draft-setup", url.Values{"routine": {"trig_01BZoqNHJbV6v6xWAhdBg7en"}, "token": {"sk-ant-oat01-test"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("setup: %d %s", rec.Code, rec.Body)
	}
	if page := req("GET", "/admin/call/c1", nil).Body.String(); strings.Contains(page, "Generate token") || !strings.Contains(page, ">Other<") {
		t.Fatal("after setup the page should offer Other and no setup form")
	} else if out := os.Getenv("SNOOP_PREVIEW"); out != "" {
		os.WriteFile(out, []byte(page), 0o644) // SNOOP_PREVIEW=<file>: the call page, for a screenshot
	}
	if rec := req("POST", "/admin/call/c1/draft", url.Values{"a": {"0"}}); rec.Code != http.StatusNoContent {
		t.Fatalf("draft: %d %s", rec.Code, rec.Body)
	}
	if len(fired) != 1 || !strings.HasPrefix(fired[0], "MODE: CALL FOLLOW-UP") || !strings.Contains(fired[0], "Jane Doe <jane@acme.com>") ||
		strings.Contains(fired[0], "mark@sreday.com>") || !strings.Contains(fired[0], "The follow-up Mark picked: Send Jane the Gold package") || !strings.Contains(fired[0], "send the prospectus") {
		t.Fatalf("payload: %q", fired)
	}
	if page := req("GET", "/admin/call/c1", nil).Body.String(); !strings.Contains(page, "Claude is drafting: Send Jane the Gold package") {
		t.Fatal("no drafting state")
	}
	// A second click while Claude works doesn't fire again; Other carries Mark's words.
	req("POST", "/admin/call/c1/draft", url.Values{"note": {"Offer London instead"}})
	if len(fired) != 1 {
		t.Fatalf("fired %d times", len(fired))
	}
	now = now.Add(5 * time.Minute)
	req("POST", "/admin/call/c1/draft", url.Values{"note": {"Offer London instead"}})
	if len(fired) != 2 || !strings.Contains(fired[1], "Mark's own instruction for this email (follow it; it overrides everything else): Offer London instead") {
		t.Fatalf("other: %q", fired)
	}
}
