package snore

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Public speaker sources come in a few shapes. Which addresses, fields and
// patterns go with each shape is in the private recipe (data dir), not here:
//
//	FEED <kind> <address with %s or {id}, {key}, {q}, {repo}, {path}>
//	FEED <address>                 the "list" kind
//	PATTERN <kind> <regex with named groups name, company, title>
//	PATH <name> <dotted path, * for every element>
//	MAP <field> <key,key,...>      field names in open data files
//	LINK <kind> <address with {id}>
//	DIRECTORY <address> name=<path> link=<path> date=<path> city=<path> country=<path>
//	HOST <regex> <kind> <id with $1>
//	SOURCE <kind> <id> <YYYY-MM-DD or -> <city or -> <event name>

// Kinds of source.
const (
	kindList      = "list"          // a JSON list of speakers with sessions and links
	kindProfiles  = "profiles"      // a JSON list of speaker profiles
	kindSchedJSON = "schedule-json" // a published schedule export (JSON)
	kindSchedXML  = "schedule-xml"  // a published schedule export (XML)
	kindProgram   = "program-html"  // a conference program page
	kindVideo     = "video"         // a channel of talk videos (an API with a key)
	kindRepo      = "repo-data"     // open-licensed speaker data files (an API with a token)
)

var kinds = map[string]bool{kindList: true, kindProfiles: true, kindSchedJSON: true, kindSchedXML: true, kindProgram: true, kindVideo: true, kindRepo: true}

// videoKeep: what a video API returns is kept 30 days unless read again.
const videoKeep = 30 * 24 * time.Hour

type dirSpec struct {
	URL    string
	Fields map[string]string
}

type hostRule struct {
	re   *regexp.Regexp
	kind string
	id   string
}

type recipe struct {
	feeds    map[string]string
	patterns map[string]*regexp.Regexp
	paths    map[string]string
	maps     map[string][]string
	links    map[string]string
	dirs     []dirSpec
	hosts    []hostRule
	fixed    []Source
}

func parseRecipe(text string) recipe {
	r := recipe{feeds: map[string]string{}, patterns: map[string]*regexp.Regexp{}, paths: map[string]string{}, maps: map[string][]string{}, links: map[string]string{}}
	for _, l := range strings.Split(text, "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "FEED":
			if len(f) == 2 {
				r.feeds[kindList] = f[1]
			} else if kinds[f[1]] || strings.Contains(f[1], "-") {
				r.feeds[f[1]] = f[2]
			}
		case "PATTERN":
			if len(f) >= 3 {
				if re, err := regexp.Compile(strings.TrimSpace(strings.SplitN(strings.TrimSpace(l), " ", 3)[2])); err == nil {
					r.patterns[f[1]] = re
				}
			}
		case "PATH":
			if len(f) == 3 {
				r.paths[f[1]] = f[2]
			}
		case "MAP":
			if len(f) == 3 {
				r.maps[f[1]] = strings.Split(f[2], ",")
			}
		case "LINK":
			if len(f) == 3 {
				r.links[f[1]] = f[2]
			}
		case "DIRECTORY":
			d := dirSpec{URL: f[1], Fields: map[string]string{}}
			for _, kv := range f[2:] {
				if k, v, ok := strings.Cut(kv, "="); ok {
					d.Fields[k] = v
				}
			}
			r.dirs = append(r.dirs, d)
		case "HOST":
			if len(f) == 4 && kinds[f[2]] {
				if re, err := regexp.Compile(f[1]); err == nil {
					r.hosts = append(r.hosts, hostRule{re, f[2], f[3]})
				}
			}
		case "SOURCE":
			if len(f) >= 6 && kinds[f[1]] {
				src := Source{Kind: f[1], ID: f[2], Event: strings.Join(f[5:], " ")}
				if f[3] != "-" {
					src.Date = f[3]
				}
				if f[4] != "-" {
					src.City = strings.ReplaceAll(f[4], "_", " ")
				}
				r.fixed = append(r.fixed, src)
			}
		}
	}
	return r
}

