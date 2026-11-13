package snap

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Marek sorts by hand (2026-11-12: "allow me to manually drag threads
// between tabs, so that claude can filter better the upcoming ones"):
// dragging a thread onto a tab moves it there for good, and every move is
// a lesson for Claude. The latest moves go out with the queue's
// instructions, and a thread from a domain Marek has sorted carries that
// hint in its excerpt.

// Move is one thread Marek put in another tab.
type Move struct {
	Subject string    `json:"subject"`
	Company string    `json:"company,omitempty"`
	Domains []string  `json:"domains,omitempty"`
	From    string    `json:"from"`
	To      string    `json:"to"`
	At      time.Time `json:"at"`
}

const keepMoves = 40

func validCategory(c string) bool {
	for _, k := range categories {
		if k.Key == c {
			return true
		}
	}
	return false
}

// freemail domains say nothing about who someone works for.
var freemail = map[string]bool{"gmail.com": true, "googlemail.com": true, "outlook.com": true, "hotmail.com": true, "live.com": true,
	"yahoo.com": true, "icloud.com": true, "me.com": true, "proton.me": true, "protonmail.com": true, "aol.com": true, "gmx.com": true, "wp.pl": true, "o2.pl": true, "onet.pl": true}

func workDomains(people []Person) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range people {
		d := domainOf(p.Email)
		if d == "" || freemail[d] || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

func (s *Server) loadMoves() {
	if data, err := os.ReadFile(filepath.Join(s.dir, "moves.json")); err == nil {
		json.Unmarshal(data, &s.moves)
	}
}

// handleMove: POST /admin/thread/{id}/move with cat=sponsors|speakers|events.
func (s *Server) handleMove(w http.ResponseWriter, r *http.Request) {
	id, ok := s.known(w, r)
	if !ok {
		return
	}
	to := r.FormValue("cat")
	if !validCategory(to) {
		http.Error(w, "no such tab", http.StatusBadRequest)
		return
	}
	var mv Move
	s.updateTopic(id, func(t *Thread) {
		from := t.Category
		if from == "" {
			from = "events"
		}
		mv = Move{Subject: t.Subject, Company: t.Company, Domains: workDomains(t.People), From: from, To: to, At: s.now()}
		t.Category, t.MovedTo = to, to
	})
	if mv.From != to {
		s.mu.Lock()
		s.moves = append(s.moves, mv)
		if len(s.moves) > keepMoves {
			s.moves = s.moves[len(s.moves)-keepMoves:]
		}
		data, _ := json.Marshal(s.moves)
		s.mu.Unlock()
		os.WriteFile(filepath.Join(s.dir, "moves.json"), data, 0o600)
	}
	back(w, r)
}

// sortingLessons is Marek's sorting, for the instructions ("" when none).
func (s *Server) sortingLessons() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.moves) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nMarek's own sorting. He moved these threads to another tab by hand. Learn from them: sort threads like these the way he did, and his choice wins over your guess.\n")
	for i := len(s.moves) - 1; i >= 0; i-- {
		m := s.moves[i]
		if !validCategory(m.From) || !validCategory(m.To) {
			continue // moves in or out of the old Other tab teach nothing now
		}
		fmt.Fprintf(&b, "- %q", m.Subject)
		if m.Company != "" {
			fmt.Fprintf(&b, " (%s)", m.Company)
		}
		if len(m.Domains) > 0 {
			fmt.Fprintf(&b, " [%s]", strings.Join(m.Domains, ", "))
		}
		fmt.Fprintf(&b, ": %s -> %s\n", m.From, m.To)
	}
	return b.String()
}

// domainHintLocked: where Marek last filed mail from these people's
// companies. s.mu is held.
func (s *Server) domainHintLocked(people []Person) string {
	doms := workDomains(people)
	if len(doms) == 0 {
		return ""
	}
	for i := len(s.moves) - 1; i >= 0; i-- {
		if !validCategory(s.moves[i].To) {
			continue
		}
		for _, d := range s.moves[i].Domains {
			for _, want := range doms {
				if d == want {
					return fmt.Sprintf("Hint: Marek files threads with %s under %q.\n", d, s.moves[i].To)
				}
			}
		}
	}
	return ""
}
