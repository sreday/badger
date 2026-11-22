package snore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTabDate(t *testing.T) {
	for tab, want := range map[string]string{
		"City 2026 Q3":          "2026-08-15",
		"Big City 2025 Q1":      "2025-02-15",
		"City 2026 Q4":          "2026-11-15",
		"2024_city":             "2024-07-01",
		"meetup-city-2024-nov":  "2024-11-15",
		"TOPIC_2025":            "2025-07-01",
		"ENTERPRISE_TOPIC_2021": "2021-07-01",
		"ambassadors":           "",
		"notes 12345":           "",
	} {
		got := ""
		if d := tabDate(tab); !d.IsZero() {
			got = d.Format("2006-01-02")
		}
		if got != want {
			t.Errorf("tabDate(%q) = %q, want %q", tab, got, want)
		}
	}
}

func TestProfileURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.linkedin.com/in/Ada-Example/":    "https://www.linkedin.com/in/ada-example/",
		"linkedin.com/in/ada-example?trk=x":           "https://www.linkedin.com/in/ada-example/",
		"http://uk.linkedin.com/in/ada-example#about": "https://www.linkedin.com/in/ada-example/",
		"https://www.linkedin.com/company/example":    "",
		"":            "",
		"ada example": "",
	} {
		if got := profileURL(in); got != want {
			t.Errorf("profileURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReadSpeakerFeed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("key") {
		case "good":
			w.Write([]byte(`{"at":"x","rows":[{"book":"Brand WWW","tab":"City 2026 Q3","name":"Ada Example","email":"ada@example.com","org":"Example Ltd","linkedin":"linkedin.com/in/ada","status":"talk"}]}`))
		case "html":
			w.Write([]byte("<html>sign in</html>"))
		default:
			w.Write([]byte(`{"error":"not allowed"}`))
		}
	}))
	defer srv.Close()
	rows, err := readSpeakerFeed(srv.URL + "?key=good")
	if err != nil || len(rows) != 1 || rows[0].Org != "Example Ltd" {
		t.Fatalf("rows = %+v, err = %v", rows, err)
	}
	for _, key := range []string{"bad", "html"} {
		_, err := readSpeakerFeed(srv.URL + "?key=" + key)
		if err == nil {
			t.Fatalf("key %q should fail", key)
		}
		if strings.Contains(err.Error(), key) || strings.Contains(err.Error(), srv.URL) {
			t.Fatalf("the error repeats the address: %v", err)
		}
	}
}

func TestSheetSpeakersBecomeTouches(t *testing.T) {
	s, _ := testServer(t)
	now := s.now() // 21 Nov 2026
	s.cfg.HumansDB, s.cfg.SpeakerFeed = "x", "https://feed.example/exec?key=k"
	s.readTouches = func(context.Context, string) (*humanData, error) {
		return &humanData{
			profiles: []humanProfile{{slug: "ada", name: "Ada Example", title: "Developer Advocate", company: "Example Ltd"}},
			speakers: []humanSpeaker{{slug: "ada", name: "Ada Example", event: "evt-1", at: now.Add(-100 * 24 * time.Hour)}},
		}, nil
	}
	calls := 0
	s.readFeed = func(string) ([]sheetSpeaker, error) {
		calls++
		if calls > 1 {
			return nil, context.DeadlineExceeded
		}
		return []sheetSpeaker{
			{Book: "Brand WWW", Tab: "City 2026 Q3", Name: "Ada Example", Email: "ada@example.com", Org: "Example Ltd", LinkedIn: "linkedin.com/in/Ada", Status: "talk"},
			{Book: "Brand WWW", Tab: "City 2026 Q3", Name: "Ben Other", Email: "ben@gmail.com", Org: "example limited", Status: "keynote"},
			{Book: "Brand WWW", Tab: "City 2026 Q3", Name: "Cy Draft", Email: "cy@draft.example", Org: "Draftco", Status: "draft"},
			{Book: "Brand WWW", Tab: "ambassadors", Name: "No Date", Email: "n@nodate.example", Org: "Nodate"},
			{Book: "Brand WWW", Tab: "City 2021 Q3", Name: "Old Talk", Email: "o@old.example", Org: "Oldco", Status: "talk"},
		}, nil
	}
	s.syncTouches()

	by := map[string][]Touch{}
	for _, tc := range s.touches {
		by[tc.Company] = append(by[tc.Company], tc)
	}
	// Ada from the sheet and Ada from the outreach log are one person at
	// one company; Ben has private mail but the same typed company.
	w := warmthOf(by["example.com"], now)
	if w.Level != 5 || w.People != 2 || w.Owner != Organizer {
		t.Fatalf("example.com = %+v (touches %+v)", w, by["example.com"])
	}
	if got := warmthOf(by["draft.example"], now).Level; got != 2 {
		t.Fatalf("a draft row is a submission, level 2, got %d", got)
	}
	if len(by["nodate.example"]) != 0 {
		t.Fatal("a tab without a year gives no touch")
	}
	if got := warmthOf(by["old.example"], now).Level; got != 0 {
		t.Fatalf("a talk from five years ago has faded, got %d", got)
	}

	// The feed fails on the next read: the last good rows stay.
	s.syncTouches()
	if len(s.sheetRows) != 5 {
		t.Fatalf("the last good read should be kept, have %d rows", len(s.sheetRows))
	}
}
