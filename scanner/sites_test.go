package main

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const testMetadata = `title: "SREday Amsterdam"
luma_evt: evt-1
mailto: "mailto:hello@sreday.com"

sponsors:
  - logo: ing.png
    url: https://www.ing.com/
  - logo: jug-amsterdam.png
    url: https://www.meetup.com/amsterdam-java-user-group/
  - logo: sentry.png   # venue host
    url: https://sentry.io/#top
  - logo: ms.png
    name: "Microsoft Azure"

hero_pictures:
- ../photos/x.jpeg
`

func TestParseMetadata(t *testing.T) {
	id, sps := parseMetadata(testMetadata)
	if id != "evt-1" {
		t.Errorf("event id = %q", id)
	}
	want := []SiteSponsor{
		{"ING", "https://www.ing.com/"},
		{"JUG Amsterdam", "https://www.meetup.com/amsterdam-java-user-group/"},
		{"Sentry", "https://sentry.io/#top"},
		{"Microsoft Azure", ""},
	}
	if len(sps) != len(want) {
		t.Fatalf("sponsors = %+v", sps)
	}
	for i := range want {
		if sps[i] != want[i] {
			t.Errorf("sponsor %d = %+v, want %+v", i, sps[i], want[i])
		}
	}
}

func TestNameFromLogo(t *testing.T) {
	for logo, want := range map[string]string{
		"harness.png":        "Harness",
		"gdg_london.png":     "GDG London",
		"kube-events.png":    "Kube Events",
		"ksug.ai.png":        "Ksug AI",
		"pe-norway-full.png": "PE Norway",
		"DevIT_black.png":    "DevIT",
	} {
		if got := nameFromLogo(logo); got != want {
			t.Errorf("nameFromLogo(%q) = %q, want %q", logo, got, want)
		}
	}
}

// fakeGitHub serves two repos that both list evt-1 (like LLMday + PEC London)
// plus a finished event under past/ that must be ignored.
func fakeGitHub(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/a/contents/":
			io.WriteString(w, `[{"name":"2026-amsterdam-q4","type":"dir"},{"name":"past","type":"dir"},{"name":"README.md","type":"file"}]`)
		case "/repos/o/b/contents/":
			io.WriteString(w, `[{"name":"2026-london-q4","type":"dir"}]`)
		case "/o/a/HEAD/2026-amsterdam-q4/metadata.yml":
			io.WriteString(w, testMetadata)
		case "/o/b/HEAD/2026-london-q4/metadata.yml":
			io.WriteString(w, "luma_evt: evt-1\nsponsors:\n  - logo: ING.png\n  - logo: harness.png\n")
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestSiteSponsorLinks(t *testing.T) {
	luma := fakeLuma(t)
	defer luma.Close()
	lumaBaseURL = luma.URL
	gh := fakeGitHub(t)
	defer gh.Close()
	githubAPIURL, githubRawURL = gh.URL, gh.URL
	t.Setenv("SITE_REPOS", "o/a, o/b")

	srv, err := newServer(Config{DataDir: t.TempDir(), LumaAPIKey: "test-key", AdminPassword: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()

	admin := func(method, path string) string {
		req, _ := http.NewRequest(method, ts.URL+path, nil)
		req.SetBasicAuth("admin", "pw")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}

	body := admin("GET", "/admin")
	sps := srv.store.Sponsors()
	if len(sps) != 5 {
		t.Fatalf("want 5 sponsor links (ING once), got %+v", sps)
	}
	for _, sp := range sps {
		if sp.PIN != "" || !sp.FromSite || sp.EventID != "evt-1" {
			t.Errorf("site sponsor %+v should be on evt-1 without a PIN", sp)
		}
	}
	if !strings.Contains(body, "JUG Amsterdam") || !strings.Contains(body, "Generate PIN") {
		t.Errorf("admin page missing site sponsors:\n%s", body)
	}

	// A second sync, even forced, adds nothing.
	admin("POST", "/admin/refresh")
	if n := len(srv.store.Sponsors()); n != 5 {
		t.Errorf("after resync: %d sponsor links", n)
	}

	// Taken off the page: untouched links go, links with a PIN stay.
	var harness Sponsor
	for _, sp := range srv.store.Sponsors() {
		if sp.Name == "Harness" {
			harness = sp
		}
	}
	admin("POST", "/admin/pin/"+harness.Token)
	ev := LumaEvent{APIId: "evt-1"}
	if _, removed, _ := srv.store.SyncSiteSponsors(ev, []string{"ING"}, false); removed != 0 {
		t.Errorf("prune=false removed %d", removed)
	}
	if _, removed, _ := srv.store.SyncSiteSponsors(ev, []string{"ING"}, true); removed != 3 {
		t.Errorf("removed %d, want 3 (JUG, Sentry, Microsoft Azure)", removed)
	}
	if _, ok := srv.store.Sponsor(harness.Token); !ok {
		t.Error("a link with a PIN must never be removed")
	}

	var ing Sponsor
	for _, sp := range sps {
		if sp.Name == "ING" {
			ing = sp
		}
	}
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	base := ts.URL + "/s/" + ing.Token

	// No PIN yet: the link says so and no PIN opens it, not even an empty one.
	if body := get(t, c, base); !strings.Contains(body, "isn't switched on yet") {
		t.Errorf("expected inactive page, got:\n%s", body)
	}
	c.PostForm(base+"/pin", url.Values{"pin": {""}})
	if body := get(t, c, base); strings.Contains(body, "Start camera") {
		t.Fatal("link without a PIN must not open")
	}

	admin("POST", "/admin/pin/"+ing.Token)
	sp, _ := srv.store.Sponsor(ing.Token)
	if len(sp.PIN) != 4 {
		t.Fatalf("PIN = %q", sp.PIN)
	}
	c.PostForm(base+"/pin", url.Values{"pin": {sp.PIN}})
	if body := get(t, c, base); !strings.Contains(body, "Start camera") {
		t.Fatal("expected scanner after PIN")
	}

	// A new PIN signs the phone out.
	for sp2 := sp; sp2.PIN == sp.PIN; sp2, _ = srv.store.Sponsor(ing.Token) {
		admin("POST", "/admin/pin/"+ing.Token)
	}
	if body := get(t, c, base); strings.Contains(body, "Start camera") {
		t.Error("old PIN session should be signed out")
	}
}
