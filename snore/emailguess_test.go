package snore

import "testing"

func TestGuessMail(t *testing.T) {
	shapes := learnShapes([]namedMail{
		{"Ada Example", "ada.example@one.example"}, {"Bo Sample", "bo.sample@one.example"}, {"Cy Other", "cother@one.example"},
		{"Dee Flast", "dflast@two.example"},
		{"Eve First", "eve@three.example"}, {"Fay Dot", "fay.dot@three.example"}, // a tie: the commoner shape in general wins
		{"Gus Private", "gus.private@gmail.com"},
		{"Hal Odd", "h4l@four.example"},
		{"Ida Twice", "ida.twice@five.example"}, {"Ida Twice", "IDA.TWICE@five.example"},
	})
	if s := shapes["one.example"]; s.Shape != "first.last" || s.N != 2 {
		t.Errorf("one.example = %+v", s)
	}
	if s := shapes["three.example"]; s.Shape != "first.last" || s.N != 1 {
		t.Errorf("a tie = %+v", s)
	}
	if s := shapes["five.example"]; s.N != 1 {
		t.Errorf("one address counted twice: %+v", s)
	}
	for _, c := range []struct{ domain, name, want, basis string }{
		{"one.example", "Zoë Müller-Brown", "zoe.brown@one.example", "first.last, from 2 addresses we hold"},
		{"two.example", "Pat O'Neil", "poneil@two.example", "flast, from 1 address we hold"},
		{"four.example", "Any Body", "", ""},    // no address there follows a shape
		{"nowhere.example", "Any Body", "", ""}, // no address held at all
		{"gmail.com", "Gus Other", "", ""},      // private mail teaches nothing
		{"one.example", "Mononym", "", ""},      // one name is not enough
		{"name:one", "Ada Example", "", ""},     // a company known by name only has no domain
		{"one.example", "Łukasz Żak", "", ""},   // letters an address cannot carry: no guess
	} {
		got, basis := guessMail(shapes, c.domain, c.name)
		if got != c.want || basis != c.basis {
			t.Errorf("guessMail(%s, %q) = %q (%q), want %q (%q)", c.domain, c.name, got, basis, c.want, c.basis)
		}
	}
}
