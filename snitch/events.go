package snitch

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sreday/badger/sites"
)

// Event is one conference a mission can be about. The list comes from the
// websites: every in-person event's metadata.yml in the site repos (also
// the ones already moved to past/), and Conf42's events list.
type Event struct {
	ID     string // brand/folder, e.g. sreday/2026-campinas-q4, conf42/mlops2026
	Brand  string // sreday llmday platformday pec conf42
	Name   string // SREday Campinas
	Day    string // first day, 2026-12-02
	EndDay string // last day
	URL    string // the event's page
	Base   string // the brand's site, with the trailing slash
	CFP    string
	Luma   string // in-person: the Luma event id; Conf42: the Luma link
}

// Conf42URL is Conf42's events list (the site's own metadata, public).
var Conf42URL = "https://raw.githubusercontent.com/conf42/src/main/metadata.yml"

const (
	eventsBack  = 7   // days an event stays after its last day
	eventsAhead = 183 // six months
	eventsEvery = 6 * time.Hour
)

var brandNames = map[string]string{"sreday": "SREday", "llmday": "LLMday", "platformday": "PLATFORMday", "pec": "PEC", "conf42": "Conf42"}

var brandBases = map[string]string{
	"sreday": "https://www.sreday.com/", "llmday": "https://www.llmday.com/", "platformday": "https://www.platformday.com/",
	"pec": "https://promptengineering.rocks/", "conf42": "https://www.conf42.com/",
}

// Events is the cached list; a refresh that fails keeps the last good one.
type Events struct {
	Repos []string
	path  string // events.json, so a restart has the list at once
	mu    sync.Mutex
	list  []Event
	at    time.Time
	err   string
}

func (e *Events) load() {
	e.mu.Lock()
	defer e.mu.Unlock()
	readJSON(e.path, &e.list)
}

// Get returns the events from a week ago to six months ahead, soonest first.
func (e *Events) Get(now time.Time) []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return inWindow(e.list, now)
}

func inWindow(list []Event, now time.Time) []Event {
	today := dateOf(now, time.UTC)
	var out []Event
	for _, ev := range list {
		if parseDay(ev.EndDay).Before(today.AddDate(0, 0, -eventsBack)) || parseDay(ev.Day).After(today.AddDate(0, 0, eventsAhead)) {
			continue
		}
		out = append(out, ev)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Day < out[j].Day })
	return out
}

// Refresh reads the websites again.
func (e *Events) Refresh(now time.Time) {
	list, err := readEvents(e.Repos, now)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.at, e.err = now, ""
	if err != "" {
		e.err = err
	}
	if len(list) == 0 {
		return // keep the last good list
	}
	// A brand that couldn't be read this time keeps its old events.
	got := map[string]bool{}
	for _, ev := range list {
		got[ev.Brand] = true
	}
	for _, ev := range e.list {
		if !got[ev.Brand] {
			list = append(list, ev)
		}
	}
	e.list = list
	if e.path != "" {
		writeJSON(e.path, list)
	}
}

var eventFolder = regexp.MustCompile(`^(20\d\d)-.+-q([1-4])$`)

func repoBrand(repo string) string {
	r := strings.ToLower(repo)
	switch {
	case strings.Contains(r, "llmday"):
		return "llmday"
	case strings.Contains(r, "platformday"):
		return "platformday"
	case strings.Contains(r, "promptengineering"):
		return "pec"
	case strings.Contains(r, "sreday"):
		return "sreday"
	}
	return ""
}