func (s *Server) recipe() recipe {
	b, _ := os.ReadFile(filepath.Join(s.dir, "recipe.txt"))
	return parseRecipe(string(b))
}

// fill puts values into an address template.
func (s *Server) fill(tmpl string, vals map[string]string) string {
	out := tmpl
	if id, ok := vals["id"]; ok && strings.Contains(out, "%s") {
		out = strings.Replace(out, "%s", id, 1)
	}
	vals["key"] = s.cfg.VideoKey
	for k, v := range vals {
		if k == "q" {
			v = url.QueryEscape(v)
		}
		out = strings.ReplaceAll(out, "{"+k+"}", v)
	}
	return out
}

// walk follows a dotted path; "*" takes every element of a list (or map).
func walk(v any, path string) []any {
	cur := []any{v}
	if path == "" || path == "." {
		return cur
	}
	for _, step := range strings.Split(path, ".") {
		var next []any
		for _, c := range cur {
			switch x := c.(type) {
			case map[string]any:
				if step == "*" {
					for _, e := range x {
						next = append(next, e)
					}
				} else if e, ok := x[step]; ok {
					next = append(next, e)
				}
			case []any:
				if step == "*" {
					next = append(next, x...)
				}
			}
		}
		cur = next
	}
	return cur
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		return fmt.Sprintf("%.0f", x)
	case int:
		return fmt.Sprint(x)
	}
	return ""
}

func first(v any, path string) string {
	for _, x := range walk(v, path) {
		if s := str(x); s != "" {
			return s
		}
	}
	return ""
}

// pick: the first non-empty of several keys of a record.
func pick(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := str(m[k]); s != "" {
			return s
		}
	}
	return ""
}

