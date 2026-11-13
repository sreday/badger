package snap

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestQuickQuestion(t *testing.T) {
	now := time.Now().UTC()
	threads := map[string][]fakeMsg{"ing": {
		{from: "Ola <ola@ing.pl>", to: "mark@sreday.com", subject: "SREday Katowice x ING", at: now.AddDate(0, 0, -5), body: "We need a formal offer."},
	}}
	gm := fakeGmail(t, threads, map[string]string{"ing": "10"})
	defer gm.Close()
	var fired struct {
		auth, text string
		n          int
	}
	fire := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/claude_code/routines/"+qqRoutineDefault+"/fire" {
			http.NotFound(w, r)
			return
		}
		var in struct{ Text string }
		json.NewDecoder(r.Body).Decode(&in)
		fired.auth, fired.text = r.Header.Get("Authorization"), in.Text
		fired.n++
		w.Write([]byte(`{"type":"routine_fire"}`))
	}))
	defer fire.Close()
	s, err := New(Config{Base: "/snap", DataDir: t.TempDir(), AdminPassword: "pw", Account: "mark@sreday.com", Every: time.Hour, TeamDomains: team, TZ: "Europe/London"})
	if err != nil {
		t.Fatal(err)
	}
	s.gmail = NewGmail(func() (string, error) { return "tok", nil })
	s.gmail.API = gm.URL
	s.fireBase = fire.URL + "/v1/claude_code/routines/%s/fire"
	s.store.Update("ing", func(th *Thread) {
		*th = Thread{ID: "ing", Subject: "SREday Katowice x ING", HistoryID: "10", ClaudeHistory: "10", LastAt: now.AddDate(0, 0, -5), LastFrom: "Ola",
			People: []Person{{Name: "Ola", Email: "ola@ing.pl"}}, Category: "sponsors", Company: "ING", Loose: true, AnalyzedBy: "claude", AnalyzedHistory: "10",
			Label: "ING Poland needs formal offer", Actions: []Action{{Title: "Send the offer"}}}
	})
	h := s.Handler()
	bearer := "Bearer " + s.token

	if body := call(t, h, "GET", "/?t=ing", "pw", "").Body.String(); !strings.Contains(body, `class="qqpill"`) || !strings.Contains(body, `class="qqwin glass" hidden`) {
		t.Fatal("no Quick question pill / window")
	}
	// Not connected yet: the window asks for the token.
	if rec := call(t, h, "GET", "/admin/thread/ing/qa", "pw", ""); !strings.Contains(rec.Body.String(), `"ready":false`) {
		t.Fatalf("qa: %s", rec.Body)
	}
	if rec := call(t, h, "POST", "/admin/thread/ing/ask", "pw", "q=Did+I+send+the+contract"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ask without token: %d", rec.Code)
	}
	if rec := call(t, h, "POST", "/admin/qq-token", "pw", "token=sk-ant-oat01-qqqqqqqqqqqqqqqqqqqq"); rec.Code != 204 {
		t.Fatalf("token: %d %s", rec.Code, rec.Body)
	}
	rec := call(t, h, "POST", "/admin/thread/ing/ask", "pw", "q=Didn%27t+I+send+them+the+tentative+contract%3F")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"asking"`) || !strings.Contains(rec.Body.String(), `"ready":true`) {
		t.Fatalf("ask: %d %s", rec.Code, rec.Body)
	}
	if fired.n != 1 || fired.auth != "Bearer sk-ant-oat01-qqqqqqqqqqqqqqqqqqqq" || !strings.Contains(fired.text, qaMarker) || !strings.Contains(fired.text, "thread id ing") {
		t.Fatalf("fire: %+v", fired)
	}
	// The routine: questions, a search, the answer.
	if rec := call(t, h, "GET", "/api/qa", "", ""); rec.Code != 401 {
		t.Fatalf("api/qa without token: %d", rec.Code)
	}
	var queue struct {
		Questions []qaItem `json:"questions"`
	}
	json.Unmarshal(call(t, h, "GET", "/api/qa", bearer, "").Body.Bytes(), &queue)
	if len(queue.Questions) != 1 || queue.Questions[0].Question != "Didn't I send them the tentative contract?" ||
		queue.Questions[0].Thread["headline"] != "ING Poland needs formal offer" || len(queue.Questions[0].Searches) == 0 {
		t.Fatalf("queue: %+v", queue)
	}
	if mails, _ := json.Marshal(queue.Questions[0].Thread["mails"]); !strings.Contains(string(mails), "formal offer") {
		t.Fatalf("mails: %s", mails)
	}
	if sr := call(t, h, "GET", "/api/search?q=ING", bearer, "").Body.String(); !strings.Contains(sr, `"thread":"ing"`) || !strings.Contains(sr, "formal offer") {
		t.Fatalf("search: %s", sr)
	}
	qid := queue.Questions[0].ID
	ans := `{"answers":[{"id":"` + qid + `","answer":"Yes, on 3 Nov.","context":"Mark sent ING the tentative contract on 3 Nov (contract.pdf).","sources":[{"thread":"x1","subject":"Offer","date":"2026-11-03"}]}]}`
	if rec := call(t, h, "POST", "/api/qa", bearer, ans); !strings.Contains(rec.Body.String(), `"applied":1`) {
		t.Fatalf("answers: %s", rec.Body)
	}
	if body := call(t, h, "GET", "/admin/thread/ing/qa", "pw", "").Body.String(); !strings.Contains(body, `"status":"answered"`) || !strings.Contains(body, "tentative contract on 3 Nov") {
		t.Fatalf("answered: %s", body)
	}
	json.Unmarshal(call(t, h, "GET", "/api/qa", bearer, "").Body.Bytes(), &queue)
	if len(queue.Questions) != 0 {
		t.Fatal("answered question still queued")
	}
	// Update the thread with context: kept, read again with it.
	if rec := call(t, h, "POST", "/admin/thread/ing/qa/"+qid+"/apply", "pw", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"applied":true`) {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body)
	}
	th, _ := s.store.Get("ing")
	if len(th.Context) != 1 || th.ClaudeHistory != "" {
		t.Fatalf("thread: %+v", th.Context)
	}
	call(t, h, "POST", "/admin/thread/ing/qa/"+qid+"/apply", "pw", "")
	if th, _ := s.store.Get("ing"); len(th.Context) != 1 {
		t.Fatal("applied twice")
	}
	s.mu.Lock()
	ctx := s.threadContextLocked(th)
	s.mu.Unlock()
	if !strings.Contains(ctx, "Context Mark confirmed from his mail (Quick question): Mark sent ING the tentative contract") {
		t.Fatalf("excerpt context: %q", ctx)
	}
	if body := call(t, h, "GET", "/?t=ing", "pw", "").Body.String(); !strings.Contains(body, "From your mail") {
		t.Fatal("context not on the card")
	}
}
