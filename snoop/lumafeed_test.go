package snoop

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testICS = "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nDTSTART:%s\r\nDTEND:%s\r\nUID:evt-web1@events.lu.ma\r\nSUMMARY:Reliaburger 0.3.0 online workshop\r\nDESCRIPTION:Get up-to-date information at: https://luma.com/webinar-reliab\r\n urger-dec-2026\\n\\nHosted by Mark\r\nLOCATION:https://luma.com/event/evt-web1\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nDTSTART:%s\r\nDTEND:%s\r\nUID:evt-ham@events.lu.ma\r\nSUMMARY:LLMday Hamburg 2026 Q4\r\nDESCRIPTION:Get up-to-date information at: https://luma.com/llmday-hamburg\r\nLOCATION:House of AI Hamburg\\, Hongkongstraße 2\\, 20457 Hamburg\r\nGEO:53.5;10.0\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

func icsTime(t time.Time) string { return t.UTC().Format("20060102T150405Z") }

func TestParseICS(t *testing.T) {
	a := time.Date(2026, 12, 1, 15, 0, 0, 0, time.UTC)
	ev := parseICS(fmt.Sprintf(testICS, icsTime(a), icsTime(a.Add(time.Hour)), icsTime(a.Add(48*time.Hour)), icsTime(a.Add(56*time.Hour))))
	if len(ev) != 2 {
		t.Fatalf("want 2 events, got %d", len(ev))
	}
	w, h := ev[0], ev[1]
	if w.ID != "evt-web1" || !w.Online || !w.Webinar() || w.Kind() != "Webinar" || w.URL != "https://luma.com/webinar-reliaburger-dec-2026" || !w.Start.Equal(a) {
		t.Errorf("webinar parsed wrong: %+v", w)
	}
	if h.Online || h.Place != "House of AI Hamburg" || h.Brand() != "llmday" || h.Kind() != "House of AI Hamburg" {
		t.Errorf("in-person event parsed wrong: %+v", h)
	}
}

func TestLumaTab(t *testing.T) {
	s := newTestServer(t)
	start := s.now().Add(2 * time.Hour).Truncate(time.Minute)
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.RawQuery, "cal-a") {
			fmt.Fprintf(w, testICS, icsTime(start), icsTime(start.Add(time.Hour)), icsTime(start.Add(48*time.Hour)), icsTime(start.Add(56*time.Hour)))
		}
	}))
	defer feed.Close()
	old := LumaFeedURL
	LumaFeedURL = feed.URL + "/?id="
	defer func() { LumaFeedURL = old }()
	t.Setenv("SNOOP_LUMA_CALENDARS", "cal-a,cal-b")
	s.store.Update("web", func(c *Call) {
		c.Title, c.MeetingCode, c.Start, c.End, c.Owned = "Reliaburger 0.3.0 online workshop", "aaa-bbbb-ccc", start, start.Add(time.Hour), true
	})
	s.refreshLuma(s.now(), true)
	r := httptest.NewRequest("GET", "/?view=luma", nil)
	r.SetBasicAuth("", "pw")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	b := w.Body.String()
	for _, want := range []string{"Upcoming on Luma", "Reliaburger 0.3.0 online workshop", "LLMday Hamburg 2026 Q4", "brand-llmday", `class="topic webinar">Webinar`, "/snoop/admin/call/web", "Snoop will record"} {
		if !strings.Contains(b, want) {
			t.Errorf("Luma tab is missing %q", want)
		}
	}
	if strings.Index(b, "Reliaburger") > strings.Index(b, "LLMday Hamburg") {
		t.Error("upcoming Luma events should be soonest first")
	}
}
