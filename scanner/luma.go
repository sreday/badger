package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/sreday/badger/pkg"
)

// lumaBaseURL is a variable so tests can point it at a fake Luma.
var lumaBaseURL = "https://api.lu.ma"

var lumaClient = &http.Client{Timeout: 30 * time.Second}

// LumaEvent is the part of a Luma event the scanner needs.
type LumaEvent struct {
	APIId    string `json:"api_id"`
	Name     string `json:"name"`
	StartAt  string `json:"start_at"`
	EndAt    string `json:"end_at"`
	Timezone string `json:"timezone"`
}

// Times returns the event's start and end. A missing end counts as 12 hours
// after the start.
func (e LumaEvent) Times() (start, end time.Time) {
	start, _ = time.Parse(time.RFC3339, e.StartAt)
	end, _ = time.Parse(time.RFC3339, e.EndAt)
	if end.IsZero() && !start.IsZero() {
		end = start.Add(12 * time.Hour)
	}
	return start.UTC(), end.UTC()
}

// lumaGetAll fetches all pages from a paginated Lu.ma API endpoint.
func lumaGetAll(path, apiKey string) ([]json.RawMessage, error) {
	var all []json.RawMessage
	cursor := ""
	for {
		u := lumaBaseURL + "/" + path
		if cursor != "" {
			sep := "?"
			if strings.Contains(u, "?") {
				sep = "&"
			}
			u += sep + "pagination_cursor=" + url.QueryEscape(cursor)
		}
		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			return nil, fmt.Errorf("creating request: %w", err)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("x-luma-api-key", apiKey)

		resp, err := lumaClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetching %s: %w", path, err)
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
		if page.NextCursor == "" {
			return all, nil
		}
		cursor = page.NextCursor
	}
}

// ListEvents returns the calendar's events, newest first.
func ListEvents(apiKey string) ([]LumaEvent, error) {
	raw, err := lumaGetAll("public/v1/calendar/list-events?sort_direction=desc&sort_column=start_at", apiKey)
	if err != nil {
		return nil, fmt.Errorf("listing events: %w", err)
	}
	events := make([]LumaEvent, 0, len(raw))
	for _, r := range raw {
		var entry struct {
			Event LumaEvent `json:"event"`
		}
		if err := json.Unmarshal(r, &entry); err != nil {
			return nil, fmt.Errorf("parsing event: %w", err)
		}
		events = append(events, entry.Event)
	}
	return events, nil
}

// GetGuests returns all approved guests of an event.
func GetGuests(eventID, apiKey string) ([]Guest, error) {
	path := "public/v1/event/get-guests?approval_status=approved&event_api_id=" + url.QueryEscape(eventID)
	raw, err := lumaGetAll(path, apiKey)
	if err != nil {
		return nil, fmt.Errorf("getting guests: %w", err)
	}
	guests := make([]Guest, 0, len(raw))
	for _, r := range raw {
		var entry struct {
			Guest struct {
				APIId         string `json:"api_id"`
				Name          string `json:"name"`
				Email         string `json:"email"`
				FirstName     string `json:"user_first_name"`
				LastName      string `json:"user_last_name"`
				Phone         string `json:"phone_number"`
				CheckInQRCode string `json:"check_in_qr_code"`
				CheckedInAt   string `json:"checked_in_at"`
				EventTickets  []struct {
					APIId string `json:"api_id"`
				} `json:"event_tickets"`
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
		var all []Answer
		for _, a := range g.RegistrationAnswers {
			s := answerText(a.Answer)
			answers[strings.ToLower(a.Label)] = s
			all = append(all, Answer{Label: strings.TrimSpace(a.Label), Value: s})
		}
		guest := Guest{
			ID:        g.APIId,
			FirstName: strings.TrimSpace(g.FirstName),
			LastName:  strings.TrimSpace(g.LastName),
			Name:      strings.TrimSpace(g.Name),
			Email:     strings.TrimSpace(g.Email),
			Title:     strings.TrimSpace(answers["job title"]),
			Company:   strings.TrimSpace(answers["company"]),
			LinkedIn:  pkg.ExtractLinkedIn(answers),
			Phone:     strings.TrimSpace(g.Phone),
			Answers:   all,
			CheckedIn: g.CheckedInAt != "",
		}
		if u, err := url.Parse(g.CheckInQRCode); err == nil {
			guest.Key = u.Query().Get("pk")
		}
		for _, t := range g.EventTickets {
			if t.APIId != "" {
				guest.Tickets = append(guest.Tickets, t.APIId)
			}
		}
		guests = append(guests, guest)
	}
	return guests, nil
}

// answerText turns a registration answer into text: strings as they are,
// checkboxes as Yes/No, lists joined with commas.
func answerText(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return strings.Trim(string(raw), `"`)
	}
	switch v := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case bool:
		if v {
			return "Yes"
		}
		return "No"
	case []any:
		parts := make([]string, 0, len(v))
		for _, x := range v {
			parts = append(parts, fmt.Sprint(x))
		}
		return strings.Join(parts, ", ")
	default:
		return fmt.Sprint(v)
	}
}

// GuestCache keeps one GuestIndex per event and refreshes it in the
// background, so a scan never waits on Luma once the first fetch is done.
type GuestCache struct {
	apiKey string
	maxAge time.Duration

	mu      sync.Mutex
	entries map[string]*guestEntry
}

type guestEntry struct {
	ix         *GuestIndex
	fetchedAt  time.Time
	triedAt    time.Time
	refreshing bool
}

func NewGuestCache(apiKey string, maxAge time.Duration) *GuestCache {
	return &GuestCache{apiKey: apiKey, maxAge: maxAge, entries: make(map[string]*guestEntry)}
}

// Index returns the cached index for an event. The first call blocks on Luma;
// later calls return immediately and refresh in the background when stale.
func (c *GuestCache) Index(eventID string) *GuestIndex {
	c.mu.Lock()
	e := c.entries[eventID]
	if e != nil && e.ix != nil {
		if time.Since(e.fetchedAt) > c.maxAge && !e.refreshing {
			e.refreshing = true
			go c.refresh(eventID)
		}
		ix := e.ix
		c.mu.Unlock()
		return ix
	}
	if e != nil && time.Since(e.triedAt) < time.Minute {
		// Luma failed moments ago: don't make every scan wait on it again.
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	return c.refresh(eventID)
}

// Fresh blocks until the index is no older than maxAge (used for exports).
func (c *GuestCache) Fresh(eventID string, maxAge time.Duration) *GuestIndex {
	c.mu.Lock()
	e := c.entries[eventID]
	c.mu.Unlock()
	if e != nil && e.ix != nil && time.Since(e.fetchedAt) <= maxAge {
		return e.ix
	}
	return c.refresh(eventID)
}

// refresh fetches the guest list. On failure the previous index is kept.
func (c *GuestCache) refresh(eventID string) *GuestIndex {
	guests, err := GetGuests(eventID, c.apiKey)

	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[eventID]
	if e == nil {
		e = &guestEntry{}
		c.entries[eventID] = e
	}
	e.refreshing = false
	e.triedAt = time.Now()
	if err != nil {
		log.Printf("Guest refresh for %s failed: %v", eventID, err)
		return e.ix
	}
	e.ix = NewGuestIndex(guests)
	e.fetchedAt = time.Now()
	log.Printf("Guest list for %s: %d approved guests", eventID, len(guests))
	return e.ix
}
