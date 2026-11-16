package snoop

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// A call in a language Meet can't transcribe takes its words from the
// captions; the rest of the series follows; the menu is on every call.
func TestCallLanguage(t *testing.T) {
	s := newTestServer(t)
	now := time.Date(2026, 11, 5, 15, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	api := []Line{{Speaker: "Alex Example", Text: "nonsense heard as English", At: now.Add(-50 * time.Minute)}}
	ext := []Line{{Speaker: "Alex Example", Text: "Dzień dobry, zaczynamy", At: now.Add(-50 * time.Minute)}}
	s.store.Update("c1", func(c *Call) {
		c.Title, c.MeetingCode, c.Series = "Weekly", "abc-defg-hij", "ser1"
		c.Start, c.End = now.Add(-time.Hour), now.Add(-30*time.Minute)
		c.APILines, c.ExtLines = api, ext
		c.APINotes = &Notes{Summary: "A summary of the nonsense"}
	})
	s.store.Update("c2", func(c *Call) { // next week's, same series
		c.Title, c.MeetingCode, c.Series = "Weekly", "abc-defg-hij", "ser1"
		c.Start, c.End = now.Add(7*24*time.Hour), now.Add(7*24*time.Hour+30*time.Minute)
	})
	s.store.Update("c0", func(c *Call) { // last week's: over, stays as it was
		c.Title, c.MeetingCode, c.Series = "Weekly", "abc-defg-hij", "ser1"
		c.Start, c.End = now.Add(-7*24*time.Hour), now.Add(-7*24*time.Hour+30*time.Minute)
	})
	do := func(method, path string, form url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.SetBasicAuth("", "pw")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}

	c, _ := s.store.Get("c1")
	if got := c.Lines(); len(got) != 1 || got[0].Text != api[0].Text {
		t.Fatalf("before: %+v", got)
	}
	page := do("GET", "/admin/call/c1", nil)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `<option value="pl">Polish</option>`) || !strings.Contains(page.Body.String(), "/snoop/admin/extension.zip") {
		t.Fatalf("call page without the language menu or the download: %d", page.Code)
	}

	if w := do("POST", "/admin/call/c1/lang", url.Values{"lang": {"pl"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("set language: %d", w.Code)
	}
	c, _ = s.store.Get("c1")
	if got := c.Lines(); c.Lang != "pl" || len(got) != 1 || got[0].Text != ext[0].Text {
		t.Errorf("Polish call: lang %q, lines %+v", c.Lang, got)
	}
	if c.Notes() != nil || !strings.Contains(c.TranscriptSource(), "Polish") {
		t.Errorf("Polish call: notes %+v, source %q", c.Notes(), c.TranscriptSource())
	}
	if next, _ := s.store.Get("c2"); next.Lang != "pl" {
		t.Errorf("next in the series: %q", next.Lang)
	}
	if last, _ := s.store.Get("c0"); last.Lang != "" {
		t.Errorf("a past call of the series changed: %q", last.Lang)
	}
	if page := do("GET", "/admin/call/c1", nil); !strings.Contains(page.Body.String(), `<option value="pl" selected>Polish</option>`) {
		t.Error("the menu doesn't show Polish as picked")
	}

	if w := do("POST", "/admin/call/c1/lang", url.Values{"lang": {"xx"}}); w.Code != http.StatusNotFound {
		t.Errorf("unknown language: %d", w.Code)
	}
	do("POST", "/admin/call/c1/lang", url.Values{"lang": {""}})
	if c, _ = s.store.Get("c1"); c.Lang != "" || c.Lines()[0].Text != api[0].Text {
		t.Errorf("back to Meet's own: %q", c.Lang)
	}
}

func TestExtensionZip(t *testing.T) {
	s := newTestServer(t)
	r := httptest.NewRequest("GET", "/admin/extension.zip", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("without the password: %d", w.Code)
	}
	r.SetBasicAuth("", "pw")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	z, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if w.Code != http.StatusOK || err != nil {
		t.Fatalf("zip: %d, %v", w.Code, err)
	}
	have := map[string]bool{}
	for _, f := range z.File {
		have[f.Name] = true
	}
	for _, want := range []string{"snoop-extension/manifest.json", "snoop-extension/content.js", "snoop-extension/background.js"} {
		if !have[want] {
			t.Errorf("%s is not in the zip (%d files)", want, len(z.File))
		}
	}
}
