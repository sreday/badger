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

// Badger 2.0 at /badger: the admin password, Snout's Luma key, and badges
// handed to the browser to print.
func TestHostedBadger(t *testing.T) {
	luma := fakeLuma(t)
	defer luma.Close()
	pkg.SetLumaBaseURL(luma.URL)
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

	if code, _, _ := get("GET", "/badger/", false); code != http.StatusUnauthorized {
		t.Fatalf("badger without the password: %d", code)
	}
	if code, _, _ := get("GET", "/badger/printed/x", false); code != http.StatusUnauthorized {
		t.Fatalf("a printed badge without the password: %d", code)
	}
	code, body, _ := get("GET", "/badger/", true)
	if code != 200 || !strings.Contains(body, `href="/badger/event/evt-1"`) || strings.Contains(body, "Lu.ma API key") {
		t.Fatalf("events page: %d\n%s", code, body)
	}
	code, body, _ = get("GET", "/badger/event/evt-1", true)
	if code != 200 || !strings.Contains(body, "Ada Lovelace") || !strings.Contains(body, `href="/badger/print/evt-1/ada@example.com"`) ||
		!strings.Contains(body, "--kiosk-printing") && !strings.Contains(body, `id="kiosk"`) {
		t.Fatalf("guest page: %d\n%s", code, body)
	}

	// Print: no CUPS on the server, the job is the badge for the browser.
	code, body, _ = get("POST", "/badger/print/evt-1/ada@example.com", true)
	var job struct{ Status, URL, For string }
	if err := json.Unmarshal([]byte(body), &job); err != nil || code != 200 || job.Status != "browser" ||
		!strings.HasPrefix(job.URL, "/badger/printed/") || job.For != "ada@example.com" {
		t.Fatalf("print: %d %s", code, body)
	}
	code, body, h := get("GET", job.URL, true)
	if code != 200 || h.Get("Content-Type") != "image/png" || !strings.HasPrefix(body, "\x89PNG") {
		t.Fatalf("printed badge: %d %s", code, h.Get("Content-Type"))
	}
	if code, _, _ := get("GET", "/badger/luma", true); code == 200 {
		t.Fatal("hosted Badger has no Luma login")
	}
}
