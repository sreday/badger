package snap

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestThreeDraftsAtOnce(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	fired := 0
	fire := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fired++
		mu.Unlock()
		w.Write([]byte(`{}`))
	}))
	defer fire.Close()
	gm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"id": strings.TrimPrefix(r.URL.Path, "/threads/"), "historyId": "1", "messages": []any{}})
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
	s.draftGap = time.Hour // the watchers stay asleep: the test moves things by hand
	for i := 0; i < 5; i++ {
		th := flagged(fmt.Sprintf("d%d", i), "sponsors", "high", now.Add(-time.Hour))
		th.Actions = []Action{{Title: "Reply", Kind: "reply"}}
		s.store.Update(th.ID, func(x *Thread) { *x = th })
	}
	h := s.Handler()
	count := func() int { mu.Lock(); defer mu.Unlock(); return fired }
	for i := 0; i < 5; i++ {
		if rec := call(t, h, "POST", fmt.Sprintf("/admin/thread/d%d/draft", i), "pw", "a=0"); rec.Code != 204 {
			t.Fatalf("draft %d: %d %s", i, rec.Code, rec.Body)
		}
		now = now.Add(time.Second)
	}
	if count() != 3 {
		t.Fatalf("five clicks should start three drafts, started %d", count())
	}
	d3, _ := s.store.Get("d3")
	if !d3.Queued4Draft(now) || d3.Drafting(now) {
		t.Fatalf("the fourth should wait its turn: %+v", d3.DraftWait)
	}
	page := call(t, h, "GET", "/?view=hot&t=d3", "pw", "").Body.String()
	if !strings.Contains(page, "Three drafts are in the making") || strings.Count(page, `class="wheel k`) != 5 || strings.Count(page, ` busy">`) != 5 {
		t.Error("a queued draft should say so, with its wheel spinning like the others")
	}
	// A second click on a queued one changes nothing; the Draft bar is full too.
	call(t, h, "POST", "/admin/thread/d3/draft", "pw", "a=0")
	if rec := call(t, h, "POST", "/admin/compose", "pw", "brief=follow+up+with+Acme"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("a fourth draft from the bar: %d", rec.Code)
	}
	if count() != 3 {
		t.Fatalf("started %d", count())
	}
	// One draft lands: the oldest queued one starts, the other keeps waiting.
	s.store.Update("d0", func(x *Thread) { x.HasDraft = true })
	s.pumpDrafts()
	d3, _ = s.store.Get("d3")
	d4, _ := s.store.Get("d4")
	if count() != 4 || !d3.Drafting(now) || d3.Queued4Draft(now) || !d4.Queued4Draft(now) {
		t.Fatalf("after one finished: started %d, d3 drafting %v, d4 queued %v", count(), d3.Drafting(now), d4.Queued4Draft(now))
	}
	// A queued draft nobody could start for half an hour is dropped.
	now = now.Add(31 * time.Minute)
	s.pumpDrafts()
	if d4, _ = s.store.Get("d4"); !d4.DraftWait.At.IsZero() {
		t.Fatal("a stale queued draft was kept")
	}
}
