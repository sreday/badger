package snap

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Claude answers a form lead in the lead's own follow-up thread, not in the
// form thread Snap asked about: the wheel must stop anyway.
func TestDraftInAnotherThread(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	drafts := []map[string]any{{"id": "old", "message": map[string]any{"id": "m0", "threadId": "elsewhere"}}}
	sent := false
	fire := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) }))
	defer fire.Close()
	hdr := func(kv ...string) []map[string]string {
		var out []map[string]string
		for i := 0; i < len(kv); i += 2 {
			out = append(out, map[string]string{"name": kv[i], "value": kv[i+1]})
		}
		return out
	}
	gm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch p := r.URL.Path; {
		case p == "/drafts":
			json.NewEncoder(w).Encode(map[string]any{"drafts": drafts})
		case strings.HasPrefix(p, "/drafts/"):
			json.NewEncoder(w).Encode(map[string]any{"id": strings.TrimPrefix(p, "/drafts/"), "message": map[string]any{"id": "m1", "threadId": "reply",
				"payload": map[string]any{"mimeType": "text/plain", "headers": hdr("Subject", "Re: Sponsoring", "To", "Jane Doe <jane@acme.com>"),
					"body": map[string]any{"data": "SGV5IEphbmUsIGhhcHB5IHRvIGNoYXQu"}}}})
		case p == "/threads/reply" && sent:
			json.NewEncoder(w).Encode(map[string]any{"id": "reply", "historyId": "9", "messages": []any{map[string]any{"id": "s1", "labelIds": []string{"SENT"},
				"internalDate": "1794571800000", "payload": map[string]any{"headers": hdr("From", "Mark <mark@sreday.com>", "To", "jane@acme.com")}}}})
		default:
			json.NewEncoder(w).Encode(map[string]any{"id": strings.TrimPrefix(p, "/threads/"), "historyId": "1", "messages": []any{}})
		}
	}))
	defer gm.Close()
	s, err := New(Config{Base: "/snap", DataDir: t.TempDir(), AdminPassword: "pw", Account: "mark@sreday.com", Every: time.Hour, TZ: "Europe/London",
		DraftRoutine: "trig_draft", DraftToken: "sk-ant-oat01-x"})
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now }
	s.gmail = NewGmail(func() (string, error) { return "tok", nil })
	s.gmail.API = gm.URL
	s.gmail.Gap = 0
	s.fireBase = fire.URL + "/%s"
	s.draftGap = 20 * time.Millisecond
	form := flagged("form", "sponsors", "high", now.Add(-time.Hour))
	form.Actions = []Action{{Title: "Offer a call", Kind: "reply"}}
	form.People = []Person{{Name: "Jane Doe", Email: "jane@acme.com"}, {Name: "Mark", Email: "mark@sreday.com"}}
	s.store.Update("form", func(x *Thread) { *x = form })
	// A draft to a stranger that shows up meanwhile is nobody's reply here.
	other := flagged("other", "speakers", "low", now.Add(-time.Hour))
	other.Actions = []Action{{Title: "Reply", Kind: "reply"}}
	other.People = []Person{{Name: "Bob", Email: "bob@else.org"}}
	s.store.Update("other", func(x *Thread) { *x = other })

	h := s.Handler()
	if rec := call(t, h, "POST", "/admin/thread/form/draft", "pw", "a=0"); rec.Code != 204 {
		t.Fatalf("draft: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, h, "POST", "/admin/thread/other/draft", "pw", "a=0"); rec.Code != 204 {
		t.Fatalf("draft: %d %s", rec.Code, rec.Body)
	}
	mu.Lock()
	drafts = append([]map[string]any{{"id": "new", "message": map[string]any{"id": "m1", "threadId": "reply"}}}, drafts...)
	mu.Unlock()
	var got Thread
	for i := 0; i < 100; i++ {
		time.Sleep(20 * time.Millisecond)
		if got, _ = s.store.Get("form"); got.HasDraft {
			break
		}
	}
	if !got.HasDraft || got.DraftThread != "reply" || got.DraftID != "new" {
		t.Fatalf("the draft in the other thread wasn't found: %+v", got)
	}
	if o, _ := s.store.Get("other"); o.HasDraft || o.DraftID != "" {
		t.Fatal("a draft to someone else was taken for this thread's reply")
	}
	page := call(t, h, "GET", "/?view=hot&t=form", "pw", "").Body.String()
	if !strings.Contains(page, "#all/reply") || !strings.Contains(page, "Draft ready in Gmail") {
		t.Error("the card should link to the thread the draft is in")
	}
	// A read of the form thread (no draft in it) doesn't take the draft away.
	s.store.Update("form", func(x *Thread) { applyMeta(x, &ParsedThread{ID: "form", HistoryID: "1"}, Meta{}) })
	if got, _ = s.store.Get("form"); !got.HasDraft {
		t.Fatal("reading the asked thread dropped the draft that lives elsewhere")
	}
	// Mark sends it: the card parks like any answered one.
	mu.Lock()
	drafts, sent = drafts[1:], true
	mu.Unlock()
	now = now.Add(10 * time.Minute)
	s.checkSends()
	got, _ = s.store.Get("form")
	if got.HasDraft || got.DraftID != "" || got.Waiting != "them" || !got.SnoozeUntil.After(now) {
		t.Fatalf("a sent reply should park the card: draft %v id %q waiting %q", got.HasDraft, got.DraftID, got.Waiting)
	}
}
