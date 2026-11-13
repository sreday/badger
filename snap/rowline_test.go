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
	// All of Mark.s first (recent, then high before low), then the others
	// (high before low), then the others.
	want := "you-med-fresh you-high-old you-low-old them-high-fresh between-high-old"
	if got := strings.Join(ids, " "); got != want {
		t.Fatalf("order:\n got %s\nwant %s", got, want)
	}
}

func TestNeonColors(t *testing.T) {
	one := neonColors(Thread{Event: "SREday London 2027", Label: "Echo sponsorship went quiet"})
	if len(one) != 1 || one[0] != "#a855f7" {
		t.Errorf("SREday: %v", one)
	}
	many := neonColors(Thread{Label: "ClickHouse US events for 2027", Summary: "They asked about LLMday SF, PLATFORMday Austin and Conf42 Observability."})
	if strings.Join(many, " ") != "#22c55e #f97316 #1e40af" {
		t.Errorf("several events: %v", many)
	}
	if pec := neonColors(Thread{Event: "PEC London 2026"}); len(pec) != 1 || pec[0] != "#1e40af" {
		t.Errorf("PEC: %v", pec)
	}
	if none := neonColors(Thread{Label: "Acme wants a call"}); len(none) != 1 || none[0] != neonPlain {
		t.Errorf("no conference: %v", none)
	}
	if s := string(neonStyle(Thread{Event: "LLMday Hamburg 2026"})); s != "--neon: linear-gradient(90deg, #22c55e, #22c55e); --neon1: #22c55e" {
		t.Errorf("style: %q", s)
	}
}

func TestSisterBrands(t *testing.T) {
	th := Thread{Subject: "Free pass to cfp ninja for Signals Conference", Label: "Signals: Heinrich open to CFP.ninja"}
	if got := tagOf(th); got != "CFP.ninja" {
		t.Errorf("tag = %q, want CFP.ninja", got)
	}
	if got := tagOf(Thread{Event: "SREday London 2027", Label: "Acme asks about CFP.ninja"}); got != "SREday London 2027" {
		t.Errorf("a conference wins the tag: %q", got)
	}
	if got := tagOf(Thread{Subject: "Lunch on Friday?"}); got != "" {
		t.Errorf("no brand, no tag: %q", got)
	}
	for in, want := range map[string]string{
		"Feedback on reliaburger chapter 3": "Reliaburger",
		"certified fyi partnership":         "certified.fyi",
		"Badger lead scanner for Devoxx":    "Badger",
	} {
		if got := tagOf(Thread{Subject: in}); got != want {
			t.Errorf("tagOf(%q) = %q, want %q", in, got, want)
		}
	}
	if got := fixBrands("Heinrich is open to cfp ninja and Relia Burger; see https://cfp.ninja/e/signals"); got != "Heinrich is open to CFP.ninja and Reliaburger; see https://cfp.ninja/e/signals" {
		t.Errorf("fixBrands: %q", got)
	}
	if c := neonColors(th); len(c) != 1 || c[0] != "#0d9488" {
		t.Errorf("neon for CFP.ninja: %v", c)
	}
	if !strings.Contains(Instructions, "SISTER BRANDS AND INITIATIVES") {
		t.Error("the instructions do not cover the sister brands")
	}
}
