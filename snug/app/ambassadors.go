package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The Ambassadors tab: every sister brand's ambassadors, as their site shows
// them, read from the site's published /ambassadors.json (written by each
// site's home/_build/generate.py from its ambassadors list), so a new
// ambassador shows up here once their site is rebuilt.
var ambassadorSites = []struct{ Brand, URL string }{
	{"SREday", "https://sreday.com/ambassadors.json"},
	{"LLMday", "https://llmday.com/ambassadors.json"},
	{"PLATFORMday", "https://platformday.com/ambassadors.json"},
}

// Ambassador is one person on a site's list.
type Ambassador struct {
	Name     string `json:"name"`
	Job      string `json:"job"`
	Company  string `json:"company"`
	LinkedIn string `json:"linkedin"`
	Region   string `json:"region"`
}

// AmbassadorGroup is one brand's list; Error says why it is missing.
type AmbassadorGroup struct {
	Brand  string       `json:"brand"`
	People []Ambassador `json:"people"`
	Error  string       `json:"error,omitempty"`
}

const ambassadorTTL = 10 * time.Minute

type ambassadorCache struct {
	mu     sync.Mutex
	at     time.Time
	groups []AmbassadorGroup
}

var ambassadors ambassadorCache

var ambassadorClient = &http.Client{Timeout: 8 * time.Second}

func fetchAmbassadors(url string) ([]Ambassador, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "badger-snug")
	resp, err := ambassadorClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the site answered %d", resp.StatusCode)
	}
	var list []Ambassador
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	out := list[:0]
	for _, a := range list {
		a.Name = strings.TrimSpace(a.Name)
		if a.Name == "" {
			continue
		}
		// Only a LinkedIn link goes on a badge (the QR), never anything
		// else typed into that field.
		if !strings.Contains(strings.ToLower(a.LinkedIn), "linkedin.com/") {
			a.LinkedIn = ""
		}
		out = append(out, a)
	}
	return out, nil
}

// get is the lists, fetched again after ambassadorTTL (or with fresh); a
// site that can't be reached keeps the list it had last time.
func (c *ambassadorCache) get(fresh bool) []AmbassadorGroup {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !fresh && c.groups != nil && time.Since(c.at) < ambassadorTTL {
		return c.groups
	}
	groups := make([]AmbassadorGroup, len(ambassadorSites))
	var wg sync.WaitGroup
	for i, site := range ambassadorSites {
		wg.Add(1)
		go func() {
			defer wg.Done()
			people, err := fetchAmbassadors(site.URL)
			groups[i] = AmbassadorGroup{Brand: site.Brand, People: people}
			if err != nil {
				groups[i].Error = err.Error()
				for _, old := range c.groups {
					if old.Brand == site.Brand {
						groups[i].People = old.People
					}
				}
			}
		}()
	}
	wg.Wait()
	c.groups, c.at = groups, time.Now()
	return groups
}

// handleAmbassadors is the Ambassadors tab's data (?fresh=1: no cache).
func (s *Server) handleAmbassadors(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, ambassadors.get(r.URL.Query().Get("fresh") != ""))
}
