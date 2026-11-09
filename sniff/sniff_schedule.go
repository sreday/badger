package sniff

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"github.com/sreday/badger/sites"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The sessions Sniff gives a QR to come from the event page's schedule:
// /<event>/live/ (the venue screen, built by the site's live.py) carries
// every session's start and end, room, title and speakers as JSON in
// <script id="live-data">. Events whose site has no live page yet fall back
// to _db/talks.csv (titles and speakers, no times).

// SchedItem is one session on the event's schedule.
type SchedItem struct {
	Key   string // the talk page's short_url: the session's identity on the site
	Title string
	Who   string // "Ada Lovelace & Grace Hopper"
	Org   string
	Room  string
	Kind  string // keynote, talk, workshop
	Start time.Time
	End   time.Time
}

var (
	schedMu    sync.Mutex
	schedCache = map[string]schedEntry{}
)

type schedEntry struct {
	items []SchedItem
	at    time.Time
}

// EventSchedule reads the event's schedule (cached 10 minutes; force skips
// the cache).
func EventSchedule(repos []string, eventID, eventName string, force bool) ([]SchedItem, error) {
	schedMu.Lock()
	if e, ok := schedCache[eventID]; ok && !force && time.Since(e.at) < 10*time.Minute {
		schedMu.Unlock()
		return e.items, nil
	}
	schedMu.Unlock()

	dir, err := sites.FindEventDir(repos, eventID, eventName)
	if err != nil {
		return nil, err
	}
	var items []SchedItem
	if base := metaValue(dir.Metadata, "base_path"); base != "" && !strings.HasPrefix(dir.Dir, "past/") {
		if body, err := sites.Get(strings.TrimRight(base, "/") + "/" + path.Base(dir.Dir) + "/live/"); err == nil {
			items, err = parseLiveSchedule(body)
			if err != nil {
				return nil, fmt.Errorf("%s/live/: %w", path.Base(dir.Dir), err)
			}
		}
	}
	if items == nil {
		body, err := sites.Get(dir.Raw("_db/talks.csv"))
		if err != nil {
			return nil, fmt.Errorf("%s/%s talks.csv: %w", dir.Repo, dir.Dir, err)
		}
		items = parseTalksSchedule(body)
	}
	schedMu.Lock()
	schedCache[eventID] = schedEntry{items, time.Now()}
	schedMu.Unlock()
	return items, nil
}

// metaValue reads one top-level key of metadata.yml.
func metaValue(yml, key string) string {
	for _, line := range strings.Split(yml, "\n") {
		line = strings.TrimRight(sites.StripComment(line), " \t\r")
		if k, v, ok := strings.Cut(line, ":"); ok && k == key {
			return sites.Unquote(v)
		}
	}
	return ""
}

var liveDataRe = regexp.MustCompile(`(?s)<script id="live-data" type="application/json">(.*?)</script>`)

// parseLiveSchedule reads the sessions out of the live page's JSON.
func parseLiveSchedule(page []byte) ([]SchedItem, error) {
	m := liveDataRe.FindSubmatch(page)
	if m == nil {
		return nil, fmt.Errorf("no live-data")
	}
	var data struct {
		Days [][]struct {
			Room  string `json:"room"`
			Items []struct {
				Start string `json:"start"`
				End   string `json:"end"`
				Kind  string `json:"kind"`
				Title string `json:"title"`
				Who   string `json:"who"`
				Org   string `json:"org"`
				URL   string `json:"url"`
			} `json:"items"`
		} `json:"days"`
	}
	if err := json.Unmarshal([]byte(strings.ReplaceAll(string(m[1]), `<\/`, `</`)), &data); err != nil {
		return nil, err
	}
	items := []SchedItem{}
	for _, day := range data.Days {
		for _, room := range day {
			for _, it := range room.Items {
				if it.Kind != "keynote" && it.Kind != "talk" && it.Kind != "workshop" {
					continue
				}
				key := strings.TrimSuffix(path.Base(it.URL), ".html")
				if key == "" || key == "." || key == "/" {
					key = talkURL(it.Who, it.Org, it.Title)
				}
				s := SchedItem{Key: key, Title: strings.TrimSpace(it.Title), Who: strings.TrimSpace(it.Who), Org: strings.TrimSpace(it.Org), Room: room.Room, Kind: it.Kind}
				s.Start, _ = time.Parse(time.RFC3339, it.Start)
				s.End, _ = time.Parse(time.RFC3339, it.End)
				items = append(items, s)
			}
		}
	}
	return items, nil
}

// parseTalksSchedule reads the sessions out of talks.csv: no times; long
// sessions (the same talk on back-to-back rows) count once.
func parseTalksSchedule(data []byte) []SchedItem {
	rows := readCSV(data)
	if len(rows) == 0 {
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
	items := []SchedItem{}
	seen := map[string]bool{}
	for _, row := range rows[1:] {
		name := get(row, "name")
		if name == "" || strings.HasPrefix(name, "_") || !sites.LiveStatus(get(row, "status")) {
			continue
		}
		kind := "talk"
		for _, k := range []string{"keynote", "workshop"} {
			if strings.Contains(strings.ToLower(get(row, "status")), k) {
				kind = k
			}
		}
		s := SchedItem{Title: get(row, "title"), Who: name, Org: get(row, "organization"), Kind: kind}
		s.Key = talkURL(s.Who, s.Org, s.Title)
		if seen[s.Key] {
			continue
		}
		seen[s.Key] = true
		items = append(items, s)
	}
	return items
}

var nonURL = regexp.MustCompile(`[^A-Za-z0-9_]+`)

// talkURL is the site's generate_talk_url: name, company and title with
// spaces (and commas in the title) as underscores, everything else dropped,
// cut to 100 characters.
func talkURL(name, org, title string) string {
	u := strings.ReplaceAll(name, " ", "_")
	if org != "" {
		u += "_" + strings.ReplaceAll(org, " ", "_")
	}
	if title != "" {
		u += "_" + strings.ReplaceAll(strings.ReplaceAll(title, ",", "_"), " ", "_")
	}
	u = nonURL.ReplaceAllString(u, "")
	if len(u) > 100 {
		u = u[:100]
	}
	return u
}

// speakersOf splits "Ada Lovelace & Grace Hopper" into people.
func speakersOf(who, org string) []Who {
	var out []Who
	for _, n := range sites.SplitNames(who) {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, Who{Name: n, Company: org, Title: "Speaker"})
		}
	}
	return out
}

// readCSV reads a whole CSV file leniently (BOM, ragged rows, stray quotes).
func readCSV(data []byte) [][]string {
	r := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	rows, err := r.ReadAll()
	if err != nil {
		return nil
	}
	return rows
}
