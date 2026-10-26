package main

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
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
	pins := map[string]bool{}
	for _, sp := range sps {
		if len(sp.PIN) != 4 || pins[sp.PIN] || !sp.FromSite || sp.EventID != "evt-1" || sp.EventStart.IsZero() {
			t.Errorf("site sponsor %+v should be on evt-1 with its own PIN and the event times", sp)
		}
		pins[sp.PIN] = true
	}
	if !strings.Contains(body, "JUG Amsterdam") || !strings.Contains(body, "Live until") {
		t.Errorf("admin page missing site sponsors or phase:\n%s", body)
	}

	// A second sync, even forced, adds nothing.
	admin("POST", "/admin/refresh")
	if n := len(srv.store.Sponsors()); n != 5 {
		t.Errorf("after resync: %d sponsor links", n)
	}

	byName := func(name string) Sponsor {
		for _, sp := range srv.store.Sponsors() {
			if sp.Name == name {
				return sp
			}
		}
		t.Fatalf("no sponsor %q", name)
		return Sponsor{}
	}
	open := func(sp Sponsor, pin string) *http.Client {
		jar, _ := cookiejar.New(nil)
		c := &http.Client{Jar: jar}
		c.PostForm(ts.URL+"/s/"+sp.Token+"/pin", url.Values{"pin": {pin}})
		return c
	}

	// Taken off the page: never-opened links go, opened ones stay.
	harness := byName("Harness")
	open(harness, harness.PIN)
	ev := LumaEvent{APIId: "evt-1", Name: "SREday Test", StartAt: time.Now().Format(time.RFC3339)}
	if _, removed, _ := srv.store.SyncSiteSponsors(ev, []string{"ING"}, false); removed != 0 {
		t.Errorf("prune=false removed %d", removed)
	}
	if _, removed, _ := srv.store.SyncSiteSponsors(ev, []string{"ING"}, true); removed != 3 {
		t.Errorf("removed %d, want 3 (JUG, Sentry, Microsoft Azure)", removed)
	}
	if _, ok := srv.store.Sponsor(harness.Token); !ok {
		t.Error("a link that was opened must never be removed")
	}

	ing := byName("ING")
	base := ts.URL + "/s/" + ing.Token
	if body := get(t, open(ing, ""), base); strings.Contains(body, "Start camera") {
		t.Fatal("an empty PIN must not open the link")
	}
	c := open(ing, ing.PIN)
	if body := get(t, c, base); !strings.Contains(body, "Start camera") {
		t.Fatal("expected scanner after PIN")
	}

	// A new PIN signs the phone out.
	admin("POST", "/admin/pin/"+ing.Token)
	if sp, _ := srv.store.Sponsor(ing.Token); sp.PIN == ing.PIN || len(sp.PIN) != 4 {
		t.Fatalf("new PIN = %q (old %q)", sp.PIN, ing.PIN)
	}
	if body := get(t, c, base); strings.Contains(body, "Start camera") {
		t.Error("old PIN session should be signed out")
	}
}
