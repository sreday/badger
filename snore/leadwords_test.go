package snore

import "testing"

func TestMailFitsAndShortTitle(t *testing.T) {
	for _, c := range []struct {
		email, name string
		want        bool
	}{
		{"ada@work.example", "Someone Else", true}, // a work address is taken as it is
		{"ada.example7@gmail.com", "Ada Example", true},
		{"aexample@gmail.com", "Ada Example", true},
		{"bo.sample13@gmail.com", "Ada Example", false}, // another person's private address
		{"", "Ada Example", false},
	} {
		if got := mailFits(c.email, c.name); got != c.want {
			t.Errorf("mailFits(%q, %q) = %v", c.email, c.name, got)
		}
	}
	for in, want := range map[string]string{
		"SRE | Speaker | Helping teams sleep":   "SRE",
		"Developer Advocate @ Example":          "Developer Advocate",
		"Head of Events":                        "Head of Events",
		"Field CTO • Example • Building things": "Field CTO",
	} {
		if got := shortTitle(in); got != want {
			t.Errorf("shortTitle(%q) = %q, want %q", in, got, want)
		}
	}
}
