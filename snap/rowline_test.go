package snap

import "testing"

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
