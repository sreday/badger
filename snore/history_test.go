package snore

import (
	"strings"
	"testing"
	"time"
)

func TestCompanyDomain(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.example.com/pricing":      "example.com",
		"http://Docs.Example.co.uk":            "example.co.uk",
		"example.io":                           "example.io",
		"https://www.linkedin.com/company/x":   "",
		"https://www.meetup.com/some-group/":   "",
		"https://community.cncf.io/some-group": "",
		"":                                     "",
		"not a link":                           "",
		"https://localhost/x":                  "",
	} {
		if got := companyDomain(in); got != want {
			t.Errorf("companyDomain(%q) = %q, want %q", in, got, want)
		}
	}
}

const metaWithSponsors = `luma_evt: evt-abc
city_name: Testville
start_time: 2026-05-01T09:00:00Z
sponsors:
  - logo: ./assets/sponsors/acme.png
    url: https://www.acme.example.com
  - logo: ./assets/sponsors/acme-dark.png
    url: https://acme.example.com/again
  - logo: ./assets/sponsors/friends.png
    url: https://friends.example.org
  - logo: ./assets/sponsors/meetup-group.png
    url: https://www.meetup.com/some-group/
`

func TestSponsorsOf(t *testing.T) {
	luma, got := sponsorsOf(metaWithSponsors, map[string]bool{"friends.png": true})
	if luma != "evt-abc" {
		t.Fatalf("luma id = %q", luma)
	}
	if len(got) != 2 {
		t.Fatalf("want the company once and the one without its own site, got %+v", got)
	}
	if got[0].Domain != "example.com" || got[1].Domain != "" {
		t.Fatalf("domains = %q, %q", got[0].Domain, got[1].Domain)
	}
}

func TestSponsorHistoryAndTouches(t *testing.T) {
	s, _ := testServer(t)
	s.fp.F["x/sreday/past/2025-amsterdam-q2"].Sponsors = []Sponsored{{Name: "Acme", Domain: "acme.example"}, {Name: "Loose", Domain: ""}}
	s.fp.F["x/sreday/past/2025-amsterdam-q2"].Key = "x/sreday/past/2025-amsterdam-q2"
	s.fp.F["x/llmday/2026-bengaluru-q4"].Sponsors = []Sponsored{{Name: "Acme", Domain: "acme.example"}}
	s.fp.F["x/llmday/2026-bengaluru-q4"].Key = "x/llmday/2026-bengaluru-q4"

	known, unknown := s.fp.History()
	if len(known) != 1 || known[0].Domain != "acme.example" || len(known[0].Events) != 2 {
		t.Fatalf("history = %+v", known)
	}
	if len(known[0].Brands) != 2 || len(known[0].Cities) != 2 || known[0].Events[0] != "x/sreday/past/2025-amsterdam-q2" {
		t.Fatalf("brands, cities, order = %+v", known[0])
	}
	if len(unknown) != 1 || unknown[0] != "Loose" {
		t.Fatalf("unknown = %v", unknown)
	}

	touches := s.fp.SponsorTouches()
	if len(touches) != 2 || touches[0].Kind != TouchSponsor || touches[0].By != Organizer {
		t.Fatalf("touches = %+v", touches)
	}
	// Sponsored in May 2025 and December 2026: a partner today.
	w := warmthOf(touches, time.Date(2026, 11, 21, 6, 0, 0, 0, time.UTC))
	if w.Level != 7 || w.Owner != Organizer {
		t.Fatalf("a past sponsor should be level 7: %+v", w)
	}
}

const conf42Meta = `title: x
events:
  - name: Topic One 2026
    date: 2026-03-05
    short_url: one2026
    db_path: ./_db/one2026.csv
    sponsors:
      diamond:
        - acme
      gold:
        - ownbrand
        - unknownid
      media:
        - paper
  - name: Topic Two 2026
    date: 2026-04-02
    short_url: two2026
    sponsors:
      silver:
        - "beta"
other: x
`

func TestConf42PaidSponsors(t *testing.T) {
	evs := parseConf42(conf42Meta)
	if len(evs) != 2 {
		t.Fatalf("events = %+v", evs)
	}
	if got := strings.Join(evs[0].Paid, ","); got != "acme,ownbrand,unknownid" {
		t.Fatalf("paid ids of the first event = %q (media partners are not paid)", got)
	}
	if got := strings.Join(evs[1].Paid, ","); got != "beta" {
		t.Fatalf("paid ids of the second event = %q", got)
	}
	if evs[0].DB != "./_db/one2026.csv" || evs[0].Short != "one2026" {
		t.Fatalf("the plain fields still read: %+v", evs[0])
	}
	ids := conf42Sponsors([]byte("id,name,url\nacme,Acme,https://www.acme.example\nownbrand,Ours,https://www.conf42.com\nbeta,Beta,https://beta.example/en/\n"))
	if ids["acme"].Domain != "acme.example" || ids["beta"].Name != "Beta" || !ownDomains[ids["ownbrand"].Domain] {
		t.Fatalf("ids = %+v", ids)
	}
}
