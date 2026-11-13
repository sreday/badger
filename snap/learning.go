package snap

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Learning from Marek (2026-11-12): "make claude analyse all 'Other' and
// 'draft new with claude' - so that the recommendations get more context on
// what to do. Ideally with time I will use Other less and less."
//
// Every brief he types (Other on a thread, or the Draft with Claude line)
// is kept with what Snap had offered at the time, and goes out with each
// queue, so Claude learns which plays he actually picks. A thread's own
// briefs also go into its excerpt.
//
// Claude also runs a quick background check on each company it flags
// (where they are, how big, what they sell) and posts it back; Snap keeps
// it per company and hands it over next time, so the options fit where the
// company really is (one office in NYC: propose NYC, not SF).

const (
	keepBriefs = 40
	factsFresh = 120 * 24 * time.Hour // re-check a company after about 4 months
)

// Brief is one instruction Marek typed for Claude.
type Brief struct {
	At       time.Time `json:"at"`
	Kind     string    `json:"kind"` // "other" (on a thread) or "compose" (Draft with Claude)
	Thread   string    `json:"thread,omitempty"`
	Subject  string    `json:"subject,omitempty"`
	Company  string    `json:"company,omitempty"`
	Category string    `json:"category,omitempty"`
	Offered  []string  `json:"offered,omitempty"` // Snap's options he passed over
	Text     string    `json:"text"`
}

// Facts is Claude's background check on one company.
type Facts struct {
	Company string    `json:"company"`
	Facts   string    `json:"facts"`
	At      time.Time `json:"at"`
}

func (s *Server) loadLearning() {
	if data, err := os.ReadFile(filepath.Join(s.dir, "briefs.json")); err == nil {
		json.Unmarshal(data, &s.briefs)
	}
	s.facts = map[string]Facts{}
	if data, err := os.ReadFile(filepath.Join(s.dir, "companies.json")); err == nil {
		json.Unmarshal(data, &s.facts)
	}
}

func writeJSON(dir, name string, v any) {
	data, _ := json.MarshalIndent(v, "", "  ")
	tmp := filepath.Join(dir, name+".tmp")
	if os.WriteFile(tmp, data, 0o600) == nil {
		os.Rename(tmp, filepath.Join(dir, name))
	}
}

