package snoop

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Luma: the events of the Conf42 calendar (online conferences and webinars)
// and the SREday + LLMday + PLATFORMday calendar (in person), read from
// Luma's public iCal feeds - free, no API key (Marek 2026-11-11: "have Snoop
// pull meetings from Luma"). Webinars are matched to the Meet trace that
// recorded them, so their Gemini notes + transcript feed the blog recap.

const lumaEvery = 15 * time.Minute

// LumaFeedURL is a variable so tests can point it at a fake Luma.
var LumaFeedURL = "https://api.luma.com/ics/get?entity=calendar&id="

var lumaClient = &http.Client{Timeout: 30 * time.Second}

// lumaCalendars: Conf42, then SREday + LLMday + PLATFORMday.
func lumaCalendars() []string {
	v := envOr("SNOOP_LUMA_CALENDARS", "cal-7AchTZw3ZjU5sOm,cal-cPvICdCwmTwqvT9")
	var out []string
	for _, c := range strings.Split(v, ",") {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// LumaEvent is one event of a Luma calendar.
type LumaEvent struct {
	ID         string // evt-...
	Title      string
	URL        string // the event's Luma page
	Start, End time.Time
	Online     bool   // a Meet/Zoom event: its location is the Luma page itself
	Place      string // in person: the venue (first part of the address)
}

// Brand is the event's family, for the tinted pill: sreday, llmday,
// platformday, pec, conf42 or "".
func (e LumaEvent) Brand() string {
	t := strings.ToLower(e.Title)
	for _, b := range []string{"sreday", "llmday", "platformday", "conf42"} {
		if strings.HasPrefix(t, b) {
			return b
		}
	}
	if strings.Contains(t, "prompt engineering conference") {
		return "pec"
	}
	return ""
}

// BrandName is how the pill spells the brand.
func (e LumaEvent) BrandName() string {
	return map[string]string{"sreday": "SREday", "llmday": "LLMday", "platformday": "PLATFORMday", "pec": "PEC", "conf42": "Conf42"}[e.Brand()]
}

// Webinar: online and not one of the Conf42 online conferences.
func (e LumaEvent) Webinar() bool { return e.Online && e.Brand() != "conf42" }

// Kind is the first pill: Webinar, Online conference, or the venue.
func (e LumaEvent) Kind() string {
	switch {
	case e.Webinar():
		return "Webinar"
	case e.Online:
		return "Online conference"
	}
	return e.Place
}

var lumaURLRe = regexp.MustCompile(`https://luma\.com/[^\s\\]+`)

// parseICS reads the VEVENTs of a Luma iCal feed.
func parseICS(src string) []LumaEvent {
	// Unfold continuation lines (RFC 5545: a line starting with a space or
	// tab continues the previous one).
	src = strings.ReplaceAll(src, "\r\n", "\n")
	src = strings.NewReplacer("\n ", "", "\n\t", "").Replace(src)
	var out []LumaEvent
	var e *LumaEvent
	var desc, loc string
	for _, line := range strings.Split(src, "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key, _, _ = strings.Cut(key, ";")
		switch key {
		case "BEGIN":
			if val == "VEVENT" {
				e, desc, loc = &LumaEvent{}, "", ""
			}
		case "END":
			if val == "VEVENT" && e != nil {
				e.Online = strings.HasPrefix(loc, "https://luma.com/event/") || loc == ""
				if !e.Online {
					e.Place, _, _ = strings.Cut(loc, ",")
				}
				if m := lumaURLRe.FindString(desc); m != "" {
					e.URL = m
				} else if strings.HasPrefix(loc, "https://") {
					e.URL = loc
				}
				if e.ID != "" && !e.Start.IsZero() {
					out = append(out, *e)
				}
				e = nil
			}
		}
		if e == nil {
			continue
		}
		switch key {
		case "UID":
			e.ID, _, _ = strings.Cut(val, "@")
		case "SUMMARY":
			e.Title = icsText(val)
		case "DESCRIPTION":
			desc = icsText(val)
		case "LOCATION":
			loc = icsText(val)
		case "DTSTART":
			e.Start, _ = time.Parse("20060102T150405Z", val)
		case "DTEND":
			e.End, _ = time.Parse("20060102T150405Z", val)
		}
	}
	return out
}

func icsText(s string) string {
	return strings.NewReplacer(`\n`, "\n", `\N`, "\n", `\,`, ",", `\;`, ";", `\\`, `\`).Replace(s)
}

// fetchLuma reads every calendar's feed. One failing calendar doesn't drop
// the others; its error is returned with what did come in.
func fetchLuma() ([]LumaEvent, error) {
	var all []LumaEvent
	var errs []string
	for _, cal := range lumaCalendars() {
		req, _ := http.NewRequest("GET", LumaFeedURL+cal, nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 (Snoop by Badger)") // Luma's edge refuses bare clients
		resp, err := lumaClient.Do(req)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			errs = append(errs, fmt.Sprintf("%s: HTTP %d", cal, resp.StatusCode))
			continue
		}
		all = append(all, parseICS(string(body))...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Start.Before(all[j].Start) })
	if len(errs) > 0 {
		return all, fmt.Errorf("reading Luma: %s", strings.Join(errs, "; "))
	}
	return all, nil
}

// refreshLuma re-reads the feeds when the copy is older than lumaEvery.
func (s *Server) refreshLuma(now time.Time, force bool) {
	s.lumaMu.Lock()
	stale := force || now.Sub(s.lumaAt) >= lumaEvery
	s.lumaMu.Unlock()
	if !stale || os.Getenv("SNOOP_LUMA_OFF") != "" {
		return
	}
	events, err := fetchLuma()
	s.lumaMu.Lock()
	defer s.lumaMu.Unlock()
	if len(events) > 0 || err == nil {
		s.luma = events
	}
	s.lumaAt, s.lumaErr = now, err
}

// lumaTrace is the Meet trace that recorded an online Luma event: a call
// with a Meet overlapping the event, the same title first.
func lumaTrace(e LumaEvent, calls []Call) (Call, bool) {
	if !e.Online {
		return Call{}, false
	}
	var best Call
	found, named := false, false
	for _, c := range calls {
		if c.MeetingCode == "" || c.Cancelled || !c.Start.Before(e.End) || !c.End.After(e.Start) {
			continue
		}
		same := sameTitle(c.Title, e.Title)
		if !found || same && !named {
			best, found, named = c, true, same
		}
	}
	return best, found
}

func sameTitle(a, b string) bool {
	a, b = strings.ToLower(strings.TrimSpace(a)), strings.ToLower(strings.TrimSpace(b))
	return a != "" && b != "" && (strings.Contains(a, b) || strings.Contains(b, a))
}

// lumaRow is one line of the Luma tab.
type lumaRow struct {
	LumaEvent
	Trace  *callRow // the Meet trace, for online events Snoop recorded or will record
	Rel    string
	Base   string
	TZ     *time.Location
	Status string
	Dead   bool // in the Dead end bin (dead.go)
}

// renderLuma is the Luma tab: upcoming events of both calendars on the right
// (soonest first), past online events with their recordings on the left, and
// the actions of those recordings below.
func (s *Server) renderLuma(w http.ResponseWriter, r *http.Request, now time.Time, all []Call, recurring map[string]bool) {
	s.lumaMu.Lock()
	events, lumaErr, fetched := s.luma, s.lumaErr, s.lumaAt
	s.lumaMu.Unlock()
	traced := map[string]bool{}
	row := func(e LumaEvent) lumaRow {
		lr := lumaRow{LumaEvent: e, Rel: relDay(e.Start, now, s.tz), Base: s.cfg.Base, TZ: s.tz}
		if c, ok := lumaTrace(e, all); ok {
			cr := s.row(c, now)
			lr.Trace, traced[c.ID] = &cr, true
		} else if e.Online && e.Start.After(now) {
			lr.Status = "no Meet in the calendar yet"
		} else if e.Online {
			lr.Status = "not recorded"
		}
		return lr
	}
	var past, upcoming []lumaRow
	for _, e := range events {
		if s.dead.has(e.URL) {
			continue // in the Dead end bin (dead.go)
		}
		switch {
		case e.End.After(now):
			upcoming = append(upcoming, row(e))
		case e.Online && e.Start.After(now.Add(-90*24*time.Hour)):
			past = append([]lumaRow{row(e)}, past...) // latest first
		}
	}
	open, resolved := s.openActions(now)
	pick := func(l []action) []action {
		var out []action
		for _, a := range l {
			if traced[a.Call.ID] {
				out = append(out, a)
			}
		}
		return out
	}
	errText := ""
	if lumaErr != nil {
		errText = lumaErr.Error()
	} else if fetched.IsZero() {
		errText = "Luma not read yet: press Sync."
	}
	s.render(w, "admin.html", map[string]any{
		"Actions":      pick(open),
		"Resolved":     pick(resolved),
		"View":         "luma",
		"LumaPast":     past,
		"LumaUpcoming": upcoming,
		"Kind":         "all",
		"SyncError":    errText,
	})
}
