package snore

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sreday/badger/sites"
)

// The footprint is everywhere we have ever been or plan to be: every event
// folder of the site repos (top level and past/), with its city and its
// lineup, plus Conf42's events and their lineups. It gives Snore its
// topics, the cities each in-person topic covers, and the names that are
// never recommended (anyone who is or was on one of our lineups).

// Conf42URL is Conf42's events list (the site's own metadata, public).
var Conf42URL = "https://raw.githubusercontent.com/conf42/src/main/metadata.yml"

// Conf42Raw is where Conf42's lineup CSVs (db_path) are read from.
var Conf42Raw = "https://raw.githubusercontent.com/conf42/src/main/"

const footprintEvery = 6 * time.Hour

// Folder is one event: an in-person event folder or a Conf42 event.
type Folder struct {
	Key   string    // repo/past/dir, or conf42/<short_url>
	Brand string    // sreday llmday platformday pec conf42
	Name  string    // the city, or the Conf42 topic
	Start time.Time // first day
	Names []string  // everyone on its lineup
	At    time.Time // when it was read

	Sponsors []Sponsored `json:",omitempty"` // paying sponsors on its page
	Luma     string      `json:",omitempty"` // its registration event id
	Ver      int         `json:",omitempty"` // folderVer when it was read
}

// Topic is one tab of the board.
type Topic struct {
	ID     string // sre llm platform, or conf42-<slug>
	Name   string // SRE, LLM, PLATFORM, or the Conf42 topic
	Brand  string // the colour: sreday llmday platformday conf42
	Online bool
	Next   string // Conf42: the event date
	Cities []City
}

// City is one place of our footprint.
type City struct {
	Name   string   `json:"city"`
	Brands []string `json:"brands"`
	Events int      `json:"events_held_or_planned"`
	Next   string   `json:"next_event,omitempty"` // the next date we are there
	Last   string   `json:"last_event,omitempty"`
}

type Footprint struct {
	Repos []string
	path  string
	mu    sync.Mutex
	F     map[string]*Folder
	At    time.Time
	Err   string
}

func (fp *Footprint) load() {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	readJSON(fp.path, &fp.F)
	if fp.F == nil {
		fp.F = map[string]*Folder{}
	}
}

var eventDir = regexp.MustCompile(`^20\d\d-`)

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

