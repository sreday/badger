package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
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
	Sponsors []SiteSponsor
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
func (a Attendance) CheckIn(g Guest) string {
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

func isOrganizer(g Guest) bool {
	return organizerRe.MatchString(g.Company)
}

func (a Attendance) isSpeaker(g Guest) bool {
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

func (a Attendance) isSponsor(g Guest) bool {
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

	want := folderGuess(eventName)
	var errs []string
	for _, repo := range repos {
		var dirs []string
		for _, base := range []string{"", "past"} {
			ds, err := listDirs(repo, base)
			if err != nil {
				if base == "past" && strings.Contains(err.Error(), "HTTP 404") {
					continue
				}
				errs = append(errs, fmt.Sprintf("%s/%s: %v", repo, base, err))
				continue
			}
			dirs = append(dirs, ds...)
		}
		// The folder named like the event first, then the rest of that
		// year and quarter, so a renamed city still turns up.
		var exact, near []string
		for _, d := range dirs {
			switch b := path.Base(d); {
			case want != "" && b == want:
				exact = append(exact, d)
			case want != "" && sameYearQuarter(b, want):
				near = append(near, d)
			}
		}
		for _, d := range append(exact, near...) {
			body, err := httpGet(githubRawURL + "/" + repo + "/HEAD/" + d + "/metadata.yml")
			if err != nil {
				continue
			}
			id, sponsors := parseMetadata(string(body))
			if id != eventID {
				continue
			}
			partners, err := loadPartners(repos)
			if err != nil {
				return nil, err
			}
			site := &EventSite{}
			for _, sp := range sponsors {
				if !partners[strings.ToLower(path.Base(sp.Logo))] {
					site.Sponsors = append(site.Sponsors, sp)
				}
			}
			if talks, err := httpGet(githubRawURL + "/" + repo + "/HEAD/" + d + "/_db/talks.csv"); err == nil {
				site.Speakers = parseLiveTalks(talks)
			} else if !strings.Contains(err.Error(), "HTTP 404") {
				return nil, fmt.Errorf("%s/%s talks.csv: %w", repo, d, err)
			}
			siteMu.Lock()
			siteCache[eventID] = siteEntry{site, time.Now()}
			siteMu.Unlock()
			return site, nil
		}
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("event page for %s not found (%s)", eventName, strings.Join(errs, "; "))
	}
	return nil, fmt.Errorf("event page for %s not found in %s", eventName, strings.Join(repos, ", "))
}

// listDirs lists the event folders at the top of a repo or under past/,
// as paths from the repo root.
func listDirs(repo, base string) ([]string, error) {
	body, err := httpGet(githubAPIURL + "/repos/" + repo + "/contents/" + base)
	if err != nil {
		return nil, err
	}
	var entries []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, err
	}
	var dirs []string
	for _, e := range entries {
		if e.Type == "dir" && eventDirRe.MatchString(e.Name) {
			dirs = append(dirs, path.Join(base, e.Name))
		}
	}
	return dirs, nil
}

var (
	yearRe    = regexp.MustCompile(`\b(20\d\d)\b`)
	quarterRe = regexp.MustCompile(`(?i)\bq([1-4])\b`)
	brandRe   = regexp.MustCompile(`(?i)\b(sreday|llmday|platformday|pec|devops not dead|prompt engineering conference)\b|\+|&`)
)

// folderGuess turns "SREday San Francisco 2026 Q4" into 2026-san-francisco-q4.
func folderGuess(name string) string {
	y := yearRe.FindStringSubmatch(name)
	q := quarterRe.FindStringSubmatch(name)
	if y == nil || q == nil {
		return ""
	}
	city := brandRe.ReplaceAllString(name, " ")
	city = yearRe.ReplaceAllString(city, " ")
	city = quarterRe.ReplaceAllString(city, " ")
	words := strings.Fields(nonWord.ReplaceAllString(strings.ToLower(city), " "))
	return y[1] + "-" + strings.Join(words, "-") + "-q" + q[1]
}

func sameYearQuarter(dir, want string) bool {
	return len(dir) > 5 && dir[:5] == want[:5] && strings.HasSuffix(dir, want[len(want)-3:])
}

// loadPartners reads every repo's partners.yaml (lowercased logo names).
func loadPartners(repos []string) (map[string]bool, error) {
	partners := make(map[string]bool)
	for _, repo := range repos {
		body, err := httpGet(githubRawURL + "/" + repo + "/HEAD/partners.yaml")
		if err != nil {
			if strings.Contains(err.Error(), "HTTP 404") {
				continue
			}
			return nil, fmt.Errorf("%s partners.yaml: %w", repo, err)
		}
		for logo := range parsePartners(string(body)) {
			partners[logo] = true
		}
	}
	return partners, nil
}

// liveStatus is the sites' own rule for a talk on the page: talk, keynote,
// workshop or the old "confirmed", never a draft. A noshow row is not live.
var (
	draftRe = regexp.MustCompile(`\bdraft\b`)
	liveRe  = regexp.MustCompile(`keynote|confirmed|\b(talk|workshop)\b`)
	splitRe = regexp.MustCompile(`\s*&\s*|\s+and\s+`)
)

func liveStatus(s string) bool {
	s = strings.ToLower(s)
	return !draftRe.MatchString(s) && liveRe.MatchString(s)
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
		if name == "" || strings.HasPrefix(name, "_") || !liveStatus(get(row, "status")) {
			continue
		}
		sp := SiteSpeaker{Names: splitRe.Split(name, -1)}
		for _, k := range []string{"linkedin", "linkedin2"} {
			if key, ok := ParseScan(get(row, k)); ok && key.Kind == "linkedin" {
				sp.LinkedIns = append(sp.LinkedIns, key.Value)
			}
		}
		out = append(out, sp)
	}
	return out
}
