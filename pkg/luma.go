package pkg

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

const lumaBaseURL = "https://api.lu.ma"

// LumaEvent holds the event data nested inside the API response entry.
type LumaEvent struct {
	APIId   string `json:"api_id"`
	Name    string `json:"name"`
	StartAt string `json:"start_at"`
	EndAt   string `json:"end_at"`
	URL     string `json:"url"`
}

// LumaEventEntry wraps the event object as returned by the list-events API.
type LumaEventEntry struct {
	Event LumaEvent `json:"event"`
}

// BadgeData holds the information needed to generate a badge.
type BadgeData struct {
	Name     string
	Email    string
	Title    string
	Company  string
	LinkedIn string
	Flagged  bool
}

// lumaGetAll fetches all pages from a paginated Lu.ma API endpoint.
func lumaGetAll(path, apiKey string) ([]json.RawMessage, error) {
	var all []json.RawMessage
	cursor := ""

	for {
		url := lumaBaseURL + "/" + path
		if cursor != "" {
			if strings.Contains(url, "?") {
				url += "&pagination_cursor=" + cursor
			} else {
				url += "?pagination_cursor=" + cursor
			}
		}

		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, fmt.Errorf("creating request: %w", err)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("x-luma-api-key", apiKey)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetching %s: %w", url, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("reading response: %w", err)
		}
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, string(body))
		}

		var page struct {
			Entries    []json.RawMessage `json:"entries"`
			NextCursor string            `json:"next_cursor"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("parsing response: %w", err)
		}

		all = append(all, page.Entries...)
		log.Printf("Downloaded %d items from %s", len(all), url)

		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}

	return all, nil
}

// ListEvents returns all events for the given API key.
func ListEvents(apiKey string) ([]LumaEventEntry, error) {
	raw, err := lumaGetAll("public/v1/calendar/list-events?sort_direction=desc&sort_column=start_at", apiKey)
	if err != nil {
		return nil, fmt.Errorf("listing events: %w", err)
	}

	events := make([]LumaEventEntry, 0, len(raw))
	for _, r := range raw {
		var entry LumaEventEntry
		if err := json.Unmarshal(r, &entry); err != nil {
			return nil, fmt.Errorf("parsing event: %w", err)
		}
		events = append(events, entry)
	}
	return events, nil
}

// GetGuests returns all approved guests for the given event.
func GetGuests(eventID, apiKey string) (map[string]BadgeData, error) {
	path := fmt.Sprintf("public/v1/event/get-guests?event_api_id=%s&approval_status=approved", eventID)
	raw, err := lumaGetAll(path, apiKey)
	if err != nil {
		return nil, fmt.Errorf("getting guests: %w", err)
	}

	db := make(map[string]BadgeData, len(raw))
	for _, r := range raw {
		var entry struct {
			Guest struct {
				Name                string `json:"name"`
				Email               string `json:"email"`
				RegistrationAnswers []struct {
					Label  string          `json:"label"`
					Answer json.RawMessage `json:"answer"`
				} `json:"registration_answers"`
			} `json:"guest"`
		}
		if err := json.Unmarshal(r, &entry); err != nil {
			return nil, fmt.Errorf("parsing guest: %w", err)
		}

		g := entry.Guest
		answers := make(map[string]string)
		for _, a := range g.RegistrationAnswers {
			var s string
			if err := json.Unmarshal(a.Answer, &s); err != nil {
				// Non-string answer (bool, number, etc.) — use raw JSON text
				s = strings.Trim(string(a.Answer), `"`)
			}
			answers[strings.ToLower(a.Label)] = s
		}

		const placeholder = "403 not found"
		flagged := false

		name := g.Name
		if name == "" {
			name = placeholder
			flagged = true
		}
		email := g.Email
		if email == "" {
			email = placeholder
			flagged = true
		}
		title := answers["job title"]
		if title == "" {
			title = placeholder
			flagged = true
		}
		company := answers["company"]
		if company == "" {
			company = placeholder
			flagged = true
		}

		data := BadgeData{
			Name:     Caps(name),
			Email:    email,
			Title:    title,
			Company:  company,
			LinkedIn: ExtractLinkedIn(answers),
			Flagged:  flagged,
		}
		db[data.Email] = data
	}

	return db, nil
}
