package snort

import (
	"encoding/json"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Event is one conference Snort can push. Everything here is public: it
// comes from the sites' own repos, never from a sheet.
type Event struct {
	ID    string // brand/folder (in-person) or conf42/<short_url>
	Brand string // sreday llmday platformday conf42
	Name  string // SREday Amsterdam 2026 Q4, Conf42 DevOps 2027
	Day   string // first day, 2026-12-02
	URL   string // the event's page
	Repo  string // owner/name of the site repo
	File  string // the lineup file in that repo, e.g. 2026-london-q4/_db/talks.csv

	// What the feed is asked for. In-person: the city, year and quarter, the
	// feed finds the tab itself. Conf42: the tab, named as the sheet file.
	City    string
	Year    int
	Quarter int
	Tab     string
	Suffix  string // Conf42: the headshot sky of the event, e.g. obs
}

// Brands, in the order of the board's tabs.
// PEC is left out of Snort for now (Marek, 2026-11-25).
var brandOrder = []string{"sreday", "llmday", "platformday", "conf42"}

// siteBrands are the in-person sites, read from their event folders.
var siteBrands = []string{"sreday", "llmday", "platformday"}

var brandTabs = map[string]string{"sreday": "SRE", "llmday": "LLM", "platformday": "PLATFORM", "conf42": "Conf42"}

var brandNames = map[string]string{"sreday": "SREday", "llmday": "LLMday", "platformday": "PLATFORMday", "conf42": "Conf42"}

var brandBases = map[string]string{
	"sreday": "https://www.sreday.com/", "llmday": "https://www.llmday.com/", "platformday": "https://www.platformday.com/",
	"conf42": "https://www.conf42.com/",
}

// The site repos. Conf42's lineups are in src, its headshots in static.
var siteRepos = map[string]string{
	"sreday": "sreday/sreday", "llmday": "sreday/llmday", "platformday": "sreday/platformday",
	"conf42": "conf42/src",
}

// conf42Static holds Conf42's headshots.
const conf42Static = "conf42/static"

const (
	pastShown = 3 // past events listed per brand, for late fixes
	eventsTTL = 6 * time.Hour
)

var eventFolder = regexp.MustCompile(`^(20\d\d)-(.+)-q([1-4])$`)

// readEvents lists every brand's events: upcoming ones, soonest first, then
// the last few past ones. A brand that can't be read keeps what it had.
func (s *Server) readEvents(now time.Time) (map[string][]Event, string) {
	out := map[string][]Event{}
	var errs []string
	for _, brand := range siteBrands {
		list, err := s.readSiteEvents(brand, now)
		if err != nil {
			errs = append(errs, brandNames[brand]+": "+err.Error())
			continue
		}
		out[brand] = list
	}
	if list, err := s.readConf42Events(now); err != nil {
		errs = append(errs, "Conf42: "+err.Error())
	} else {
		out["conf42"] = list
	}
	return out, strings.Join(errs, "; ")
}

func (s *Server) readSiteEvents(brand string, now time.Time) ([]Event, error) {
	repo := siteRepos[brand]
	var all []Event
	for _, prefix := range []string{"", "past/"} {
		body, err := s.gh.get(repo, "contents/"+strings.TrimSuffix(prefix, "/"))
		if err != nil {
			if prefix == "" {
				return nil, err
			}
			continue
		}
		var entries []struct{ Name, Type string }
		if json.Unmarshal(body, &entries) != nil {
			continue
		}
		for _, en := range entries {
			m := eventFolder.FindStringSubmatch(en.Name)
			if en.Type != "dir" || m == nil {
				continue
			}
			y, _ := strconv.Atoi(m[1])
			if y < now.Year()-1 {
				continue // older folders never need a push
			}
			meta, err := s.gh.file(repo, prefix+en.Name+"/metadata.yml")
			if err != nil {
				continue
			}
			q, _ := strconv.Atoi(m[3])
			if ev, ok := parseSiteEvent(brand, prefix, en.Name, y, q, string(meta)); ok {
				all = append(all, ev)
			}
		}
	}
	return pick(all, now), nil
}

// parseSiteEvent reads the few top-level keys of an event's metadata.yml.
func parseSiteEvent(brand, prefix, folder string, year, quarter int, yml string) (Event, bool) {
	ev := Event{ID: brand + "/" + folder, Brand: brand, Repo: siteRepos[brand], File: prefix + folder + "/_db/talks.csv", Year: year, Quarter: quarter}
	var start time.Time
	base := brandBases[brand]
	for _, line := range strings.Split(yml, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "-") || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(stripComment(line), ":")
		if !ok {
			continue
		}
		v = unquote(strings.TrimSpace(v))
		switch strings.TrimSpace(k) {
		case "city_name":
			ev.City = v
		case "start_time":
			start, _ = time.Parse(time.RFC3339, v)
		case "base_path":
			if strings.HasPrefix(v, "https://") {
				base = strings.TrimSuffix(v, "/") + "/"
			}
		}
	}
	if start.IsZero() || ev.City == "" {
		return Event{}, false
	}
	ev.Day = start.Format(dayFmt)
	ev.Name = brandNames[brand] + " " + ev.City + " " + strconv.Itoa(year) + " Q" + strconv.Itoa(quarter)
	ev.URL = base + folder + "/"
	return ev, true
}

// conf42Tab is the name of a Conf42 lineup: DEVOPS_2027 for ./_db/DEVOPS_2027.csv.
var conf42Tab = regexp.MustCompile(`^[A-Z0-9]+_20\d\d$`)

