package snap

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Snap's own Google sign-in: Gmail read-only, Snap's redirect URI, the right
// account only, and the token kept in Snap's folder.
func TestGoogleConnect(t *testing.T) {
	who := "mark@sreday.com"
	google := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			r.ParseForm()
			if r.Form.Get("redirect_uri") != "https://scan.sreday.com/snap/admin/google/callback" {
				http.Error(w, "bad redirect", 400)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "a", "refresh_token": "r", "expires_in": 3600})
		case "/userinfo":
			json.NewEncoder(w).Encode(map[string]string{"email": who})
		}
	}))
	defer google.Close()
	dir := t.TempDir()
	s, err := New(Config{Base: "/snap", DataDir: dir, AdminPassword: "pw", Account: "mark@sreday.com", PublicURL: "https://scan.sreday.com",
		ClientID: "cid", ClientSecret: "cs", Every: time.Hour, TZ: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	s.google.TokenURL, s.google.UserinfoURL = google.URL+"/token", google.URL+"/userinfo"
	h := s.Handler()

	rec := call(t, h, "GET", "/admin/google/connect", "pw", "")
	loc, _ := url.Parse(rec.Header().Get("Location"))
	q := loc.Query()
	if rec.Code != 303 || q.Get("client_id") != "cid" || q.Get("redirect_uri") != "https://scan.sreday.com/snap/admin/google/callback" ||
		!strings.Contains(q.Get("scope"), "gmail.readonly") || strings.Contains(q.Get("scope"), "drive") || strings.Contains(q.Get("scope"), "calendar") {
		t.Fatalf("connect: %d %s", rec.Code, loc)
	}
	state := q.Get("state")
	if rec := call(t, h, "GET", "/admin/google/callback?code=c&state=forged", "pw", ""); rec.Code != 400 {
		t.Fatalf("forged state: %d", rec.Code)
	}
	who = "someone@gmail.com"
	if rec := call(t, h, "GET", "/admin/google/callback?code=c&state="+state, "pw", ""); rec.Code != 502 || s.googleEmail() != "" {
		t.Fatalf("wrong account accepted: %d", rec.Code)
	}
	who = "mark@sreday.com"
	if rec := call(t, h, "GET", "/admin/google/callback?code=c&state="+state, "pw", ""); rec.Code != 303 || s.googleEmail() != "mark@sreday.com" {
		t.Fatalf("callback: %d %s", rec.Code, rec.Body)
	}
	if info, err := os.Stat(filepath.Join(dir, "snap", "google.json")); err != nil || info.Size() == 0 {
		t.Fatalf("token not kept in Snap's folder: %v", err)
	}
	if tok, err := s.google.AccessToken(); err != nil || tok != "a" {
		t.Fatalf("access token: %q %v", tok, err)
	}
	// The page offers Reconnect now, and Connect before.
	if body := call(t, h, "GET", "/", "pw", "").Body.String(); !strings.Contains(body, "Reconnect") {
		t.Fatal("no Reconnect button")
	}
}

// Gmail's "slow down" (403 quota, 429) is waited out; other errors aren't retried.
func TestGmailBacksOff(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch {
		case r.URL.Path == "/threads/denied":
			http.Error(w, `{"error":{"message":"Request had insufficient authentication scopes."}}`, 403)
		case calls <= 2:
			http.Error(w, `{"error":{"message":"Quota exceeded for quota metric 'Queries' ... rateLimitExceeded"}}`, 403)
		case calls == 3:
			http.Error(w, "slow down", 429)
		default:
			json.NewEncoder(w).Encode(map[string]any{"id": "x", "historyId": "1", "messages": []any{}})
		}
	}))
	defer srv.Close()
	g := NewGmail(func() (string, error) { return "tok", nil })
	g.API, g.Gap, g.Backoff = srv.URL, 0, time.Millisecond
	if _, err := g.Thread("x", false); err != nil || calls != 4 {
		t.Fatalf("after 3 slow-downs: %v, %d calls", err, calls)
	}
	calls = 100
	if _, err := g.Thread("denied", false); err == nil || calls != 101 || !strings.Contains(err.Error(), "Reconnect") {
		t.Fatalf("scope error retried or unclear: %v, %d calls", err, calls-100)
	}
}
