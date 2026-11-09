// Package luma is the part of the Luma API that Snout and Sniff use: the
// calendar's events and an event's approved guests.
package luma

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/sreday/badger/pkg"
)

// BaseURL is a variable so tests can point it at a fake Luma.
var BaseURL = "https://api.lu.ma"

var client = &http.Client{Timeout: 30 * time.Second}

// Event is the part of a Luma event the scanner needs.
type Event struct {
	APIId    string `json:"api_id"`
	Name     string `json:"name"`
	StartAt  string `json:"start_at"`
	EndAt    string `json:"end_at"`
	Timezone string `json:"timezone"`
}

// Times returns the event's start and end. A missing end counts as 12 hours
// after the start.
func (e Event) Times() (start, end time.Time) {
	start, _ = time.Parse(time.RFC3339, e.StartAt)
	end, _ = time.Parse(time.RFC3339, e.EndAt)
	if end.IsZero() && !start.IsZero() {
		end = start.Add(12 * time.Hour)
	}
	return start.UTC(), end.UTC()
}

// getAll fetches all pages from a paginated Lu.ma API endpoint.
func getAll(path, apiKey string) ([]json.RawMessage, error) {
	var all []json.RawMessage
	cursor := ""
	for {
		u := BaseURL + "/" + path
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

		resp, err := client.Do(req)
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

// RecentEvents keeps events from the last week onwards, soonest first;
// sponsors aren't set up for old ones.
func RecentEvents(events []Event) []Event {
	cutoff := time.Now().AddDate(0, 0, -7)
	var recent []Event
	for _, e := range events {
		if t, err := time.Parse(time.RFC3339, e.StartAt); err != nil || t.After(cutoff) {
			recent = append(recent, e)
		}
	}
	sort.SliceStable(recent, func(i, j int) bool { return recent[i].StartAt < recent[j].StartAt })
	return recent
}

// ListEvents returns the calendar's events, newest first.
func ListEvents(apiKey string) ([]Event, error) {
	raw, err := getAll("public/v1/calendar/list-events?sort_direction=desc&sort_column=start_at", apiKey)
	if err != nil {
		return nil, fmt.Errorf("listing events: %w", err)
	}
	events := make([]Event, 0, len(raw))
	for _, r := range raw {
		var entry struct {
			Event Event `json:"event"`
		}
		if err := json.Unmarshal(r, &entry); err != nil {
			return nil, fmt.Errorf("parsing event: %w", err)
		}
		events = append(events, entry.Event)
	}
	return events, nil
}

// Guest is an approved Luma guest, reduced to what a lead needs.
type Guest struct {
	ID          string // gst-...
	Key         string // g-... (pk= in the check-in QR)
	Tickets     []string
	FirstName   string
	LastName    string
	Name        string
	Email       string
	Title       string
	Company     string
	LinkedIn    string // normalized by pkg.ExtractLinkedIn, same as the badge QR
	Phone       string
	Answers     []Answer // every registration answer, in form order
	CheckedIn   bool
	CheckedInAt time.Time // zero when not checked in
}

// Answer is one registration question and the guest's answer.
type Answer struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// ExtraAnswers are the registration answers not already in a lead's own
// columns (job title, company, LinkedIn).
func (g *Guest) ExtraAnswers() []Answer {
	var out []Answer
	for _, a := range g.Answers {
		l := strings.ToLower(a.Label)
		if l == "job title" || l == "company" || strings.Contains(l, "linkedin") {
			continue
		}
		out = append(out, a)
	}
	return out
}

// GetGuests returns all approved guests of an event.
func GetGuests(eventID, apiKey string) ([]Guest, error) {
	path := "public/v1/event/get-guests?approval_status=approved&event_api_id=" + url.QueryEscape(eventID)
	raw, err := getAll(path, apiKey)
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
		if t, err := time.Parse(time.RFC3339, g.CheckedInAt); err == nil {
			guest.CheckedInAt = t
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
