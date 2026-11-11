package snap

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/sreday/badger/sites"
	"golang.org/x/text/unicode/norm"
)

// Talk is one live row of an upcoming event's talks.csv.
type Talk struct {
	Names  []string // presenters
	Status string
	Title  string
	Org    string
}

// Lineup is an upcoming event's live talks.
type Lineup struct {
	Repo, Dir string // sreday/sreday, 2026-nyc-q4
	Brand     string // SREday
	City      string // NYC
	Start     time.Time
	Talks     []Talk
}

func (l Lineup) Name() string {
	s := strings.TrimSpace(l.Brand + " " + l.City)
	if !l.Start.IsZero() {
		s += " " + l.Start.Format("2006")
	}
	return s
}

// Lineups reads every upcoming event's talks.csv from the site repos, at
// most once an hour (GitHub's API allows 60 unauthenticated calls an hour).
type Lineups struct {
	Repos []string
	mu    sync.Mutex
	at    time.Time
	list  []Lineup
	err   error
}

func (ls *Lineups) Get(now time.Time) ([]Lineup, error) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if !ls.at.IsZero() && now.Sub(ls.at) < time.Hour {
		return ls.list, ls.err
	}
	list, err := readLineups(ls.Repos, now)
	if err != nil && len(list) == 0 && len(ls.list) > 0 {
		return ls.list, err // keep the last good read
	}
	ls.list, ls.err, ls.at = list, err, now
	return list, err
}

var eventDir = regexp.MustCompile(`^20\d\d-`)

func readLineups(repos []string, now time.Time) ([]Lineup, error) {
	var out []Lineup
	var errs []string
	for _, repo := range repos {
		body, err := sites.Get(sites.APIURL + "/repos/" + repo + "/contents/")
		if err != nil {
			errs = append(errs, repo+": "+err.Error())
			continue
		}
		var entries []struct{ Name, Type string }
		if err := json.Unmarshal(body, &entries); err != nil {
			errs = append(errs, repo+": "+err.Error())
			continue
		}
		for _, e := range entries {
			if e.Type != "dir" || !eventDir.MatchString(e.Name) {
				continue
			}
			l := Lineup{Repo: repo, Dir: e.Name}
			raw := sites.RawURL + "/" + repo + "/HEAD/" + url.PathEscape(e.Name) + "/"
			if meta, err := sites.Get(raw + "metadata.yml"); err == nil {
				l.Brand, l.City, l.Start = parseMeta(string(meta))
			}
			if !l.Start.IsZero() && l.Start.Before(now.Add(-24*time.Hour)) {
				continue // over: past events aren't changed any more
			}
			csvData, err := sites.Get(raw + "_db/talks.csv")
			if err != nil {
				csvData, err = sites.Get(raw + "talks.csv")
			}
			if err != nil {
				continue // no lineup yet
			}
			l.Talks = parseTalks(csvData)
			out = append(out, l)
		}
	}
	if len(errs) > 0 {
		return out, fmt.Errorf("reading lineups: %s", strings.Join(errs, "; "))
	}
	return out, nil
}

func parseMeta(yml string) (brand, city string, start time.Time) {
	for _, line := range strings.Split(yml, "\n") {
		k, v, ok := strings.Cut(sites.StripComment(line), ":")
		if !ok || strings.HasPrefix(line, " ") {
			continue
		}
		v = sites.Unquote(strings.TrimSpace(v))
		switch strings.TrimSpace(k) {
		case "brand_name":
			brand = v
		case "city_name":
			city = v
		case "start_time":
			start, _ = time.Parse(time.RFC3339, v)
		}
	}
	return
}

func parseTalks(data []byte) []Talk {
	r := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	rows, err := r.ReadAll()
	if err != nil || len(rows) < 2 {
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
	var out []Talk
	for _, row := range rows[1:] {
		st := get(row, "status")
		if !sites.LiveStatus(st) {
			continue
		}
		name := get(row, "name")
		if name == "" {
			continue
		}
		out = append(out, Talk{Names: sites.SplitNames(name), Status: st, Title: get(row, "title"), Org: get(row, "organization")})
	}
	return out
}

var nonLetters = regexp.MustCompile(`[^\p{L}\p{N}]+`)

// normName folds case, accents and punctuation: "José  O'Neil" -> "jose oneil".
func normName(s string) string {
	s = norm.NFD.String(strings.ToLower(s))
	var b strings.Builder
	for _, r := range s {
		if r >= 0x300 && r <= 0x36f {
			continue // combining accents
		}
		if r == '\'' || r == '’' {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(nonLetters.ReplaceAllString(b.String(), " "))
}

// sameName: the full names match, or first + last name do (a middle name
// on one side only).
func sameName(a, b string) bool {
	a, b = normName(a), normName(b)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	fa, fb := strings.Fields(a), strings.Fields(b)
	if len(fa) < 2 || len(fb) < 2 {
		return false
	}
	return fa[0] == fb[0] && fa[len(fa)-1] == fb[len(fb)-1]
}

// StillListed says where the speaker still has a live talk. The event they
// named comes first; any other upcoming lineup counts too (people name the
// city wrong).
func StillListed(lineups []Lineup, name, event string) string {
	if name == "" {
		return ""
	}
	ev := normName(event)
	var hits []string
	for _, l := range lineups {
		for _, t := range l.Talks {
			for _, n := range t.Names {
				if !sameName(n, name) {
					continue
				}
				where := fmt.Sprintf("Still on the %s lineup (%s/%s talks.csv, status %s)", l.Name(), l.Repo, l.Dir, t.Status)
				if t.Title != "" {
					where += ": " + t.Title
				}
				city := normName(l.City)
				if ev != "" && city != "" && strings.Contains(ev, city) {
					return where
				}
				hits = append(hits, where)
			}
		}
	}
	if len(hits) > 0 {
		return hits[0]
	}
	return ""
}
