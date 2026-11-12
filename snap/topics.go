package snap

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Topics (Marek 2026-11-12): "Snap needs to group related threads, listed
// as one item. ING Poland asked me for an official offer in one email, I
// said yes and sent it in a separate thread, but I can see many items
// claiming the ball is with me." Watch out for false friends: the same
// person on SREday and on Conf42, or hosting vs sponsoring, are separate.
//
// How it works:
//   - Claude gives every thread a topic key ("ing poland / sponsorship /
//     sreday warsaw 2026"); same people, different matter = different key.
//   - When Claude reads a thread it also sees the other threads with the
//     same work domain or company (relatedBlock), so it judges the topic by
//     its newest state, not the one thread.
//   - When a related thread moves after Claude's last read of a flagged
//     one, that flagged one goes back to Claude (RelatedAt, Pending).
//   - The page shows one card per topic: the thread Claude read last
//     speaks for the topic. If that read says it's settled, the topic's
//     older "ball with you" cards stay hidden. Done, Dead end, Snooze and
//     Move apply to the whole topic.

const relatedWindow = 365 * 24 * time.Hour

func normTopic(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

// related: the other threads with the same people's company (work domain,
// not the team's or a freemail one) or the same company name; newest first.
func (s *Server) related(t Thread, all []Thread, n int) []Thread {
	team := map[string]bool{}
	for _, d := range s.cfg.TeamDomains {
		team[d] = true
	}
	doms := map[string]bool{}
	for _, d := range workDomains(t.People) {
		if !team[d] {
			doms[d] = true
		}
	}
	company := normTopic(t.Company)
	var out []Thread
	for _, o := range all {
		if o.ID == t.ID || o.LastAt.IsZero() || t.LastAt.Sub(o.LastAt) > relatedWindow || o.LastAt.Sub(t.LastAt) > relatedWindow {
			continue
		}
		hit := company != "" && normTopic(o.Company) == company
		for _, d := range workDomains(o.People) {
			hit = hit || doms[d]
		}
		if hit {
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastAt.After(out[j].LastAt) })
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// relatedBlock: the other threads, for the excerpt Claude reads.
func (s *Server) relatedBlock(t Thread, all []Thread) string {
	rel := s.related(t, all, 10)
	if len(rel) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("OTHER THREADS WITH THE SAME PEOPLE OR COMPANY (newest first; judge this thread's topic by its newest state):\n")
	for _, o := range rel {
		from := o.LastFrom
		if from == "me" {
			from = "Mark"
		}
		fmt.Fprintf(&b, "- %s %q (thread %s), last message from %s", o.LastAt.In(s.tz).Format("2 Jan 2006"), o.Subject, o.ID, from)
		if o.Topic != "" {
			fmt.Fprintf(&b, "; topic %q", o.Topic)
		}
		if o.ByClaude() && o.Summary != "" {
			fmt.Fprintf(&b, "; Snap's read: %s", clip(o.Summary, 220))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// markRelated notes, on every open loose end, when its related threads last
// moved; a move after Claude's last read sends it back to Claude.
func (s *Server) markRelated() {
	all := s.store.All()
	newest := map[string]time.Time{}
	for _, t := range all {
		if !t.Flagged() {
			continue
		}
		if rel := s.related(t, all, 1); len(rel) > 0 {
			newest[t.ID] = rel[0].LastAt
		}
	}
	s.store.UpdateAll(func(t *Thread) {
		if at, ok := newest[t.ID]; ok {
			t.RelatedAt = at
		}
	})
}

// topicLeads: per topic, the thread Claude read last; it speaks for the
// topic on the page.
func topicLeads(all []Thread) map[string]Thread {
	leads := map[string]Thread{}
	for _, t := range all {
		k := normTopic(t.Topic)
		if k == "" || !t.ByClaude() {
			continue
		}
		l, ok := leads[k]
		if !ok || t.AnalyzedAt.After(l.AnalyzedAt) || (t.AnalyzedAt.Equal(l.AnalyzedAt) && t.LastAt.After(l.LastAt)) {
			leads[k] = t
		}
	}
	return leads
}

// Sibling is another email of the same topic, listed on the card.
type Sibling struct {
	ID, Subject, LastFrom, Gmail string
	LastAt                       time.Time
}

func (s *Server) siblings(t Thread, all []Thread) []Sibling {
	k := normTopic(t.Topic)
	if k == "" {
		return nil
	}
	var out []Sibling
	for _, o := range all {
		if o.ID == t.ID || normTopic(o.Topic) != k {
			continue
		}
		from := o.LastFrom
		if from == "me" {
			from = "you"
		}
		out = append(out, Sibling{ID: o.ID, Subject: o.Subject, LastFrom: from, LastAt: o.LastAt, Gmail: "https://mail.google.com/mail/u/?authuser=" + s.cfg.Account + "#all/" + o.ID})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastAt.After(out[j].LastAt) })
	return out
}

// updateTopic applies Marek's choice (Done, Dead end, Snooze, Move) to the
// thread and every thread on its topic.
func (s *Server) updateTopic(id string, fn func(t *Thread)) {
	t, ok := s.store.Get(id)
	s.store.Update(id, fn)
	k := normTopic(t.Topic)
	if !ok || k == "" {
		return
	}
	for _, o := range s.store.All() {
		if o.ID != id && normTopic(o.Topic) == k {
			s.store.Update(o.ID, fn)
		}
	}
}