// Refresh reads the websites again. Events long over are read once and
// kept: their lineups don't change any more.
func (fp *Footprint) Refresh(now time.Time) {
	fp.mu.Lock()
	old := fp.F
	fp.mu.Unlock()
	next := map[string]*Folder{}
	var errs []string
	settled := func(key string) bool {
		f := old[key]
		return f != nil && !f.Start.IsZero() && f.Start.Before(now.AddDate(0, 0, -30)) && f.Ver == folderVer
	}
	// Without the partner lists, partners would count as sponsors: read no
	// sponsors then, and leave the folders to be read again.
	partners, perr := sites.Partners(fp.Repos)
	ver := folderVer
	if perr != nil {
		errs = append(errs, perr.Error())
		ver = 0
	}
	for _, repo := range fp.Repos {
		brand := repoBrand(repo)
		if brand == "" {
			continue
		}
		for _, prefix := range []string{"", "past/"} {
			body, err := sites.Get(sites.APIURL + "/repos/" + repo + "/contents/" + strings.TrimSuffix(prefix, "/"))
			if err != nil {
				if prefix == "" {
					errs = append(errs, repo+": "+err.Error())
				}
				for k, f := range old { // keep what we had of this repo
					if strings.HasPrefix(k, repo+"/"+prefix) && (prefix != "" || !strings.HasPrefix(k, repo+"/past/")) {
						next[k] = f
					}
				}
				continue
			}
			var entries []struct{ Name, Type string }
			if json.Unmarshal(body, &entries) != nil {
				continue
			}
			for _, en := range entries {
				if en.Type != "dir" || !eventDir.MatchString(en.Name) {
					continue
				}
				key := repo + "/" + prefix + en.Name
				if settled(key) {
					next[key] = old[key]
					continue
				}
				raw := sites.RawURL + "/" + repo + "/HEAD/" + prefix + url.PathEscape(en.Name) + "/"
				meta, err := sites.Get(raw + "metadata.yml")
				if err != nil {
					continue // a leftover folder with no metadata
				}
				f := &Folder{Key: key, Brand: brand, At: now}
				f.Name, f.Start = parseMeta(string(meta))
				if f.Name == "" {
					continue
				}
				if f.Ver = ver; ver == folderVer {
					f.Luma, f.Sponsors = sponsorsOf(string(meta), partners)
					for i, sp := range f.Sponsors {
						if sp.Logo != "" {
							f.Sponsors[i].Src = sites.RawURL + "/" + repo + "/HEAD/sponsors/" + url.PathEscape(sp.Logo)
						}
					}
				}
				data, err := sites.Get(raw + "_db/talks.csv")
				if err != nil {
					data, err = sites.Get(raw + "talks.csv")
				}
				if err == nil {
					f.Names = talkNames(data)
				}
				next[key] = f
			}
		}
	}
	if body, err := sites.Get(Conf42URL); err != nil {
		errs = append(errs, "conf42: "+err.Error())
		for k, f := range old {
			if strings.HasPrefix(k, "conf42/") {
				next[k] = f
			}
		}
	} else {
		// Conf42 names its sponsors by id; one list says who each id is.
		ids := map[string]Sponsored{}
		if data, err := sites.Get(Conf42Raw + "_db/sponsors.csv"); err == nil {
			ids = conf42Sponsors(data)
		}
		for _, ev := range parseConf42(string(body)) {
			key := "conf42/" + ev.Short
			if settled(key) {
				next[key] = old[key]
				continue
			}
			f := &Folder{Key: key, Brand: "conf42", Name: ev.Name, Start: ev.Start, At: now, Ver: folderVer}
			if len(ids) == 0 {
				f.Ver = 0 // read again when the list is back
			}
			for _, id := range ev.Paid {
				if sp, ok := ids[id]; ok && !ownDomains[sp.Domain] {
					f.Sponsors = append(f.Sponsors, sp)
				}
			}
			if ev.DB != "" {
				if data, err := sites.Get(Conf42Raw + strings.TrimPrefix(ev.DB, "./")); err == nil {
					f.Names = talkNames(data)
				}
			}
			next[key] = f
		}
	}
	fp.mu.Lock()
	defer fp.mu.Unlock()
	fp.At, fp.Err = now, strings.Join(errs, "; ")
	if len(next) == 0 {
		return // keep the last good list
	}
	fp.F = next
	if fp.path != "" {
		writeJSON(fp.path, next)
	}
}

func parseMeta(yml string) (city string, start time.Time) {
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
		}
	}
	return
}

type c42 struct {
	Name, Short, DB string
	Start           time.Time
	Paid            []string // ids of the paying sponsors
}

// paidTiers are the Conf42 sponsor tiers that are bought; media partners,
// newsletters and the like are not.
var paidTiers = map[string]bool{"diamond": true, "platinum": true, "gold": true, "silver": true, "bronze": true, "startup": true}

// parseConf42 reads name, date, short_url and db_path of each event.
func parseConf42(yml string) []c42 {
	var out []c42
	var cur *c42
	in, inSponsors, tier := false, false, ""
	flush := func() {
		if cur != nil && cur.Short != "" && !cur.Start.IsZero() {
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
			break
		}
		if strings.HasPrefix(line, "  - name:") {
			flush()
			cur = &c42{Name: sites.Unquote(strings.TrimSpace(strings.TrimPrefix(line, "  - name:")))}
			continue
		}
		if cur != nil && strings.HasPrefix(line, "     ") {
			// Deeper lines: the sponsor tiers and their ids.
			switch item := strings.TrimSpace(line); {
			case !inSponsors:
			case strings.HasPrefix(line, "      ") && !strings.HasPrefix(line, "       ") && strings.HasSuffix(item, ":"):
				tier = strings.TrimSuffix(item, ":")
			case strings.HasPrefix(item, "- ") && paidTiers[tier]:
				cur.Paid = append(cur.Paid, sites.Unquote(strings.TrimSpace(strings.TrimPrefix(item, "- "))))
			}
			continue
		}
		if cur == nil || !strings.HasPrefix(line, "    ") {
			continue
		}
		inSponsors, tier = strings.TrimSpace(line) == "sponsors:", ""
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
			cur.Start, _ = time.Parse(dayFmt, v)
		case "short_url":
			cur.Short = v
		case "db_path":
			cur.DB = v
		}
	}
	flush()
	return out
}

