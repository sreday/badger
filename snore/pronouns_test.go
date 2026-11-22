package snore

import "testing"

func TestPronounsOnlyFromPublishedWords(t *testing.T) {
	for text, want := range map[string]string{
		"Kim Doe (she/her), SRE lead":                                   "she",
		"Alex leads SRE. She speaks often and her talks are practical.": "she",
		"Sam runs platforms. He writes and his team ships.":             "he",
		"Robin builds things (they/them).":                              "they",
		"Maria Garcia, Staff Engineer at Example":                       "", // a name says nothing
		"She founded it.": "", // one word is not enough
	} {
		if got := pronounsOf(text); got != want {
			t.Errorf("pronounsOf(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestWomenFirstAmongRelevant(t *testing.T) {
	s, _ := testServer(t)
	s.mu.Lock()
	src := &Source{Event: "SomeConf 2026"}
	s.addToPool(src, PoolSpeaker{Name: "Hal Strong", Bio: "He runs SRE, incident response and observability. His on-call work is known.", Talks: []Talk{{Title: "SLOs"}}}, s.now())
	s.addToPool(src, PoolSpeaker{Name: "Wen Strong", Bio: "She leads SRE and incident response. Her observability talks are known.", Talks: []Talk{{Title: "Chaos"}}}, s.now())
	s.addToPool(src, PoolSpeaker{Name: "Wia Weak", Bio: "She writes about kubernetes. Her blog is fun."}, s.now())
	tp, _ := s.topic("sre")
	cs := s.candidates(tp, s.now())
	s.mu.Unlock()
	if len(cs) != 3 || cs[0]["name"] != "Wen Strong" || cs[1]["name"] != "Hal Strong" || cs[2]["name"] != "Wia Weak" {
		t.Fatalf("order = %v %v %v", cs[0]["name"], cs[1]["name"], cs[2]["name"])
	}
	board := womenFirst([]*Card{{Name: "A", Match: 90}, {Name: "B", Match: 60, Pronouns: "she"}, {Name: "C", Match: 80}})
	if board[0].Name != "B" || board[1].Name != "A" || board[2].Name != "C" {
		t.Fatalf("board order = %s %s %s", board[0].Name, board[1].Name, board[2].Name)
	}
}

func TestNameSignal(t *testing.T) {
	for name, want := range map[string]bool{"Maria Garcia": true, "Agnieszka Nowak": true, "Andrea Rossi": false, "Kim Lee": false, "Mark Smith": false, "Madonna": false} {
		if likelyWoman(name) != want {
			t.Errorf("likelyWoman(%q) = %v", name, !want)
		}
	}
	s, _ := testServer(t)
	s.mu.Lock()
	s.addToPool(&Source{Event: "X 2026"}, PoolSpeaker{Name: "Julia Example", Bio: "Works on SRE."}, s.now())
	s.addToPool(&Source{Event: "X 2026"}, PoolSpeaker{Name: "Julia Stated", Bio: "He runs SRE. His team is great."}, s.now())
	s.mu.Unlock()
	if s.pool["julia example"].Pronouns != "she" || s.pool["julia stated"].Pronouns != "he" {
		t.Fatal("the name counts only when nothing is published")
	}
}
