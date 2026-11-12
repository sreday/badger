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

func TestCompose(t *testing.T) {
	var mu sync.Mutex
	drafts := []string{`{"id":"r-old","message":{"id":"m-old","threadId":"t-old"}}`}
	gm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/drafts":
			w.Write([]byte(`{"drafts":[` + strings.Join(drafts, ",") + `]}`))
		case "/drafts/r-new":
			w.Write([]byte(`{"id":"r-new","message":{"id":"m-new","threadId":"t-ch","payload":{"headers":[{"name":"Subject","value":"Re: ClickHouse x SREday"},{"name":"To","value":"Ann Lee <ann@clickhouse.com>, bo@clickhouse.com"}]}}}`))
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
		w.Write([]byte(`{"type":"routine_fire"}`))
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
	h := s.Handler()

	if !strings.Contains(call(t, h, "GET", "/", "pw", "").Body.String(), "Draft something new with Claude:") {
		t.Fatal("no compose line")
	}
	if rec := call(t, h, "POST", "/admin/compose", "pw", "brief="); rec.Code != 400 {
		t.Fatalf("empty brief: %d", rec.Code)
	}
	if rec := call(t, h, "POST", "/admin/compose", "pw", "brief=find+the+ClickHouse+sponsorship+thread+and+follow+up"); rec.Code != 204 {
		t.Fatalf("compose: %d %s", rec.Code, rec.Body)
	}
	mu.Lock()
	if len(fired) != 1 || !strings.HasPrefix(fired[0], composeMarker) || !strings.Contains(fired[0], "find the ClickHouse sponsorship thread") || !strings.Contains(fired[0], "Never send") {
		t.Fatalf("fired: %q", fired)
	}
	mu.Unlock()
	if act := call(t, h, "GET", "/admin/activity", "pw", "").Body.String(); !strings.Contains(act, `"label":"Drafting: find the ClickHouse sponsorship`) {
		t.Fatalf("activity: %s", act)
	}
	if body := call(t, h, "GET", "/", "pw", "").Body.String(); !strings.Contains(body, "Claude is on it") {
		t.Fatal("no working line")
	}
	// Claude saves its draft; the old one doesn't count.
	mu.Lock()
	drafts = append([]string{`{"id":"r-new","message":{"id":"m-new","threadId":"t-ch"}}`}, drafts...)
	mu.Unlock()
	for i := 0; i < 200 && s.recentComposes(time.Now())[0].Ready.IsZero(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	c := s.recentComposes(time.Now())[0]
	if c.Thread != "t-ch" || c.Subject != "Re: ClickHouse x SREday" || c.To != "Ann Lee +1" {
		t.Fatalf("job: %+v", c)
	}
	body := call(t, h, "GET", "/", "pw", "").Body.String()
	if !strings.Contains(body, "Draft ready") || !strings.Contains(body, "#drafts/t-ch") {
		t.Fatal("no ready line")
	}
	if act := call(t, h, "GET", "/admin/activity", "pw", "").Body.String(); strings.Contains(act, "Drafting:") {
		t.Fatalf("still busy: %s", act)
	}
	// It survives a restart.
	s2, err := New(Config{Base: "/snap", DataDir: s.cfg.DataDir, AdminPassword: "pw", Account: "mark@sreday.com", Every: time.Hour, TZ: "Europe/London"})
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.recentComposes(time.Now()); len(got) != 1 || got[0].Thread != "t-ch" {
		t.Fatalf("after restart: %+v", got)
	}
}