// talkNames reads the presenters of a lineup CSV: the brand sites' talks.csv
// (name, status; live rows only) or Conf42's (Name1, Name2).
func talkNames(data []byte) []string {
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
	var out []string
	for _, row := range rows[1:] {
		if _, ok := col["name"]; ok {
			if st := get(row, "status"); st != "" && !sites.LiveStatus(st) {
				continue
			}
			for _, n := range sites.SplitNames(get(row, "name")) {
				if n = strings.TrimSpace(n); n != "" {
					out = append(out, n)
				}
			}
			continue
		}
		for _, k := range []string{"name1", "name2"} {
			if n := get(row, k); n != "" {
				out = append(out, n)
			}
		}
	}
	return out
}

var parens = regexp.MustCompile(`\s*\([^)]*\)`)

// conf42Topic: "Site Reliability Engineering (SRE)" -> "Site Reliability Engineering".
func conf42Topic(name string) string { return strings.TrimSpace(parens.ReplaceAllString(name, "")) }

func slug(s string) string { return strings.Trim(nonLetters.ReplaceAllString(normName(s), "-"), "-") }

var themeOf = map[string]string{"sreday": "sre", "llmday": "llm", "pec": "llm", "platformday": "platform"}

// Topics: SRE, LLM, PLATFORM with every city we have ever been to or plan,
// then each Conf42 topic announced for today or later; past ones drop off.
func (fp *Footprint) Topics(now time.Time) []Topic {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	themes := []Topic{
		{ID: "sre", Name: "SRE", Brand: "sreday"},
		{ID: "llm", Name: "LLM", Brand: "llmday"},
		{ID: "platform", Name: "PLATFORM", Brand: "platformday"},
	}
	cities := map[string]map[string]*City{} // theme -> normalised city -> city
	c42 := map[string]*Topic{}
	today := now.Format(dayFmt)
	for _, f := range fp.F {
		day := ""
		if !f.Start.IsZero() {
			day = f.Start.Format(dayFmt)
		}
		if f.Brand == "conf42" {
			if day == "" || day < today { // only what is announced and still to come
				continue
			}
			name := conf42Topic(f.Name)
			if strings.Contains(strings.ToLower(name), "remix") { // not a conference we scout for
				continue
			}
			id := "conf42-" + slug(name)
			if c42[id] == nil {
				c42[id] = &Topic{ID: id, Name: name, Brand: "conf42", Online: true, Next: day}
			} else if day < c42[id].Next {
				c42[id].Next = day
			}
			continue
		}
		th := themeOf[f.Brand]
		if th == "" {
			continue
		}
		if cities[th] == nil {
			cities[th] = map[string]*City{}
		}
		k := normName(f.Name)
		c := cities[th][k]
		if c == nil {
			c = &City{Name: f.Name}
			cities[th][k] = c
		}
		c.Events++
		b := brandNames[f.Brand]
		if !contains(c.Brands, b) {
			c.Brands = append(c.Brands, b)
		}
		if day != "" && day >= today && (c.Next == "" || day < c.Next) {
			c.Next = day
		}
		if day != "" && day < today && day > c.Last {
			c.Last = day
		}
	}
	for i := range themes {
		for _, c := range cities[themes[i].ID] {
			sort.Strings(c.Brands)
			themes[i].Cities = append(themes[i].Cities, *c)
		}
		sort.Slice(themes[i].Cities, func(a, b int) bool { return themes[i].Cities[a].Name < themes[i].Cities[b].Name })
	}
	var online []Topic
	for _, t := range c42 {
		online = append(online, *t)
	}
	sort.Slice(online, func(a, b int) bool { // in timeline order
		if online[a].Next != online[b].Next {
			return online[a].Next < online[b].Next
		}
		return online[a].Name < online[b].Name
	})
	return append(themes, online...)
}

// Lineups: every name on any of our lineups, normalised, with where.
func (fp *Footprint) Lineups() map[string]string {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	out := map[string]string{}
	for _, f := range fp.F {
		for _, n := range f.Names {
			for _, k := range nameKeys(n) {
				out[k] = brandNames[f.Brand] + " " + f.Name
			}
		}
	}
	return out
}

var brandNames = map[string]string{"sreday": "SREday", "llmday": "LLMday", "platformday": "PLATFORMday", "pec": "PEC", "conf42": "Conf42"}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
