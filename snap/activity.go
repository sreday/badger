package snap

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

// The activity pill in the header (Marek 2026-11-12): "Nothing happening" in
// grey, or what's in the making right now ("Drafting email to nudgebee")
// with a fill that grows as it goes.

// Activity is one thing in the making.
type Activity struct {
	ID      string    `json:"id"`             // the thread, for drafts
	Label   string    `json:"label"`          // "Drafting email to nudgebee"
	Done    string    `json:"done,omitempty"` // what it says when it finishes
	Started time.Time `json:"started"`
	Expect  int       `json:"expect"` // seconds it usually takes; 0 = no estimate
}

const draftExpect = 70 // seconds from click to draft in Gmail, as measured

func (s *Server) activities(now time.Time) []Activity {
	var out []Activity
	for _, t := range s.store.All() {
		if t.Enrich.Working(now) {
			out = append(out, Activity{ID: t.Enrich.ID, Label: "Enriching the thread", Done: "Thread enriched", Started: t.Enrich.At, Expect: enrichExpect})
		}
		if t.Fix.Saving(now) {
			out = append(out, Activity{ID: t.ID + "-fix", Label: "Saving your corrected draft", Done: "Draft corrected in Gmail", Started: t.Fix.At, Expect: fixExpect})
		}
		if !t.Drafting(now) {
			continue
		}
		who := t.Company
		if who == "" && len(t.People) > 0 {
			who = t.People[0].Name
			if who == "" {
				who = t.People[0].Email
			} else if f := strings.Fields(who); len(f) > 0 {
				who = f[0]
			}
		}
		label := "Drafting email"
		if who != "" {
			label += " to " + who
		}
		out = append(out, Activity{ID: t.ID, Label: label, Done: "Draft ready in Gmail", Started: t.DraftAsked, Expect: draftExpect})
	}
	for _, c := range s.recentComposes(now) {
		if !c.Working(now) {
			continue
		}
		label := c.Brief
		if r := []rune(label); len(r) > 34 {
			label = strings.TrimSpace(string(r[:33])) + "…"
		}
		out = append(out, Activity{ID: c.ID, Label: "Drafting: " + label, Done: "Draft ready in Gmail", Started: c.Asked, Expect: composeExpect})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	s.mu.Lock()
	running, runStart := s.running, s.runStart
	s.mu.Unlock()
	if running {
		out = append(out, Activity{Label: "Reading Gmail", Done: "Gmail read", Started: runStart})
	}
	return out
}

// handleActivity: GET /admin/activity, polled by the header pill.
func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	items := s.activities(s.now())
	if items == nil {
		items = []Activity{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"now": s.now(), "items": items})
}

// Seg is one arc of the choice wheel.
type Seg struct{ Class, D string }

// wheelSegs splits the ring equally between the options present (Marek
// 2026-11-12: two options get half each, three a third, four a quarter),
// clockwise from the top: the recommended one, the right option, Other,
// the left option.
func wheelSegs(left, right, other bool) []Seg {
	classes := []string{"seg0"}
	if right {
		classes = append(classes, "seg2")
	}
	if other {
		classes = append(classes, "seg3")
	}
	if left {
		classes = append(classes, "seg1")
	}
	n := float64(len(classes))
	span, gap := 360/n, 14.0
	if n == 1 {
		gap = 0.5
	}
	var out []Seg
	for i, c := range classes {
		mid := -90 + float64(i)*span
		a, b := (mid-span/2+gap/2)*math.Pi/180, (mid+span/2-gap/2)*math.Pi/180
		large := 0
		if span-gap > 180 {
			large = 1
		}
		out = append(out, Seg{c, fmt.Sprintf("M%.2f %.2f A80 80 0 %d 1 %.2f %.2f", 100+80*math.Cos(a), 100+80*math.Sin(a), large, 100+80*math.Cos(b), 100+80*math.Sin(b))})
	}
	return out
}
