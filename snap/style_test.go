package snap

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func b64(s string) string { return base64.URLEncoding.EncodeToString([]byte(s)) }

func TestStyleFromSends(t *testing.T) {
	var mu sync.Mutex
	drafts := `{"drafts":[{"id":"r1","message":{"id":"m1","threadId":"t1"}},{"id":"r2","message":{"id":"m2","threadId":"t2"}}]}`
	thread1 := `{"id":"t1","historyId":"5","messages":[]}`
	claude := "Hi Jane,\n\nI hope this email finds you well! I wanted to follow up on the Gold package.\n\nBest regards,\nMark"
	gm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/drafts":
			w.Write([]byte(drafts))
		case "/drafts/r1":
			w.Write([]byte(`{"id":"r1","message":{"id":"m1","threadId":"t1","payload":{"mimeType":"text/plain","headers":[{"name":"Subject","value":"Re: Gold"}],"body":{"data":"` + b64(claude) + `"}}}}`))
		case "/drafts/r2":
			w.Write([]byte(`{"id":"r2","message":{"id":"m2","threadId":"t2","payload":{"mimeType":"text/plain","headers":[{"name":"Subject","value":"Re: Venue"}],"body":{"data":"` + b64("Hey Bo,\nAll good!") + `"}}}}`))
		case "/threads/t1":
			w.Write([]byte(thread1))
		case "/threads/t2":
			w.Write([]byte(`{"id":"t2","historyId":"9","messages":[]}`))
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

	// Claude's drafts show up and are kept as written.
	s.keepDraft("r1", "t1", "reply", "Nudge Jane about the Gold package")
	s.keepDraft("r1", "t1", "reply", "again") // once only
	s.keepDraft("r2", "t2", "compose", "tell Bo all good")
	if len(s.sends) != 2 || s.sends[0].Claude != claude || s.sends[0].Subject != "Re: Gold" {
		t.Fatalf("kept: %+v", s.sends)
	}
	// Still drafts: nothing to learn yet.
	s.checkSends()
	if s.styleJob() != nil || strings.Contains(s.styleBlock(), "CLAUDE WROTE") {
		t.Fatal("learning from unsent drafts")
	}
	// Mark edits r1 and sends it; r2 goes out untouched.
	mu.Lock()
	drafts = `{"drafts":[]}`
	sent := "Hey Jane,\n\nAny news on Gold? Happy to jump on a call.\n\nCheers,\nMark"
	thread1 = `{"id":"t1","historyId":"6","messages":[{"id":"x1","labelIds":["SENT"],"internalDate":"` + ms(time.Now()) + `","payload":{"mimeType":"text/plain","headers":[{"name":"From","value":"Mark <mark@sreday.com>"},{"name":"Subject","value":"Re: Gold"}],"body":{"data":"` + b64(sent+"\n\nOn Mon, 9 Nov 2026 at 10:00, Jane <jane@acme.com> wrote:\n> old") + `"}}}]}`
	mu.Unlock()
	s.sends[1].Thread = "t1x" // r2's thread fails to load: stays open
	s.checkSends()
	if p := s.sends[0]; p.Sent != sent || p.Same() || p.Gone {
		t.Fatalf("sent: %+v", p)
	}
	if !s.sends[1].Open() {
		t.Fatal("r2 closed without its thread")
	}
	s.sends[1].Sent = s.sends[1].Claude // as if sent untouched

	// The very next draft request carries the edit.
	block := s.styleBlock()
	if !strings.Contains(block, "CLAUDE WROTE:\nHi Jane,") || !strings.Contains(block, "MARK SENT:\nHey Jane,") || !strings.Contains(block, "1 recent drafts went out exactly") {
		t.Fatalf("block: %s", block)
	}
	if err := s.fireRoutine("Draft a reply"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fired[0], "MARK SENT:") {
		t.Fatalf("payload: %s", fired[0])
	}

	// The reading routine gets the pairs and sends back the rulebook.
	h := s.Handler()
	req := httptest.NewRequest("GET", "/api/queue", nil)
	req.Header.Set("Authorization", "Bearer "+s.token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var q struct{ Style *StyleJob }
	json.Unmarshal(rec.Body.Bytes(), &q)
	if q.Style == nil || len(q.Style.Sends) != 2 || !q.Style.Sends[1].Same || q.Style.Sends[0].Asked == "" {
		t.Fatalf("queue style: %s", rec.Body)
	}
	ans := `{"model":"claude-fable-5-2","threads":[],"style":{"notes":["Opens with 'Hey <first name>,'","Signs off 'Cheers,'",""],"learnt":["r1","r2","nope"]}}`
	req = httptest.NewRequest("POST", "/api/answers", strings.NewReader(ans))
	req.Header.Set("Authorization", "Bearer "+s.token)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"style_learnt":2`) {
		t.Fatalf("answers: %s", rec.Body)
	}
	if s.styleJob() != nil || len(s.style.Notes) != 2 || s.style.From != 2 {
		t.Fatalf("style: %+v", s.style)
	}
	if b := s.styleBlock(); !strings.Contains(b, "(2 sends so far)") || !strings.Contains(b, "- Signs off 'Cheers,'") {
		t.Fatalf("block: %s", b)
	}
	// It survives a restart.
	s2, err := New(Config{Base: "/snap", DataDir: s.cfg.DataDir, AdminPassword: "pw", Account: "mark@sreday.com", Every: time.Hour, TZ: "Europe/London"})
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.sends) != 2 || len(s2.style.Notes) != 2 {
		t.Fatal("not saved")
	}
}

func ms(t time.Time) string { return strconv.FormatInt(t.UnixMilli(), 10) }