// records: the list of objects in a document: the document itself, the
// first list in it, or a map of objects keyed by id.
func records(v any) []map[string]any {
	var out []map[string]any
	add := func(list []any) {
		for _, e := range list {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
	}
	switch x := v.(type) {
	case []any:
		add(x)
	case map[string]any:
		for _, e := range x {
			if l, ok := e.([]any); ok && len(l) > 0 {
				if _, isObj := l[0].(map[string]any); isObj {
					add(l)
					return out
				}
			}
		}
		for _, e := range x {
			if m, ok := e.(map[string]any); ok && pick(m, "name", "fullName", "full_name") != "" {
				out = append(out, m)
			}
		}
	}
	return out
}

func linkedInFrom(v string) string {
	v = strings.TrimSpace(v)
	switch {
	case v == "":
		return ""
	case strings.Contains(v, "linkedin.com/"):
		return normalizeLinkedIn(v)
	default:
		return "https://www.linkedin.com/in/" + strings.Trim(v, "/@ ") + "/"
	}
}

func talksFrom(m map[string]any) []Talk {
	var out []Talk
	for _, k := range []string{"sessions", "talks", "proposals", "submissions"} {
		for _, t := range walk(m[k], "*") {
			tm, ok := t.(map[string]any)
			title := str(t)
			if ok {
				title = pick(tm, "title", "name")
			}
			if title != "" {
				out = append(out, Talk{Title: clip(title, 140)})
			}
		}
	}
	return out
}

// parseProfiles: a JSON list of speaker profiles.
func parseProfiles(body []byte) ([]PoolSpeaker, error) {
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("not a speaker list")
	}
	var out []PoolSpeaker
	for _, m := range records(doc) {
		name := pick(m, "fullName", "name")
		if name == "" {
			name = strings.TrimSpace(pick(m, "firstName") + " " + pick(m, "lastName"))
		}
		if name == "" {
			continue
		}
		p := PoolSpeaker{Name: name, Company: pick(m, "company", "companyName", "organization"), Bio: clip(pick(m, "bio", "biography"), 500),
			Tagline: clip(pick(m, "title", "jobTitle", "tagLine", "tagline"), 160), LinkedIn: linkedInFrom(pick(m, "linkedInUsername", "linkedin", "linkedIn", "linkedInUrl")),
			Talks: talksFrom(m)}
		if img := pick(m, "imageUrl", "image", "avatar", "photoUrl", "profilePicture"); strings.HasPrefix(img, "https://") {
			p.Photo = img
		}
		if p.Tagline == "" && p.Company != "" {
			p.Tagline = p.Company
		}
		for _, k := range []string{"blog", "website", "homepage"} {
			if u := pick(m, k); strings.HasPrefix(u, "http") {
				p.Links = append(p.Links, u)
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// parseSchedJSON: every object with "persons" is a talk.
func parseSchedJSON(body []byte) ([]PoolSpeaker, error) {
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("not a schedule")
	}
	by := map[string]*PoolSpeaker{}
	var order []string
	var visit func(v any)
	visit = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if ps, ok := x["persons"].([]any); ok {
				title := pick(x, "title", "name")
				for _, p := range ps {
					pm, _ := p.(map[string]any)
					name := str(p)
					if pm != nil {
						name = pick(pm, "public_name", "full_public_name", "name")
					}
					k := normName(name)
					if k == "" {
						continue
					}
					sp := by[k]
					if sp == nil {
						sp = &PoolSpeaker{Name: name}
						by[k] = sp
						order = append(order, k)
					}
					if pm != nil && sp.Bio == "" {
						sp.Bio = clip(pick(pm, "biography", "abstract"), 500)
					}
					if title != "" {
						sp.Talks = append(sp.Talks, Talk{Title: clip(title, 140)})
					}
				}
				return
			}
			for _, e := range x {
				visit(e)
			}
		case []any:
			for _, e := range x {
				visit(e)
			}
		}
	}
	visit(doc)
	var out []PoolSpeaker
	for _, k := range order {
		out = append(out, *by[k])
	}
	return out, nil
}

// parseSchedXML: <event><title/><persons><person/></persons></event>.
func parseSchedXML(body []byte) ([]PoolSpeaker, error) {
	type ev struct {
		Title   string   `xml:"title"`
		Persons []string `xml:"persons>person"`
	}
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.Strict = false
	by := map[string]*PoolSpeaker{}
	var order []string
	seen := false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "event" {
			continue
		}
		var e ev
		if dec.DecodeElement(&e, &se) != nil {
			continue
		}
		seen = true
		for _, name := range e.Persons {
			name = strings.TrimSpace(name)
			k := normName(name)
			if k == "" {
				continue
			}
			if by[k] == nil {
				by[k] = &PoolSpeaker{Name: name}
				order = append(order, k)
			}
			if t := strings.TrimSpace(e.Title); t != "" {
				by[k].Talks = append(by[k].Talks, Talk{Title: clip(t, 140)})
			}
		}
	}
	if !seen {
		return nil, fmt.Errorf("not a schedule")
	}
	var out []PoolSpeaker
	for _, k := range order {
		out = append(out, *by[k])
	}
	return out, nil
}

var tags = regexp.MustCompile(`<[^>]*>`)

func plain(s string) string {
	return strings.Join(strings.Fields(html.UnescapeString(tags.ReplaceAllString(s, " "))), " ")
}

// parsePattern: speakers out of a page or a text with the recipe's regex.
func parsePattern(text string, re *regexp.Regexp) []PoolSpeaker {
	if re == nil {
		return nil
	}
	by := map[string]*PoolSpeaker{}
	var order []string
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		get := func(g string) string {
			if i := re.SubexpIndex(g); i > 0 && i < len(m) {
				return plain(m[i])
			}
			return ""
		}
		for _, name := range splitNames(get("name")) {
			k := normName(name)
			if k == "" || len(strings.Fields(name)) < 2 || len(name) > 60 {
				continue
			}
			if by[k] == nil {
				by[k] = &PoolSpeaker{Name: name, Company: clip(get("company"), 100)}
				by[k].Tagline = by[k].Company
				order = append(order, k)
			}
			if t := get("title"); t != "" {
				by[k].Talks = append(by[k].Talks, Talk{Title: clip(t, 140)})
			}
		}
	}
	var out []PoolSpeaker
	for _, k := range order {
		out = append(out, *by[k])
	}
	return out
}

