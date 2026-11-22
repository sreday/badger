package snore

import (
	"bytes"
	"encoding/csv"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/sreday/badger/sites"
	"golang.org/x/net/publicsuffix"
)

// Our own sponsor history: who sponsored which of our events, read from the
// event pages (past events too). A company is known by its website domain.

// folderVer: a folder read before this version is read once more, so old
// events get their sponsors too.
const folderVer = 3

// Sponsored is one paying sponsor listed on an event page.
type Sponsored struct {
	Name   string `json:"name"`
	Domain string `json:"domain,omitempty"` // empty when the link is not the company's own site
	Logo   string `json:"logo,omitempty"`
}

// sharedHosts are sites many companies link to instead of their own: a
// link there says nothing about whose domain it is.
var sharedHosts = map[string]bool{
	"linkedin.com": true, "meetup.com": true, "github.com": true, "github.io": true, "x.com": true, "twitter.com": true,
	"youtube.com": true, "youtu.be": true, "facebook.com": true, "instagram.com": true, "medium.com": true,
	"lu.ma": true, "luma.com": true, "eventbrite.com": true, "eventbrite.co.uk": true, "linktr.ee": true,
	"notion.site": true, "substack.com": true, "google.com": true, "bit.ly": true, "cncf.io": true,
	"discord.gg": true, "discord.com": true, "slack.com": true, "t.me": true, "bsky.app": true,
}

// companyDomain: the registrable domain of a company link (www.example.co.uk
// -> example.co.uk), or "" for a shared site, an address or nothing.
func companyDomain(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" || !strings.Contains(host, ".") {
		return ""
	}
	d, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil || sharedHosts[d] {
		return ""
	}
	return d
}

// sponsorsOf reads an event page's paying sponsors, partners left out.
func sponsorsOf(meta string, partners map[string]bool) (luma string, out []Sponsored) {
	id, list := sites.ParseMetadata(meta)
	seen := map[string]bool{}
	for _, sp := range list {
		if partners[strings.ToLower(path.Base(sp.Logo))] {
			continue
		}
		s := Sponsored{Name: sp.Name, Domain: companyDomain(sp.URL), Logo: path.Base(sp.Logo)}
		if ownDomains[s.Domain] {
			continue
		}
		k := s.Domain
		if k == "" {
			k = "name:" + normName(s.Name)
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, s)
	}
	return id, out
}

// SponsorTouches: one touch per sponsor and event, for the companies whose
// domain is known. The date is the event's first day.
func (fp *Footprint) SponsorTouches() []Touch {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	var out []Touch
	for _, f := range fp.F {
		if f.Start.IsZero() {
			continue
		}
		for _, sp := range f.Sponsors {
			if sp.Domain == "" {
				continue
			}
			out = append(out, Touch{Company: sp.Domain, By: Organizer, Kind: TouchSponsor, Event: f.Key, At: f.Start})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Company != out[j].Company {
			return out[i].Company < out[j].Company
		}
		return out[i].At.Before(out[j].At)
	})
	return out
}

// SponsorHistory is one company's record with us.
type SponsorHistory struct {
	Domain string
	Name   string
	Events []string // folder keys, oldest first
	Brands []string
	Cities []string
}

// History groups our sponsors by company. Sponsors whose link is not their
// own site are grouped by name under Unknown, to be matched by hand.
func (fp *Footprint) History() (known []SponsorHistory, unknown []string) {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	keys := make([]string, 0, len(fp.F))
	for k := range fp.F {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return fp.F[keys[i]].Start.Before(fp.F[keys[j]].Start) })
	by := map[string]*SponsorHistory{}
	loose := map[string]bool{}
	for _, k := range keys {
		f := fp.F[k]
		for _, sp := range f.Sponsors {
			if sp.Domain == "" {
				loose[sp.Name] = true
				continue
			}
			h := by[sp.Domain]
			if h == nil {
				h = &SponsorHistory{Domain: sp.Domain, Name: sp.Name}
				by[sp.Domain] = h
			}
			h.Events = append(h.Events, f.Key)
			if !contains(h.Brands, f.Brand) {
				h.Brands = append(h.Brands, f.Brand)
			}
			if f.Brand != "conf42" && !contains(h.Cities, f.Name) {
				h.Cities = append(h.Cities, f.Name)
			}
		}
	}
	for _, h := range by {
		known = append(known, *h)
	}
	sort.Slice(known, func(i, j int) bool {
		if len(known[i].Events) != len(known[j].Events) {
			return len(known[i].Events) > len(known[j].Events)
		}
		return known[i].Domain < known[j].Domain
	})
	for n := range loose {
		unknown = append(unknown, n)
	}
	sort.Strings(unknown)
	return known, unknown
}

// ownDomains are our own brands: they list each other as sponsors, which
// is no sponsorship.
var ownDomains = map[string]bool{
	"sreday.com": true, "llmday.com": true, "platformday.com": true, "conf42.com": true, "promptengineering.rocks": true,
	"reliaburger.com": true, "cfp.ninja": true, "certified.fyi": true, "hockeystick.show": true, "devopsnotdead.com": true,
}

// conf42Sponsors reads the id,name,url list of Conf42 sponsors.
func conf42Sponsors(data []byte) map[string]Sponsored {
	out := map[string]Sponsored{}
	rows, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	if err != nil {
		return out
	}
	for i, r := range rows {
		if i == 0 || len(r) < 3 || strings.TrimSpace(r[0]) == "" {
			continue
		}
		out[strings.TrimSpace(r[0])] = Sponsored{Name: strings.TrimSpace(r[1]), Domain: companyDomain(r[2])}
	}
	return out
}
