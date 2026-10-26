package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeLuma serves list-events and get-guests in Luma's response shape,
// split over two pages to exercise pagination.
func fakeLuma(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-luma-api-key") != "test-key" {
			http.Error(w, "bad key", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/public/v1/calendar/list-events":
			io.WriteString(w, `{"entries":[{"event":{"api_id":"evt-1","name":"SREday Test","start_at":"`+
				time.Now().Format(time.RFC3339)+`","timezone":"America/Chicago"}}],"next_cursor":null}`)
		case "/public/v1/event/get-guests":
			if r.URL.Query().Get("pagination_cursor") == "" {
				io.WriteString(w, `{"entries":[{"guest":{"api_id":"gst-1","name":"Ada Lovelace","email":"ada@example.com",
					"user_first_name":"Ada","user_last_name":"Lovelace",
					"check_in_qr_code":"https://luma.com/check-in/evt-1?pk=g-aaaaaa1",
					"event_tickets":[{"api_id":"tkt-aaaaaa1"}],
					"registration_answers":[{"label":"Job Title","answer":"CTO"},{"label":"Company","answer":"Analytical"},
					{"label":"What is your LinkedIn profile?","answer":"/in/Ada-L"},{"label":"Terms and Conditions","answer":true}]}}],
					"next_cursor":"page2"}`)
			} else {
				io.WriteString(w, `{"entries":[{"guest":{"api_id":"gst-2","name":"Grace Hopper","email":"grace@example.com",
					"check_in_qr_code":"https://luma.com/check-in/evt-1?pk=g-bbbbbb2","registration_answers":[]}}],"next_cursor":null}`)
			}
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestServerEndToEnd(t *testing.T) {
	luma := fakeLuma(t)
	defer luma.Close()
	lumaBaseURL = luma.URL

	srv, err := newServer(Config{DataDir: t.TempDir(), LumaAPIKey: "test-key", AdminPassword: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()

	// Admin is behind basic auth.
	if resp, _ := http.Get(ts.URL + "/admin"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("admin without auth: %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("POST", ts.URL+"/admin/sponsors", strings.NewReader(url.Values{"name": {"Acme"}, "event_id": {"evt-1"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "pw")
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if resp, err := noRedirect.Do(req); err != nil || resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("add sponsor: %v %v", resp, err)
	}
	sps := srv.store.Sponsors()
	if len(sps) != 1 || sps[0].EventTZ != "America/Chicago" {
		t.Fatalf("sponsors = %+v", sps)
	}
	sp := sps[0]
	base := ts.URL + "/s/" + sp.Token

	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}

	// Without the PIN: PIN page, and the API refuses.
	body := get(t, c, base)
	if !strings.Contains(body, `name="pin"`) {
		t.Fatal("expected PIN page")
	}
	if resp, _ := c.Post(base+"/sync", "application/json", strings.NewReader(`{"ops":[]}`)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("sync without PIN: %d", resp.StatusCode)
	}
	c.PostForm(base+"/pin", url.Values{"pin": {"wrong"}})
	c.PostForm(base+"/pin", url.Values{"pin": {sp.PIN}})
	if body := get(t, c, base); !strings.Contains(body, "Tap to start camera") {
		t.Fatal("expected scanner page after PIN")
	}

	now := time.Now().UTC()
	results := postSync(t, c, base, []syncOp{
		{Op: "scan", ScanID: "a", Raw: "linkedin://in/ada-l", ScannedAt: now.Add(-10 * time.Minute), ScannedBy: "Bob"},
		{Op: "update", ScanID: "a", Rating: "hot", Note: "wants a demo"},
		{Op: "scan", ScanID: "b", Raw: "https://luma.com/check-in/evt-1?pk=g-bbbbbb2", ScannedAt: now.Add(-5 * time.Minute)},
		{Op: "scan", ScanID: "c", Raw: "https://luma.com/check-in/evt-1?pk=g-aaaaaa1", ScannedAt: now},
		{Op: "scan", ScanID: "d", Raw: "WIFI:T:WPA;S:x;P:y;;", ScannedAt: now},
		{Op: "scan", ScanID: "a", Raw: "linkedin://in/ada-l", ScannedAt: now.Add(-10 * time.Minute)}, // retry
	})
	wantStatus := []string{"ok", "ok", "ok", "duplicate", "unreadable", "ok"}
	for i, r := range results {
		if r.Status != wantStatus[i] {
			t.Errorf("op %d status = %s, want %s", i, r.Status, wantStatus[i])
		}
	}
	if l := results[0].Lead; l == nil || !l.Matched || l.Name != "Ada Lovelace" || l.Company != "Analytical" {
		t.Errorf("badge scan lead = %+v", results[0].Lead)
	}
	if l := results[3].Lead; l == nil || l.ScanID != "a" {
		t.Errorf("duplicate should point at the first scan: %+v", l)
	}

	csvBody := get(t, c, base+"/export.csv")
	lines := strings.Split(strings.TrimSpace(strings.TrimPrefix(csvBody, "\ufeff")), "\n")
	if len(lines) != 3 || !strings.Contains(lines[1], "Ada,Lovelace,ada@example.com,CTO,Analytical,https://www.linkedin.com/in/ada-l,hot,wants a demo,Bob") ||
		!strings.Contains(lines[2], ",Grace,Hopper,grace@example.com,") {
		t.Errorf("export =\n%s", csvBody)
	}

	var leads struct{ Leads []LeadView }
	if err := json.Unmarshal([]byte(get(t, c, base+"/leads")), &leads); err != nil || len(leads.Leads) != 2 {
		t.Errorf("leads = %+v, %v", leads, err)
	}
}

func TestAdminUser(t *testing.T) {
	srv, err := newServer(Config{DataDir: t.TempDir(), LumaAPIKey: "k", AdminUser: "maro", AdminPassword: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	h := srv.requireAdmin(func(w http.ResponseWriter, r *http.Request) {})
	for _, tc := range []struct {
		user string
		want int
	}{{"maro", http.StatusOK}, {"admin", http.StatusUnauthorized}} {
		req := httptest.NewRequest("GET", "/admin", nil)
		req.SetBasicAuth(tc.user, "pw")
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != tc.want {
			t.Errorf("user %q: %d, want %d", tc.user, rec.Code, tc.want)
		}
	}
}

func get(t *testing.T, c *http.Client, u string) string {
	t.Helper()
	resp, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func postSync(t *testing.T, c *http.Client, base string, ops []syncOp) []syncResult {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"ops": ops})
	resp, err := c.Post(base+"/sync", "application/json", strings.NewReader(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct{ Results []syncResult }
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Results
}
