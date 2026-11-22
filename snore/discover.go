package snore

import (
	"strconv"
	"strings"
	"time"
)

// discover adds the recipe's fixed sources and the events the open
// conference directories list whose address matches one of the recipe's
// HOST rules. New sources are read on the same run. Called without the lock.
const discoverMax = 120 // new sources a day from the directories

func (s *Server) discover() {
	r := s.recipe()
	now := s.now()
	s.mu.Lock()
	added := 0
	for _, f := range r.fixed {
		if s.findSource(f.Kind, f.ID) == nil {
			src := f
			src.Added = now
			s.sources = append(s.sources, &src)
			added++
		}
	}
	if added > 0 {
		s.note("feed", "%d fixed speaker source(s) added from the recipe", added)
	}
	s.mu.Unlock()
	if len(r.hosts) == 0 {
		return
	}
	found := 0
	for _, d := range r.dirs {
		body, err := s.get(d.URL, nil, true)
		if err != nil {
			continue
		}
		doc, err := getJSON(body)
		if err != nil {
			continue
		}
		items := walk(doc, d.Fields["items"])
		if d.Fields["items"] == "" {
			items = walk(doc, "*")
		}
		for _, it := range items {
			if found >= discoverMax {
				break
			}
			link := first(it, d.Fields["link"])
			kind, id := matchHost(r.hosts, link)
			if kind == "" {
				continue
			}
			day := dirDate(walk(it, d.Fields["date"]))
			if day == "" || !recentOrComing(day, now) {
				continue
			}
			s.mu.Lock()
			known := s.findSource(kind, id) != nil
			s.mu.Unlock()
			if known {
				continue
			}
			name := first(it, d.Fields["name"])
			if !hasYear.MatchString(name) {
				name += " " + day[:4]
			}
			if !realConference(name) {
				continue
			}
			src := &Source{Kind: kind, ID: id, Event: clip(name, 100), City: clip(first(it, d.Fields["city"]), 60),
				Country: clip(first(it, d.Fields["country"]), 60), Date: day, Added: now}
			if strings.HasPrefix(link, "https://") {
				src.URL = clip(link, 300)
			}
			if kind == kindList {
				src.Kind = ""
			}
			found++
			if err := s.readSource(src, now); err != nil {
				src.Err = clip(err.Error(), 120)
			}
			s.mu.Lock()
			s.sources = append(s.sources, src)
			s.note("feed", "New speaker source: %s (%s, %d speakers)", src.Event, kind, src.Speakers)
			s.saveSources()
			s.mu.Unlock()
		}
	}
}

func matchHost(rules []hostRule, link string) (string, string) {
	for _, h := range rules {
		if m := h.re.FindStringSubmatchIndex(link); m != nil {
			id := string(h.re.ExpandString(nil, h.id, link, m))
			if validID(h.kind, id) {
				return h.kind, id
			}
		}
	}
	return "", ""
}

// dirDate: a date as directories give it: "YYYY-MM-DD...", or milliseconds
// since 1970 (alone or the first of a list).
func dirDate(vals []any) string {
	for _, v := range vals {
		switch x := v.(type) {
		case string:
			if len(x) >= 10 {
				if _, err := time.Parse(dayFmt, x[:10]); err == nil {
					return x[:10]
				}
			}
			if n, err := strconv.ParseInt(x, 10, 64); err == nil && n > 1e11 {
				return time.UnixMilli(n).UTC().Format(dayFmt)
			}
		case float64:
			if x > 1e11 {
				return time.UnixMilli(int64(x)).UTC().Format(dayFmt)
			}
		case []any:
			if d := dirDate(x); d != "" {
				return d
			}
		}
	}
	return ""
}

// recentOrComing: the last two years or the coming one.
func recentOrComing(day string, now time.Time) bool {
	d, err := time.Parse(dayFmt, day)
	return err == nil && d.After(now.AddDate(-2, 0, 0)) && d.Before(now.AddDate(1, 0, 0))
}
