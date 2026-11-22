package snore

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The speakers of our events, from the organizer's own lineup sheets. A
// small keyed feed hands them over (the address is in SNORE_SPEAKER_FEED);
// each row is one speaker on one event tab.

type sheetSpeaker struct {
	Book     string `json:"book"` // the sheet: one per brand
	Tab      string `json:"tab"`  // the event
	Name     string `json:"name"`
	Email    string `json:"email"`
	Title    string `json:"title"`
	Org      string `json:"org"`
	LinkedIn string `json:"linkedin"`
	Status   string `json:"status"`
}

// feedMax: the most a feed answer may weigh.
const feedMax = 32 << 20

// readSpeakerFeed fetches the feed. The address holds a key: errors never
// repeat it.
func readSpeakerFeed(addr string) ([]sheetSpeaker, error) {
	client := &http.Client{Timeout: 6 * time.Minute}
	resp, err := client.Get(addr)
	if err != nil {
		return nil, fmt.Errorf("the feed did not answer")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the feed answered HTTP %d", resp.StatusCode)
	}
	var out struct {
		Error string         `json:"error"`
		Rows  []sheetSpeaker `json:"rows"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, feedMax)).Decode(&out); err != nil {
		return nil, fmt.Errorf("the feed did not answer with a list")
	}
	if out.Error != "" {
		return nil, fmt.Errorf("the feed said: %s", out.Error)
	}
	return out.Rows, nil
}

var (
	tabYear    = regexp.MustCompile(`(^|[^0-9])(20[0-9][0-9])([^0-9]|$)`)
	tabQuarter = regexp.MustCompile(`(?i)(^|[^a-z0-9])q([1-4])([^0-9]|$)`)
	tabMonths  = []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}
	tabWord    = regexp.MustCompile(`[a-z]+`)
	// notOnStage: a row that never became a talk.
	notOnStage = regexp.MustCompile(`(?i)draft|reject|declin|waitlist|backup|maybe`)
)

// tabDate reads when an event was from its tab name: "City 2026 Q3",
// "2024_city", "meetup-city-2024-nov", "TOPIC_2025". The middle of the
// quarter or month, or of the year when the name says no more. Zero
// when there is no year.
func tabDate(tab string) time.Time {
	m := tabYear.FindStringSubmatch(tab)
	if m == nil {
		return time.Time{}
	}
	year, _ := strconv.Atoi(m[2])
	if q := tabQuarter.FindStringSubmatch(tab); q != nil {
		n, _ := strconv.Atoi(q[2])
		return time.Date(year, time.Month(n*3-1), 15, 0, 0, 0, 0, time.UTC)
	}
	for _, w := range tabWord.FindAllString(strings.ToLower(tab), -1) {
		for i, mon := range tabMonths {
			if w == mon {
				return time.Date(year, time.Month(i+1), 15, 0, 0, 0, 0, time.UTC)
			}
		}
	}
	return time.Date(year, time.July, 1, 0, 0, 0, 0, time.UTC)
}

var profileSlug = regexp.MustCompile(`(?i)linkedin\.com/in/([^/?#\s]+)`)

// profileURL folds what people type into one form of a profile address, so
// the same person from two sources is one person. "" when it is none.
func profileURL(raw string) string {
	m := profileSlug.FindStringSubmatch(raw)
	if m == nil {
		return ""
	}
	return "https://www.linkedin.com/in/" + strings.ToLower(strings.TrimSuffix(m[1], "/")) + "/"
}