func readEvents(repos []string, now time.Time) ([]Event, string) {
	var out []Event
	var errs []string
	lo, hi := now.AddDate(0, 0, -eventsBack-7), now.AddDate(0, 0, eventsAhead+7)
	for _, repo := range repos {
		brand := repoBrand(repo)
		if brand == "" {
			continue
		}
		seen := map[string]bool{}
		for _, prefix := range []string{"", "past/"} {
			body, err := sites.Get(sites.APIURL + "/repos/" + repo + "/contents/" + strings.TrimSuffix(prefix, "/"))
			if err != nil {
				if prefix == "" {
					errs = append(errs, repo+": "+err.Error())
				}
				continue
			}
			var entries []struct{ Name, Type string }
			if json.Unmarshal(body, &entries) != nil {
				continue
			}
			for _, en := range entries {
				m := eventFolder.FindStringSubmatch(en.Name)
				if en.Type != "dir" || m == nil || seen[en.Name] {
					continue
				}
				// The folder names its quarter: skip the ones far from the window unread.
				y, _ := strconv.Atoi(m[1])
				q, _ := strconv.Atoi(m[2])
				qStart := time.Date(y, time.Month(q*3-2), 1, 0, 0, 0, 0, time.UTC)
				if qStart.AddDate(0, 3, 0).Before(lo) || qStart.After(hi) {
					continue
				}
				meta, err := sites.Get(sites.RawURL + "/" + repo + "/HEAD/" + prefix + url.PathEscape(en.Name) + "/metadata.yml")
				if err != nil {
					continue // a leftover folder with no metadata
				}
				if ev, ok := parseEvent(brand, en.Name, string(meta)); ok {
					seen[en.Name] = true
					out = append(out, ev)
				}
			}
		}
	}
	if body, err := sites.Get(Conf42URL); err != nil {
		errs = append(errs, "conf42: "+err.Error())
	} else {
		out = append(out, parseConf42(string(body))...)
	}
	return out, strings.Join(errs, "; ")
}

// parseEvent reads the few top-level keys of an event's metadata.yml.
func parseEvent(brand, folder, yml string) (Event, bool) {
	ev := Event{ID: brand + "/" + folder, Brand: brand, Base: brandBases[brand]}
	var city string
	days := 1
	var start time.Time
	for _, line := range strings.Split(yml, "\n") {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "-") {
			continue
		}
		k, v, ok := strings.Cut(sites.StripComment(line), ":")
		if !ok {
			continue
		}
		v = sites.Unquote(strings.TrimSpace(v))
		switch strings.TrimSpace(k) {
		case "city_name":
			city = v
		case "start_time":
			start, _ = time.Parse(time.RFC3339, v)
		case "days":
			if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 7 {
				days = n
			}
		case "base_path":
			if strings.HasPrefix(v, "https://") {
				ev.Base = strings.TrimSuffix(v, "/") + "/"
			}
		case "cfp_url":
			if strings.HasPrefix(v, "https://") {
				ev.CFP = v
			}
		case "luma_evt":
			if strings.HasPrefix(v, "evt-") {
				ev.Luma = v
			}
		}
	}
	if start.IsZero() || city == "" {
		return Event{}, false
	}
	ev.Name = brandNames[brand] + " " + city
	ev.Day = start.Format(dayFmt) // the venue's own date
	ev.EndDay = start.AddDate(0, 0, days-1).Format(dayFmt)
	ev.URL = ev.Base + folder + "/"
	return ev, true
}

// parseConf42 reads the events list of Conf42's metadata.yml: name, date,
// short_url, cfp_url and luma_url of each event, nothing else.
func parseConf42(yml string) []Event {
	var out []Event
	var cur *Event
	in := false
	flush := func() {
		if cur != nil && cur.Day != "" && cur.ID != "" {
			cur.EndDay = cur.Day
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
			cur = &Event{Brand: "conf42", Base: brandBases["conf42"], Name: sites.Unquote(strings.TrimSpace(strings.TrimPrefix(line, "  - name:")))}
			continue
		}
		if cur == nil || !strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "     ") {
			continue
		}
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		v = sites.Unquote(strings.TrimSpace(v))
		if v == "~" {
			v = ""
		}
		switch k {
		case "date":
			if _, err := time.Parse(dayFmt, v); err == nil {
				cur.Day = v
			}
		case "short_url":
			if v != "" {
				cur.ID = "conf42/" + v
				cur.URL = cur.Base + v
			}
		case "cfp_url":
			if strings.HasPrefix(v, "https://") {
				cur.CFP = v
			}
		case "luma_url":
			if strings.HasPrefix(v, "https://") {
				cur.Luma = v
			}
		}
	}
	flush()
	return out
}

func eventsPath(dir string) string { return filepath.Join(dir, "events.json") }