// recordBrief keeps one brief; t is the thread it was typed on, if any.
func (s *Server) recordBrief(kind, text string, t *Thread) {
	b := Brief{At: s.now(), Kind: kind, Text: text}
	if t != nil {
		b.Thread, b.Subject, b.Company, b.Category = t.ID, t.Subject, t.Company, t.Category
		for _, a := range t.Actions {
			b.Offered = append(b.Offered, a.Title)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.briefs = append(s.briefs, b)
	if len(s.briefs) > keepBriefs {
		s.briefs = s.briefs[len(s.briefs)-keepBriefs:]
	}
	writeJSON(s.dir, "briefs.json", s.briefs)
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// briefLessons: the instructions' section on what Marek typed himself.
func (s *Server) briefLessons() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.briefs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nWHAT MARK ASKED FOR HIMSELF (newest first). When none of your options fitted, he typed his own brief (\"Other\" on a thread), or he asked Claude for a new email from scratch. These are the plays he actually wants: learn his instincts and offer moves like these yourself, so he needs to type less and less.\n")
	for i := len(s.briefs) - 1; i >= 0 && i >= len(s.briefs)-briefsShown; i-- {
		br := s.briefs[i]
		day := br.At.In(s.tz).Format("2 Jan")
		if br.Kind == "question" {
			fmt.Fprintf(&b, "- %s, %s: the card didn't say enough, he had to ask: %q\n", day, clip(br.Company+" "+br.Subject, 70), clip(br.Text, 300))
			continue
		}
		if br.Kind == "compose" {
			fmt.Fprintf(&b, "- %s, from scratch: %q\n", day, clip(br.Text, 300))
			continue
		}
		who := br.Company
		if who == "" {
			who = clip(br.Subject, 60)
		}
		fmt.Fprintf(&b, "- %s, %s (%s)", day, who, br.Category)
		if len(br.Offered) > 0 {
			fmt.Fprintf(&b, ", you offered: %s", strings.Join(br.Offered, "; "))
		}
		fmt.Fprintf(&b, ". He wrote instead: %q\n", clip(br.Text, 300))
	}
	return b.String()
}

// threadContextLocked: the extra lines for one thread's excerpt (s.mu held).
func (s *Server) threadContextLocked(t Thread) string {
	var b strings.Builder
	others := 0
	for _, br := range s.briefs {
		if br.Thread != t.ID {
			continue
		}
		if br.Kind == "question" {
			fmt.Fprintf(&b, "Mark asked about this thread (%s): %q\n", br.At.In(s.tz).Format("2 Jan"), clip(br.Text, 300))
			continue
		}
		others++
		fmt.Fprintf(&b, "Mark's own brief on this thread (%s): %q\n", br.At.In(s.tz).Format("2 Jan"), clip(br.Text, 300))
	}
	if others >= 2 {
		fmt.Fprintf(&b, "Mark passed over your options %d times on this thread and wrote his own brief each time: your read of it is off. Rethink the options from what he wrote.\n", others)
	}
	for _, c := range t.Context {
		fmt.Fprintf(&b, "Context Mark confirmed (Quick question): %s\n", c)
	}
	if f, ok := s.facts[companyKey(t.Company)]; ok && f.Facts != "" {
		fmt.Fprintf(&b, "Known about %s (checked %s): %s\n", f.Company, f.At.In(s.tz).Format("2 Jan 2006"), f.Facts)
		if s.now().Sub(f.At) > factsFresh {
			b.WriteString("(That check is old: refresh it if you flag this thread.)\n")
		}
	} else if s.calls != nil && t.Company != "" {
		// Snap has no background on them: maybe a call left some (Snoop, through the bridge).
		if known := s.calls.CompanyFacts(t.Company); known != "" {
			fmt.Fprintf(&b, "Known about %s (from a call, via Snoop): %s\n", t.Company, known)
		}
	}
	return b.String()
}

func companyKey(c string) string { return strings.ToLower(strings.Join(strings.Fields(c), " ")) }

// saveFacts keeps a company's background check from an answer.
func (s *Server) saveFacts(company, facts string) {
	company, facts = strings.TrimSpace(company), strings.TrimSpace(facts)
	if company == "" || facts == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.facts[companyKey(company)] = Facts{Company: company, Facts: clip(facts, 600), At: s.now()}
	writeJSON(s.dir, "companies.json", s.facts)
}

// eventsList: the upcoming events, for picking a better event or city.
func (s *Server) eventsList(now time.Time) string {
	list, _ := s.lineups.Get(now)
	var rows []string
	sort.Slice(list, func(i, j int) bool { return list[i].Start.Before(list[j].Start) })
	for _, l := range list {
		if l.Start.IsZero() || l.Brand == "" {
			continue
		}
		rows = append(rows, fmt.Sprintf("- %s %s, %s", l.Brand, l.City, l.Start.In(s.tz).Format("2 Jan 2006")))
	}
	if len(rows) == 0 {
		return ""
	}
	return "\n\nUPCOMING EVENTS (from the websites; Conf42 events are online). Use these when you propose another event or city, and pick ones near where the company operates:\n" + strings.Join(rows, "\n") + "\n"
}

// rereadFlagged sends every open loose end back to Claude once, so the
// options get the new rules (version marks which rules).
func (s *Server) rereadFlagged(version string) {
	mark := filepath.Join(s.dir, "reread-"+version)
	if _, err := os.Stat(mark); err == nil {
		return
	}
	s.store.UpdateAll(func(t *Thread) {
		if t.Flagged() {
			t.ClaudeHistory = ""
		}
	})
	os.WriteFile(mark, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600)
}

// recheckBounces has the next sync read again, once, every open thread
// where Mark wrote last, so bounces from before Snap knew them are found.
func (s *Server) recheckBounces(version string) {
	mark := filepath.Join(s.dir, "recheck-"+version)
	if _, err := os.Stat(mark); err == nil {
		return
	}
	s.store.UpdateAll(func(t *Thread) {
		if t.LastFrom == "me" && !t.Done && !t.DeadEnd {
			t.HistoryID = ""
		}
	})
	os.WriteFile(mark, []byte(time.Now().UTC().Format(time.RFC3339)), 0o600)
}
