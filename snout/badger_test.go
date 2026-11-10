package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sreday/badger/pkg"
)

// Snug at /badger: the admin password, Snout's Luma key, and badges
// handed to the browser to print.
func TestHostedBadger(t *testing.T) {
	fake := fakeLuma(t)
	defer fake.Close()
	pkg.SetLumaBaseURL(fake.URL)
	defer pkg.SetLumaBaseURL("https://api.lu.ma")

	srv, err := newServer(Config{DataDir: t.TempDir(), LumaAPIKey: "test-key", AdminPassword: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()
	get := func(method, path string, auth bool) (int, string, http.Header) {
		req, _ := http.NewRequest(method, ts.URL+path, nil)
		if auth {
			req.SetBasicAuth("", "pw")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header
	}

	if code, _, _ := get("GET", "/snug/", false); code != http.StatusUnauthorized {
		t.Fatalf("badger without the password: %d", code)
	}
	if code, _, _ := get("GET", "/snug/printed/x", false); code != http.StatusUnauthorized {
		t.Fatalf("a printed badge without the password: %d", code)
	}
	code, body, _ := get("GET", "/snug/", true)
	if code != 200 || !strings.Contains(body, `href="/snug/event/evt-1"`) || strings.Contains(body, "Lu.ma API key") {
		t.Fatalf("events page: %d\n%s", code, body)
	}
	code, body, _ = get("GET", "/snug/event/evt-1", true)
	if code != 200 || !strings.Contains(body, "Ada Lovelace") || !strings.Contains(body, `href="/snug/print/evt-1/ada@example.com"`) ||
		!strings.Contains(body, "--kiosk-printing") && !strings.Contains(body, `id="kiosk"`) {
		t.Fatalf("guest page: %d\n%s", code, body)
	}

	// Print: no CUPS on the server, the job is the badge for the browser.
	code, body, _ = get("POST", "/snug/print/evt-1/ada@example.com", true)
	var job struct{ Status, URL, For string }
	if err := json.Unmarshal([]byte(body), &job); err != nil || code != 200 || job.Status != "browser" ||
		!strings.HasPrefix(job.URL, "/snug/printed/") || job.For != "ada@example.com" {
		t.Fatalf("print: %d %s", code, body)
	}
	code, body, h := get("GET", job.URL, true)
	if code != 200 || h.Get("Content-Type") != "image/png" || !strings.HasPrefix(body, "\x89PNG") {
		t.Fatalf("printed badge: %d %s", code, h.Get("Content-Type"))
	}
	if code, _, _ := get("GET", "/snug/luma", true); code == 200 {
		t.Fatal("hosted Badger has no Luma login")
	}
}

// After a restart (nothing cached), Preview and Print still find the guest.
func TestHostedBadgerAfterRestart(t *testing.T) {
	fake := fakeLuma(t)
	defer fake.Close()
	pkg.SetLumaBaseURL(fake.URL)
	defer pkg.SetLumaBaseURL("https://api.lu.ma")
	srv, err := newServer(Config{DataDir: t.TempDir(), LumaAPIKey: "test-key", AdminPassword: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()
	for _, m := range []string{"GET /snug/view/evt-1/ada@example.com", "POST /snug/print/evt-1/ada@example.com", "GET /snug/view/evt-1/nobody@example.com"} {
		f := strings.Fields(m)
		req, _ := http.NewRequest(f[0], ts.URL+f[1], nil)
		req.SetBasicAuth("", "pw")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		want := 200
		if strings.Contains(m, "nobody") {
			want = 404
		}
		if resp.StatusCode != want || want == 200 && f[0] == "GET" && resp.Header.Get("Content-Type") != "image/png" {
			t.Errorf("%s: %d %s", m, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
	}
}

// Every app lives at /<app>; the old homes redirect and keep POSTs.
func TestAppAddresses(t *testing.T) {
	srv, err := newServer(Config{DataDir: t.TempDir(), LumaAPIKey: "k", AdminPassword: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	h := srv.routes()
	cases := []struct {
		method, path, want string
		code               int
	}{
		{"GET", "/admin", "/snout", http.StatusMovedPermanently},
		{"GET", "/admin?x=1", "/snout?x=1", http.StatusMovedPermanently},
		{"GET", "/badger", "/snug/", http.StatusMovedPermanently},
		{"GET", "/badger/event/evt-1?a=b", "/snug/event/evt-1?a=b", http.StatusPermanentRedirect},
		{"POST", "/badger/checkins", "/snug/checkins", http.StatusPermanentRedirect},
		{"GET", "/snug", "/snug/", http.StatusMovedPermanently},
		{"GET", "/snoop", "/snoop/", http.StatusMovedPermanently},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(c.method, c.path, nil))
		if w.Code != c.code || w.Header().Get("Location") != c.want {
			t.Errorf("%s %s = %d %q, want %d %q", c.method, c.path, w.Code, w.Header().Get("Location"), c.code, c.want)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/snout", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("/snout without the password = %d, want 401", w.Code)
	}
}

// The turning head's files: served with an ETag, 304 when unchanged, only
// known kinds, and wired into the hub and the headers.
func TestHeadAssets(t *testing.T) {
	srv, err := newServer(Config{DataDir: t.TempDir(), AdminPassword: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()
	get := func(path, etag string) *http.Response {
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		req.SetBasicAuth("admin", "pw")
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	for _, f := range []string{"head.js", "small.json", "small.webp", "large.json", "large.webp", "corners.png", "hub-head.png"} {
		r := get("/assets/head/"+f, "")
		if r.StatusCode != 200 || r.Header.Get("ETag") == "" {
			t.Fatalf("%s: %d, etag %q", f, r.StatusCode, r.Header.Get("ETag"))
		}
		if r2 := get("/assets/head/"+f, r.Header.Get("ETag")); r2.StatusCode != http.StatusNotModified {
			t.Errorf("%s with its ETag: %d, want 304", f, r2.StatusCode)
		}
	}
	if r := get("/assets/head/nope.txt", ""); r.StatusCode != 404 {
		t.Errorf("unknown file: %d", r.StatusCode)
	}
	req, _ := http.NewRequest("GET", ts.URL+"/", nil)
	req.SetBasicAuth("admin", "pw")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), `data-bhead="large" data-blink`) || !strings.Contains(string(b), "/assets/head/head.js") {
		t.Error("hub: no turning head")
	}
}
