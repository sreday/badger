package sites

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
)

// EventDir is an event's folder in a site repo, with its metadata.yml.
type EventDir struct {
	Repo, Dir string // sreday/sreday, 2026-amsterdam-q4 (or past/...)
	Metadata  string
}

// Raw is the raw GitHub URL of a file in the event folder.
func (d EventDir) Raw(file string) string {
	return RawURL + "/" + d.Repo + "/HEAD/" + d.Dir + "/" + file
}

// eventDirCache remembers where events' folders are, for an hour. Lookups take turns,
// so 40 phones opening the same page cost one lookup, not 40.
var (
	eventDirMu    sync.Mutex
	eventDirCache = map[string]eventDirHit{}
)

type eventDirHit struct {
	dir *EventDir
	at  time.Time
}

// FindEventDir finds the event's folder (top level or past/) by its
// luma_evt: the folder named like the event first, straight from raw files
// (GitHub's API allows only 60 calls an hour without a token), then, by
// listing folders through the API, the rest of that year and quarter, so a
// renamed city still turns up.
func FindEventDir(repos []string, eventID, eventName string) (*EventDir, error) {
	key := RawURL + "|" + strings.Join(repos, ",") + "|" + eventID
	eventDirMu.Lock()
	defer eventDirMu.Unlock()
	if h, ok := eventDirCache[key]; ok && time.Since(h.at) < time.Hour {
		if body, err := Get(h.dir.Raw("metadata.yml")); err == nil { // the folder is remembered, never its contents
			d := *h.dir
			d.Metadata = string(body)
			return &d, nil
		}
	}
	dir, err := lookupEventDir(repos, eventID, eventName)
	if err == nil {
		eventDirCache[key] = eventDirHit{dir, time.Now()}
	}
	return dir, err
}

func lookupEventDir(repos []string, eventID, eventName string) (*EventDir, error) {
	want := folderGuess(eventName)
	if want != "" {
		for _, repo := range repos {
			for _, d := range []string{want, "past/" + want} {
				body, err := Get(RawURL + "/" + repo + "/HEAD/" + d + "/metadata.yml")
				if err != nil {
					continue
				}
				if id, _ := ParseMetadata(string(body)); id == eventID {
					return &EventDir{Repo: repo, Dir: d, Metadata: string(body)}, nil
				}
			}
		}
	}
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
			body, err := Get(RawURL + "/" + repo + "/HEAD/" + d + "/metadata.yml")
			if err != nil {
				continue
			}
			if id, _ := ParseMetadata(string(body)); id != eventID {
				continue
			}
			return &EventDir{Repo: repo, Dir: d, Metadata: string(body)}, nil
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
	body, err := Get(APIURL + "/repos/" + repo + "/contents/" + base)
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

// Partners reads every repo's partners.yaml (lowercased logo names).
func Partners(repos []string) (map[string]bool, error) {
	partners := make(map[string]bool)
	for _, repo := range repos {
		body, err := Get(RawURL + "/" + repo + "/HEAD/partners.yaml")
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

// LiveStatus is the sites' own rule for a talk on the page: talk, keynote,
// workshop or the old "confirmed", never a draft. A noshow row is not live.
var (
	draftRe = regexp.MustCompile(`\bdraft\b`)
	liveRe  = regexp.MustCompile(`keynote|confirmed|\b(talk|workshop)\b`)
	splitRe = regexp.MustCompile(`\s*&\s*|\s+and\s+`)
)

func LiveStatus(s string) bool {
	s = strings.ToLower(s)
	return !draftRe.MatchString(s) && liveRe.MatchString(s)
}

// SplitNames splits a talks.csv name ("Ada L & Grace H", "A and B") into
// its presenters.
func SplitNames(name string) []string { return splitRe.Split(name, -1) }

var nonWord = regexp.MustCompile(`[^\p{L}\p{N}]+`)
