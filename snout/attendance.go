package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"github.com/sreday/badger/luma"
	"github.com/sreday/badger/sites"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
)

// After an event, the sponsor-ready leads get a Check-in column. Luma's
// check-in time says "yes - morning" (before noon, event time) or "yes -
// afternoon". Without one, some people were there anyway: speakers whose
// talk is on the event page (not tagged noshow), people registered by a
// sponsor (their email domain or company is a sponsor's, and their bosses
// sent them), and our own organizers (a conference name as their company).
// They get "no, but was there"; everyone else "no".
const (
	CheckInMorning   = "yes - morning"
	CheckInAfternoon = "yes - afternoon"
	CheckInNo        = "no"
	CheckInThere     = "no, but was there"
)

// EventSite is what an event's page says about who was there.
type EventSite struct {
	Sponsors []sites.Sponsor
	Speakers []SiteSpeaker
}

// SiteSpeaker is one live talks.csv row: one or more presenters.
type SiteSpeaker struct {
	Names     []string
	LinkedIns []string // match keys, as ParseScan gives them
}

// Attendance decides the Check-in column for one event.
type Attendance struct {
	Loc  *time.Location
	Site *EventSite
}

// CheckIn returns the guest's Check-in value.
func (a Attendance) CheckIn(g luma.Guest) string {
	if !g.CheckedInAt.IsZero() {
		loc := a.Loc
		if loc == nil {
			loc = time.UTC
		}
		if g.CheckedInAt.In(loc).Hour() < 12 {
			return CheckInMorning
		}
		return CheckInAfternoon
	}
	if isOrganizer(g) || a.isSpeaker(g) || a.isSponsor(g) {
		return CheckInThere
	}
	return CheckInNo
}

// organizerRe matches our conference names on a badge's company line.
var organizerRe = regexp.MustCompile(`(?i)sreday|llmday|platformday|prompt\s*engineering\s*conf|devops\s*not\s*dead|devopsnotdead|\bpec\b`)

func isOrganizer(g luma.Guest) bool {
	return organizerRe.MatchString(g.Company)
}

func (a Attendance) isSpeaker(g luma.Guest) bool {
	if a.Site == nil {
		return false
	}
	slug := ""
	if k, ok := ParseScan(g.LinkedIn); ok && k.Kind == "linkedin" {
		slug = k.Value
	}
	name := normName(g.Name)
	if name == "" {
		name = normName(g.FirstName + " " + g.LastName)
	}
	for _, sp := range a.Site.Speakers {
		for _, l := range sp.LinkedIns {
			if slug != "" && l == slug {
				return true
			}
		}
		for _, n := range sp.Names {
			if name != "" && normName(n) == name {
				return true
			}
		}
	}
	return false
}

func (a Attendance) isSponsor(g luma.Guest) bool {
	if a.Site == nil {
		return false
	}
	domain := ""
	if at := strings.LastIndex(g.Email, "@"); at >= 0 {
		domain = baseDomain(g.Email[at+1:])
	}
	company := normWord(g.Company)
	for _, sp := range a.Site.Sponsors {
		sd := ""
		if u, err := url.Parse(sp.URL); err == nil {
			sd = baseDomain(u.Hostname())
		}
		if domain != "" && sd != "" && domain == sd {
			return true
		}
		if company != "" && (company == normWord(sp.Name) || (sd != "" && company == normWord(strings.SplitN(sd, ".", 2)[0]))) {
			return true
		}
	}
	return false
}

// baseDomain trims a host to the registered domain: mail.harness.io ->
// harness.io, team.example.co.uk -> example.co.uk.
func baseDomain(host string) string {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	parts := strings.Split(host, ".")
	if len(parts) < 2 {
		return host
	}
	n := 2
	if len(parts) >= 3 && len(parts[len(parts)-1]) == 2 {
		switch parts[len(parts)-2] {
		case "co", "com", "org", "net", "ac", "gov", "edu":
			n = 3
		}
	}
	return strings.Join(parts[len(parts)-n:], ".")
}

var nonWord = regexp.MustCompile(`[^\p{L}\p{N}]+`)

func normWord(s string) string { return nonWord.ReplaceAllString(strings.ToLower(s), "") }

func normName(s string) string {
	return strings.Join(strings.Fields(nonWord.ReplaceAllString(strings.ToLower(s), " ")), " ")
}

// --- Reading the event page ---------------------------------------------

var (
	siteMu    sync.Mutex
	siteCache = map[string]siteEntry{}
)

type siteEntry struct {
	site *EventSite
	at   time.Time
}

// FindEventSite finds the event's folder in the site repos (top level or
// past/) by its luma_evt, and reads its sponsors and live talks. Results
// are cached for 10 minutes.
func FindEventSite(repos []string, eventID, eventName string) (*EventSite, error) {
	siteMu.Lock()
	if e, ok := siteCache[eventID]; ok && time.Since(e.at) < 10*time.Minute {
		siteMu.Unlock()
		return e.site, nil
	}
	siteMu.Unlock()

	dir, err := sites.FindEventDir(repos, eventID, eventName)
	if err != nil {
		return nil, err
	}
	_, sponsors := sites.ParseMetadata(dir.Metadata)
	partners, err := sites.Partners(repos)
	if err != nil {
		return nil, err
	}
	site := &EventSite{}
	for _, sp := range sponsors {
		if !partners[strings.ToLower(path.Base(sp.Logo))] {
			site.Sponsors = append(site.Sponsors, sp)
		}
	}
	if talks, err := sites.Get(dir.Raw("_db/talks.csv")); err == nil {
		site.Speakers = parseLiveTalks(talks)
	} else if !strings.Contains(err.Error(), "HTTP 404") {
		return nil, fmt.Errorf("%s/%s talks.csv: %w", dir.Repo, dir.Dir, err)
	}
	siteMu.Lock()
	siteCache[eventID] = siteEntry{site, time.Now()}
	siteMu.Unlock()
	return site, nil
}

// parseLiveTalks reads talks.csv: name ("A & B" for two), status, linkedin,
// linkedin2. Agenda rows (names starting with "_") are skipped.
func parseLiveTalks(data []byte) []SiteSpeaker {
	r := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	rows, err := r.ReadAll()
	if err != nil || len(rows) == 0 {
		return nil
	}
	col := map[string]int{}
	for i, h := range rows[0] {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	get := func(row []string, k string) string {
		if i, ok := col[k]; ok && i < len(row) {
			return strings.TrimSpace(row[i])
		}
		return ""
	}
	var out []SiteSpeaker
	for _, row := range rows[1:] {
		name := get(row, "name")
		if name == "" || strings.HasPrefix(name, "_") || !sites.LiveStatus(get(row, "status")) {
			continue
		}
		sp := SiteSpeaker{Names: sites.SplitNames(name)}
		for _, k := range []string{"linkedin", "linkedin2"} {
			if key, ok := ParseScan(get(row, k)); ok && key.Kind == "linkedin" {
				sp.LinkedIns = append(sp.LinkedIns, key.Value)
			}
		}
		out = append(out, sp)
	}
	return out
}