func (s *Server) readConf42Events(now time.Time) ([]Event, error) {
	body, err := s.gh.file(siteRepos["conf42"], "metadata.yml")
	if err != nil {
		return nil, err
	}
	list := parseConf42(string(body))
	list = pick(list, now)
	// Each event's headshot sky: the most common suffix in its own lineup,
	// else in the same topic's last lineup.
	for i := range list {
		list[i].Suffix = s.conf42Suffix(list[i].File)
		if list[i].Suffix == "" {
			if prev := previousLineup(list[i].File); prev != "" {
				list[i].Suffix = s.conf42Suffix(prev)
			}
		}
	}
	return list, nil
}

// parseConf42 reads the events list of Conf42's metadata.yml: name, date,
// short_url and db_path of each event.
func parseConf42(yml string) []Event {
	var out []Event
	var cur *Event
	in := false
	flush := func() {
		if cur != nil && cur.Day != "" && cur.ID != "" && cur.Tab != "" {
			cur.Name = "Conf42 " + cur.Name + " " + cur.Day[:4]
			out = append(out, *cur)
		}
		cur = nil
	}
	for _, line := range strings.Split(yml, "\n") {
		line = strings.TrimRight(line, "\r")
		if !in {
			in = strings.HasPrefix(line, "events:")
			continue
		}
		if line != "" && line[0] != ' ' && line[0] != '#' {
			break // the next top-level key
		}
		if strings.HasPrefix(line, "  - name:") {
			flush()
			cur = &Event{Brand: "conf42", Repo: siteRepos["conf42"], Name: unquote(strings.TrimSpace(strings.TrimPrefix(line, "  - name:")))}
			continue
		}
		if cur == nil || !strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "     ") {
			continue
		}
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		v = unquote(strings.TrimSpace(stripComment(v)))
		switch k {
		case "date":
			if _, err := time.Parse(dayFmt, v); err == nil {
				cur.Day = v
			}
		case "short_url":
			if v != "" && v != "~" {
				cur.ID = "conf42/" + v
				cur.URL = brandBases["conf42"] + v
			}
		case "db_path":
			p := strings.TrimPrefix(v, "./")
			stem := strings.TrimSuffix(path.Base(p), ".csv")
			if strings.HasPrefix(p, "_db/") && strings.HasSuffix(p, ".csv") && conf42Tab.MatchString(stem) {
				cur.File, cur.Tab = p, stem
			}
		}
	}
	flush()
	return out
}

// previousLineup is the same topic's lineup a year earlier: _db/OBSER_2026.csv for _db/OBSER_2027.csv.
func previousLineup(file string) string {
	stem := strings.TrimSuffix(path.Base(file), ".csv")
	topic, year, ok := strings.Cut(stem, "_")
	y, err := strconv.Atoi(year)
	if !ok || err != nil {
		return ""
	}
	return "_db/" + topic + "_" + strconv.Itoa(y-1) + ".csv"
}

var suffixOf = regexp.MustCompile(`_([a-z0-9]+)\.png$`)

// conf42Suffix is the most common headshot suffix in a Conf42 lineup ("" when none).
func (s *Server) conf42Suffix(file string) string {
	body, err := s.gh.file(siteRepos["conf42"], file)
	if err != nil {
		return ""
	}
	header, rows, err := parseCSV(body)
	if err != nil {
		return ""
	}
	col := -1
	for i, h := range header {
		if h == "Picture" || h == "Picture1" {
			col = i
		}
	}
	if col < 0 {
		return ""
	}
	count := map[string]int{}
	for _, r := range rows {
		if col < len(r) {
			if m := suffixOf.FindStringSubmatch(r[col]); m != nil {
				count[m[1]]++
			}
		}
	}
	best, n := "", 0
	for k, c := range count {
		if c > n || (c == n && k < best) {
			best, n = k, c
		}
	}
	return best
}

// pick keeps the upcoming events (soonest first) and the last few past ones.
func pick(list []Event, now time.Time) []Event {
	today := now.UTC().Format(dayFmt)
	var up, past []Event
	for _, ev := range list {
		// An event stays "upcoming" through its day and the next one.
		d, _ := time.Parse(dayFmt, ev.Day)
		if d.AddDate(0, 0, 2).Format(dayFmt) > today {
			up = append(up, ev)
		} else {
			past = append(past, ev)
		}
	}
	sort.SliceStable(up, func(i, j int) bool { return up[i].Day < up[j].Day })
	sort.SliceStable(past, func(i, j int) bool { return past[i].Day > past[j].Day })
	if len(past) > pastShown {
		past = past[:pastShown]
	}
	return append(up, past...)
}

func (ev Event) Upcoming(now time.Time) bool {
	d, _ := time.Parse(dayFmt, ev.Day)
	return d.AddDate(0, 0, 2).After(now.UTC())
}

func stripComment(s string) string {
	if i := strings.Index(s, " #"); i >= 0 {
		return s[:i]
	}
	return s
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}

// feedQuery is what the feed is asked for an event: never a sheet id.
func (ev Event) feedQuery() url.Values {
	q := url.Values{"brand": {ev.Brand}}
	if ev.Brand == "conf42" {
		q.Set("tab", ev.Tab)
	} else {
		q.Set("city", ev.City)
		q.Set("year", strconv.Itoa(ev.Year))
		q.Set("quarter", strconv.Itoa(ev.Quarter))
	}
	return q
}
