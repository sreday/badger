package bridge

import "testing"

func TestMatcher(t *testing.T) {
	team := []string{"sreday.com", "conf42.com"}
	m := NewMatcher([]string{"Jane@Acme.com", "bo@gmail.com", "mark@sreday.com"}, "Globex Inc", team)
	for _, c := range []struct {
		emails  []string
		company string
		want    bool
	}{
		{[]string{"jane@acme.com"}, "", true},      // the same person
		{[]string{"sam@acme.com"}, "", true},       // a colleague: same work domain
		{[]string{"bo@gmail.com"}, "", true},       // the same private address
		{[]string{"other@gmail.com"}, "", false},   // gmail.com is nobody's company
		{[]string{"anna@sreday.com"}, "", false},   // the team is on everything
		{[]string{"mark@sreday.com"}, "", false},   // so is Mark
		{[]string{"x@y.com"}, "globex  inc", true}, // the company, however it's spaced
		{[]string{"x@y.com"}, "Initech", false},
		{nil, "", false},
	} {
		if got := m.Hit(c.emails, c.company); got != c.want {
			t.Errorf("Hit(%v, %q) = %v, want %v", c.emails, c.company, got, c.want)
		}
	}
	if !NewMatcher([]string{"mark@sreday.com"}, "", team).Empty() {
		t.Error("only team addresses: nothing to match on")
	}
	c := Call{Steps: []Step{{I: 0, Text: "Mark: send the deck", Mine: true}, {I: 1, Text: "Jane: check budget"}, {I: 2, Text: "Mark: intro", Mine: true, Done: true}}}
	if len(c.Open(true)) != 1 || len(c.Open(false)) != 2 {
		t.Errorf("Open: %v %v", c.Open(true), c.Open(false))
	}
}
