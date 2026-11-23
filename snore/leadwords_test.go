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

func TestRowWords(t *testing.T) {
	for in, want := range map[string]string{
		"Senior SRE | Speaker | Helping teams sleep": "Senior SRE",
		"Building the future of on-call":             "",
		"a drummer posing as a Consulting Engineer":  "",
		"CEO at Example (ex Other)":                  "CEO",
		"VP - Head of Global Sales":                  "VP - Head of Global Sales",
		"ABC":                                        "",
	} {
		if got := jobTitle(in); got != want {
			t.Errorf("jobTitle(%q) = %q, want %q", in, got, want)
		}
	}
	if got := cleanName("jane doe ☁️"); got != "Jane Doe" {
		t.Errorf("cleanName = %q", got)
	}
	if got := betterWritten("Examplecorp", "ExampleCorp"); got != "ExampleCorp" {
		t.Errorf("betterWritten = %q", got)
	}
	if got := betterWritten("Example", "Other"); got != "Example" {
		t.Errorf("betterWritten kept %q", got)
	}
	if !ownSite("janedoe.com", "Jane Doe") || ownSite("example.com", "Jane Doe") {
		t.Error("ownSite")
	}
	if normCompany("7 yrs 8 mos") != "" {
		t.Error("a tenure is no company")
	}
	if workDomain("jane@fastmail.co") != "" {
		t.Error("a mail provider is no employer")
	}
}
