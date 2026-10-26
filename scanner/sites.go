package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The conference websites list each event's sponsors in
// <event folder>/metadata.yml, next to the event's Luma id:
//
//	luma_evt: evt-qQfoSkUnT6B5o11
//	sponsors:
//	  - logo: harness.png
//	    url: https://harness.io/
//	    name: Harness          # optional; otherwise taken from the logo
//
// Snout reads those files from GitHub so every listed sponsor gets a
// sponsor link without typing.

var defaultSiteRepos = []string{
	"sreday/sreday",
	"sreday/llmday",
	"sreday/platformday",
	"Prompt-Engineering-Conference/2026.promptengineering.rocks",
}

var (
	githubAPIURL = "https://api.github.com"
	githubRawURL = "https://raw.githubusercontent.com"
)

// SiteSponsor is one sponsor listed on an event page.
type SiteSponsor struct {
	Name string
	URL  string
}

var eventDirRe = regexp.MustCompile(`^20\d\d-`)

// ListSiteSponsors returns event id -> sponsors listed on its pages, across
// all repos. A sponsor listed on two sites for one event (LLMday + PEC
// London) appears once.
func ListSiteSponsors(repos []string) (map[string][]SiteSponsor, error) {
	type page struct {
		eventID  string
		sponsors []SiteSponsor
	}
	var (
		mu    sync.Mutex
		pages []page
		errs  []string
		wg    sync.WaitGroup
	)
	for _, repo := range repos {
		dirs, err := listEventDirs(repo)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", repo, err))
			continue
		}
		for _, dir := range dirs {
			wg.Add(1)
			go func(repo, dir string) {
				defer wg.Done()
				body, err := httpGet(githubRawURL + "/" + repo + "/HEAD/" + dir + "/metadata.yml")
				if err != nil {
					mu.Lock()
					errs = append(errs, fmt.Sprintf("%s/%s: %v", repo, dir, err))
					mu.Unlock()
					return
				}
				id, sponsors := parseMetadata(string(body))
				if id == "" {
					return
				}
				mu.Lock()
				pages = append(pages, page{id, sponsors})
				mu.Unlock()
			}(repo, dir)
		}
	}
	wg.Wait()

	out := make(map[string][]SiteSponsor)
	for _, p := range pages {
	next:
		for _, sp := range p.sponsors {
			for _, have := range out[p.eventID] {
				if strings.EqualFold(have.Name, sp.Name) {
					continue next
				}
			}
			out[p.eventID] = append(out[p.eventID], sp)
		}
	}
	if len(errs) > 0 {
		return out, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return out, nil
}

// listEventDirs returns the repo's top-level event folders (2026-amsterdam-q4).
// Finished events live under past/ and are skipped.
func listEventDirs(repo string) ([]string, error) {
	body, err := httpGet(githubAPIURL + "/repos/" + repo + "/contents/")
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
			dirs = append(dirs, e.Name)
		}
	}
	return dirs, nil
}

func httpGet(url string) ([]byte, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// parseMetadata pulls luma_evt and the sponsors list out of metadata.yml.
// It reads only those two keys, so the rest of the file can be anything.
func parseMetadata(yml string) (eventID string, sponsors []SiteSponsor) {
	inSponsors := false
	var cur map[string]string
	flush := func() {
		if cur == nil {
			return
		}
		name := cur["name"]
		if name == "" {
			name = nameFromLogo(cur["logo"])
		}
		if name != "" {
			sponsors = append(sponsors, SiteSponsor{Name: name, URL: cur["url"]})
		}
		cur = nil
	}
	for _, line := range strings.Split(yml, "\n") {
		line = strings.TrimRight(stripComment(line), " \t\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if line[0] != ' ' && line[0] != '\t' && line[0] != '-' {
			flush()
			key, val, _ := strings.Cut(line, ":")
			inSponsors = key == "sponsors"
			if key == "luma_evt" {
				eventID = unquote(val)
			}
			continue
		}
		if !inSponsors {
			continue
		}
		item := strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(item, "- "); ok {
			flush()
			cur = map[string]string{}
			item = rest
		}
		if cur == nil {
			continue
		}
		if key, val, ok := strings.Cut(item, ":"); ok {
			cur[strings.TrimSpace(key)] = unquote(val)
		}
	}
	flush()
	return eventID, sponsors
}

func stripComment(line string) string {
	// "# " only counts as a comment at the start or after a space, so URLs
	// with fragments survive.
	if i := strings.Index(line, " #"); i >= 0 {
		return line[:i]
	}
	if strings.HasPrefix(strings.TrimSpace(line), "#") {
		return ""
	}
	return line
}

func unquote(v string) string {
	return strings.Trim(strings.TrimSpace(v), `"'`)
}

// nameFromLogo turns "jug-amsterdam.png" into "JUG Amsterdam". Short words
// are usually acronyms (ING, GDG, PEC); a name: field overrides this.
func nameFromLogo(logo string) string {
	stem := strings.TrimSuffix(path.Base(logo), path.Ext(logo))
	var words []string
	for _, w := range strings.FieldsFunc(stem, func(r rune) bool { return r == '-' || r == '_' || r == '.' }) {
		switch strings.ToLower(w) {
		case "full", "black", "white", "dark", "light", "logo":
			// logo variants, not part of the name
		default:
			words = append(words, w)
		}
	}
	for i, w := range words {
		if len(w) <= 3 {
			words[i] = strings.ToUpper(w)
		} else {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

// siteRepos returns SITE_REPOS (comma-separated owner/repo) or the defaults.
func siteRepos() []string {
	if v := os.Getenv("SITE_REPOS"); v != "" {
		var repos []string
		for _, r := range strings.Split(v, ",") {
			if r = strings.TrimSpace(r); r != "" {
				repos = append(repos, r)
			}
		}
		return repos
	}
	return defaultSiteRepos
}
