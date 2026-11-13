package snap

import (
	"strings"
	"testing"
	"time"
)

func TestRowLine(t *testing.T) {
	for in, want := range map[string]string{
		"Aleksandra and Raymond":                         "Aleksandra <> Raymond",
		"Rod and Kyndryl":                                "Rod <> Kyndryl",
		"Aleksandra (SREday) and Raymond Smith, Kyndryl": "Aleksandra <> Raymond",
		"Anna & the venue":                               "Anna <> venue",
		"Miko, Anna and Jo":                              "Miko <> Anna",
		"":                                               "",
	} {
		if got := betweenShort(in); got != want {
			t.Errorf("betweenShort(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"Prompt Engineering Conference London 2026":           "PEC London 2026",
		"the Prompt Engineering Conference (PEC) London 2026": "PEC London 2026",
		"SREday London 2026":                                  "SREday London 2026",
		"":                                                    "",
	} {
		if got := confName(in); got != want {
			t.Errorf("confName(%q) = %q, want %q", in, got, want)
		}
	}
	if c, s := ball(Thread{Waiting: "you"}); c != "you" || s != "Ball in your park" {
		t.Errorf("you: %q %q", c, s)
	}
	if c, s := ball(Thread{Waiting: "them"}); c != "them" || s != "Ball in their park" {
		t.Errorf("them: %q %q", c, s)
	}
	if c, s := ball(Thread{Waiting: "between", Between: "Aleksandra and Raymond"}); c != "between" || s != "Aleksandra <> Raymond" {
		t.Errorf("between: %q %q", c, s)
	}
}

func TestBrandSpelling(t *testing.T) {
	for in, want := range map[string]string{
		"Acme wants a booth at SRE Day London and Sreday NYC":     "Acme wants a booth at SREday London and SREday NYC",
		"LLM Day Hamburg, PlatformDay SF, conf 42 and CONF42":     "LLMday Hamburg, PLATFORMday SF, Conf42 and Conf42",
		"prompt engineering conference (pec) London":              "Prompt Engineering Conference (PEC) London",
		"Write to mark@sreday.com, see https://conf42.com/sreday": "Write to mark@sreday.com, see https://conf42.com/sreday",
		"SREday, LLMday, PLATFORMday, Conf42 and PEC stay as is.": "SREday, LLMday, PLATFORMday, Conf42 and PEC stay as is.",
		"Specific spectacle, platforms day by day":                "Specific spectacle, platforms day by day",
	} {
		if got := fixBrands(in); got != want {
			t.Errorf("fixBrands(%q)\n got %q\nwant %q", in, got, want)
		}
	}
	th := Thread{Label: "Sreday London Gold open", Actions: []Action{{Title: "Offer LLM Day Hamburg"}}}
	fixCard(&th)
	if th.Label != "SREday London Gold open" || th.Actions[0].Title != "Offer LLMday Hamburg" {
		t.Errorf("fixCard: %+v", th)
	}
}

func TestBallWithMarkFirst(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	mk := func(id, pri, ball string, age time.Duration) Row {
		return Row{Thread: Thread{ID: id, Priority: pri, Waiting: ball, LastAt: now.Add(-age)}, Fresh: now.Add(-age), State: "open"}
	}
	rows := orderTab([]Row{
		mk("them-high-fresh", "high", "them", 1*day),
		mk("you-low-old", "low", "you", 80*day),
		mk("you-med-fresh", "medium", "you", 3*day),
		mk("between-high-old", "high", "between", 50*day),
		mk("you-high-old", "high", "you", 60*day),
	}, now)
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	// Top block: the recent ones, Mark's first. Then the rest: Mark's first
	// (high before low), then the others.
	want := "you-med-fresh them-high-fresh you-high-old you-low-old between-high-old"
	if got := strings.Join(ids, " "); got != want {
		t.Fatalf("order:\n got %s\nwant %s", got, want)
	}
}