// splitNames: "A and B", "A, B & C".
func splitNames(s string) []string {
	s = strings.NewReplacer(" and ", ",", " & ", ",", ";", ",").Replace(s)
	var out []string
	for _, n := range strings.Split(s, ",") {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// readKind fetches and parses one source by its kind.
func (s *Server) readKind(src *Source, r recipe) ([]PoolSpeaker, error) {
	tmpl := r.feeds[src.Kind]
	if tmpl == "" {
		return nil, fmt.Errorf("no FEED line for %s in the recipe", src.Kind)
	}
	switch src.Kind {
	case kindProfiles, kindSchedJSON, kindSchedXML, kindProgram:
		body, err := s.get(s.fill(tmpl, map[string]string{"id": src.ID}), nil, true)
		if err != nil {
			return nil, err
		}
		switch src.Kind {
		case kindProfiles:
			return parseProfiles(body)
		case kindSchedJSON:
			return parseSchedJSON(body)
		case kindSchedXML:
			return parseSchedXML(body)
		}
		if r.patternFor(src) == nil {
			return nil, fmt.Errorf("no PATTERN for %s in the recipe", src.Kind)
		}
		return parsePattern(string(body), r.patternFor(src)), nil
	case kindVideo:
		return s.readVideo(src, r)
	case kindRepo:
		return s.readRepo(src, r)
	}
	return nil, fmt.Errorf("unknown kind %q", src.Kind)
}

func getJSON(body []byte) (any, error) {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("not JSON")
	}
	return v, nil
}

// readVideo: a channel's latest talk videos; names out of the titles and
// descriptions with the recipe pattern.
func (s *Server) readVideo(src *Source, r recipe) ([]PoolSpeaker, error) {
	if s.cfg.VideoKey == "" {
		return nil, fmt.Errorf("no video key set (SNORE_VIDEO_KEY)")
	}
	if r.patternFor(src) == nil || r.feeds["video-items"] == "" {
		return nil, fmt.Errorf("the recipe lacks the video lines")
	}
	body, err := s.get(s.fill(r.feeds[kindVideo], map[string]string{"id": src.ID}), nil, false)
	if err != nil {
		return nil, err
	}
	doc, err := getJSON(body)
	if err != nil {
		return nil, err
	}
	list := first(doc, r.paths["video-list"])
	if list == "" {
		return nil, fmt.Errorf("no video list for this channel")
	}
	body, err = s.get(s.fill(r.feeds["video-items"], map[string]string{"id": list}), nil, false)
	if err != nil {
		return nil, err
	}
	if doc, err = getJSON(body); err != nil {
		return nil, err
	}
	var out []PoolSpeaker
	for _, it := range walk(doc, r.paths["video-items"]) {
		title, desc := first(it, r.paths["video-title"]), first(it, r.paths["video-text"])
		date, id := first(it, r.paths["video-date"]), first(it, r.paths["video-id"])
		for _, p := range parsePattern(title+"\n"+desc, r.patternFor(src)) {
			t := Talk{Title: clip(title, 140)}
			if len(date) >= 10 {
				t.Date = date[:10]
			}
			if id != "" && r.links[kindVideo] != "" {
				t.URL = strings.ReplaceAll(r.links[kindVideo], "{id}", id)
			}
			p.Talks = []Talk{t}
			out = append(out, p)
		}
	}
	return out, nil
}

var openLicenses = map[string]bool{"MIT": true, "Apache-2.0": true, "BSD-2-Clause": true, "BSD-3-Clause": true, "ISC": true,
	"CC0-1.0": true, "CC-BY-4.0": true, "CC-BY-SA-4.0": true, "Unlicense": true, "0BSD": true}

// readRepo: speaker data files in public repositories that carry an open
// license; anything else is skipped.
func (s *Server) readRepo(src *Source, r recipe) ([]PoolSpeaker, error) {
	if s.cfg.RepoToken == "" {
		return nil, fmt.Errorf("no repo token set (SNORE_REPO_TOKEN)")
	}
	for _, need := range []string{"repo-license", "repo-raw"} {
		if r.feeds[need] == "" {
			return nil, fmt.Errorf("the recipe lacks %s", need)
		}
	}
	hdr := http.Header{"Authorization": {"Bearer " + s.cfg.RepoToken}}
	body, err := s.get(s.fill(r.feeds[kindRepo], map[string]string{"q": src.ID}), hdr, false)
	if err != nil {
		return nil, err
	}
	doc, err := getJSON(body)
	if err != nil {
		return nil, err
	}
	fields := func(f string, def ...string) []string {
		if m := r.maps[f]; len(m) > 0 {
			return m
		}
		return def
	}
	licensed := map[string]bool{}
	var out []PoolSpeaker
	n := 0
	for _, it := range walk(doc, r.paths["repo-items"]) {
		repo, path := first(it, r.paths["repo-name"]), first(it, r.paths["repo-path"])
		if repo == "" || path == "" || n >= 30 {
			continue
		}
		n++
		ok, known := licensed[repo]
		if !known {
			lb, err := s.get(s.fill(r.feeds["repo-license"], map[string]string{"repo": repo}), hdr, false)
			if err == nil {
				if ld, err := getJSON(lb); err == nil {
					ok = openLicenses[first(ld, r.paths["repo-license-id"])]
				}
			}
			licensed[repo] = ok
		}
		if !ok {
			continue
		}
		raw, err := s.get(s.fill(r.feeds["repo-raw"], map[string]string{"repo": repo, "path": path}), nil, true)
		if err != nil {
			continue
		}
		var data any
		if json.Unmarshal(raw, &data) != nil {
			var y any
			if yaml.Unmarshal(raw, &y) != nil {
				continue
			}
			data = plainYAML(y)
		}
		for _, m := range records(data) {
			name := pick(m, fields("name", "name", "fullName", "full_name", "fullname")...)
			if len(strings.Fields(name)) < 2 {
				continue
			}
			p := PoolSpeaker{Name: name, Company: clip(pick(m, fields("company", "company", "organization", "organisation", "affiliation", "employer")...), 100),
				Tagline:  clip(pick(m, fields("title", "title", "job_title", "jobTitle", "position", "role")...), 160),
				Bio:      clip(pick(m, fields("bio", "bio", "biography", "about")...), 500),
				LinkedIn: linkedInFrom(pick(m, fields("linkedin", "linkedin", "linkedIn", "linkedin_url")...)), Talks: talksFrom(m)}
			if img := pick(m, fields("photo", "photo", "image", "avatar", "picture", "headshot", "imageUrl")...); strings.HasPrefix(img, "https://") {
				p.Photo = img
			}
			out = append(out, p)
		}
	}
	return out, nil
}

// plainYAML turns YAML maps into JSON-like ones.
func plainYAML(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = plainYAML(e)
		}
		return x
	case map[any]any:
		m := map[string]any{}
		for k, e := range x {
			m[fmt.Sprint(k)] = plainYAML(e)
		}
		return m
	case []any:
		for i, e := range x {
			x[i] = plainYAML(e)
		}
		return x
	case int:
		return float64(x)
	}
	return v
}

// patternFor: the recipe's pattern for this very source, else the kind's.
func (r recipe) patternFor(src *Source) *regexp.Regexp {
	if re := r.patterns[src.kind()+":"+src.ID]; re != nil {
		return re
	}
	return r.patterns[src.kind()]
}
