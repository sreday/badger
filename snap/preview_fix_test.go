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
)

func TestPreviewAndCorrectDraft(t *testing.T) {
	var mu sync.Mutex
	b64 := func(s string) string { return base64.URLEncoding.EncodeToString([]byte(s)) }
	claude := "Hi Jane,\n\nI hope this email finds you well! I wanted to follow up on the Gold package.\n\nBest regards,\nMark"
	mine := "Hey Jane,\n\nCan we lock the Gold package by Friday? Happy to jump on a call Thursday.\n\nMark"
	body := claude // what the draft in Gmail says
	gm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/drafts":
			w.Write([]byte(`{"drafts":[{"id":"r1","message":{"id":"m1","threadId":"t1"}}]}`))
		case "/drafts/r1":
			w.Write([]byte(`{"id":"r1","message":{"id":"m1","threadId":"t1","payload":{"mimeType":"text/plain","headers":[{"name":"Subject","value":"Re: Gold"},{"name":"To","value":"Jane Doe <jane@acme.com>"}],"body":{"data":"` + b64(body) + `"}}}}`))
		default:
			http.NotFound(w, r)
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
	s, err := New(Config{Base: "/snap", DataDir: t.TempDir(), AdminPassword: "pw", Account: "mark@sreday.com", Every: time.Hour, TZ: "Europe/London",
		DraftRoutine: "trig_draft", DraftToken: "sk-ant-oat01-x"})
	if err != nil {
		t.Fatal(err)
	}
	s.gmail = NewGmail(func() (string, error) { return "tok", nil })
	s.gmail.API = gm.URL
	s.gmail.Gap = 0
	s.fireBase = fire.URL + "/%s"
	s.draftGap = 10 * time.Millisecond
	now := time.Now()
	s.store.Update("t1", func(th *Thread) {
		*th = flagged("t1", "sponsors", "high", now.Add(-30*time.Hour))
		th.Subject, th.HasDraft, th.Waiting = "Gold", true, "them"
		th.Actions = []Action{{Title: "Nudge Jane", Kind: "reply"}}
	})
	s.keepDraft("r1", "t1", "reply", "Nudge Jane")
	h := s.Handler()

	page := call(t, h, "GET", "/?view=sponsors&t=t1", "pw", "").Body.String()
	for _, want := range []string{`class="dprevbtn"`, "/admin/thread/t1/draft", ">Preview<", "Correct draft", "Draft ready in Gmail"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	// Preview: the draft as Gmail has it.
	var pv struct{ Draft, Text, Subject, To string }
	rec := call(t, h, "GET", "/admin/thread/t1/draft", "pw", "")
	json.Unmarshal(rec.Body.Bytes(), &pv)
	if rec.Code != 200 || pv.Draft != "r1" || !strings.Contains(pv.Text, "Gold package") || pv.Subject != "Re: Gold" || pv.To != "Jane Doe" {
		t.Fatalf("preview: %d %+v", rec.Code, pv)
	}
	form := func(text string) string {
		return "text=" + strings.NewReplacer(" ", "+", "\n", "%0A", "?", "%3F").Replace(text)
	}
	// Nothing changed: nothing to correct.
	if rec := call(t, h, "POST", "/admin/thread/t1/draft/correct", "pw", form(pv.Text)); rec.Code != 400 {
		t.Fatalf("an unchanged draft was sent off: %d", rec.Code)
	}
	if rec := call(t, h, "POST", "/admin/thread/t1/draft/correct", "pw", form(mine)); rec.Code != 204 {
		t.Fatalf("correct: %d %s", rec.Code, rec.Body)
	}
	mu.Lock()
	n := len(fired)
	payload := ""
	if n > 0 {
		payload = fired[n-1]
	}
	mu.Unlock()
	if n != 1 || !strings.HasPrefix(payload, fixMarker) || !strings.Contains(payload, "lock the Gold package by Friday") || !strings.Contains(payload, "draft id r1") || strings.Contains(payload, "MARK'S STYLE") {
		t.Fatalf("the routine wasn't asked to swap the text word for word: %q", payload)
	}
	th, _ := s.store.Get("t1")
	if !th.Fix.Saving(s.now()) {
		t.Fatalf("not saving: %+v", th.Fix)
	}
	if page := call(t, h, "GET", "/?view=sponsors&t=t1", "pw", "").Body.String(); !strings.Contains(page, "saving your corrected draft") {
		t.Error("the card doesn't say the correction is on its way")
	}
	if rec := call(t, h, "POST", "/admin/thread/t1/draft/correct", "pw", form(mine+" x")); rec.Code != http.StatusConflict {
		t.Fatalf("a second correction while the first is saving: %d", rec.Code)
	}
	// The lesson is there at once: Claude's text next to Mark's.
	job := s.styleJob()
	if job == nil || len(job.Sends) != 1 || !strings.Contains(job.Sends[0].Asked, "corrected this draft by hand") || !strings.Contains(job.Sends[0].Claude, "finds you well") || !strings.Contains(job.Sends[0].Sent, "by Friday") {
		t.Fatalf("style job: %+v", job)
	}
	if !strings.Contains(s.styleBlock(), "lock the Gold package by Friday") {
		t.Error("the next draft request doesn't carry the correction")
	}
	// The routine puts Mark's text in the draft: Snap notices.
	mu.Lock()
	body = mine
	mu.Unlock()
	for i := 0; i < 200; i++ {
		if th, _ = s.store.Get("t1"); th.Fix.Status == "done" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if th.Fix.Status != "done" || !th.HasDraft {
		t.Fatalf("the corrected draft wasn't noticed: %+v", th.Fix)
	}
	// The send that follows is compared with Mark's version, not Claude's first try.
	s.mu.Lock()
	var kept Send
	for _, p := range s.sends {
		if p.Draft == "r1" {
			kept = p
		}
	}
	s.mu.Unlock()
	if !strings.Contains(kept.Claude, "by Friday") {
		t.Fatalf("the kept draft should now be Mark's version: %q", kept.Claude)
	}
}
