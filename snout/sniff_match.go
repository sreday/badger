package main

import (
	"strings"
	"sync"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Sniff names: someone who types their name under a question is matched to
// the event's Luma guest list (and the speakers), so the speaker sees "Peter
// Smith, Acme" when exactly one Peter registered. Several Peters, or nobody
// by that name: the speaker sees what was typed. The person asking never
// sees the result, so the audience page tells nobody who registered.

// Who is the person behind a named post, as the speaker sees them.
type Who struct {
	Guest    string `json:"guest,omitempty"` // gst-..., empty for a speaker from the schedule
	Name     string `json:"name"`
	Title    string `json:"title,omitempty"`
	Company  string `json:"company,omitempty"`
	LinkedIn string `json:"linkedin,omitempty"`
}

// foldName lowercases, strips accents (José -> jose, Łukasz -> lukasz) and
// turns anything that isn't a letter or digit into single spaces.
func foldName(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	out, _, err := transform.String(t, s)
	if err != nil {
		out = s
	}
	out = strings.NewReplacer("ł", "l", "Ł", "l", "ø", "o", "Ø", "o", "ß", "ss", "đ", "d", "Đ", "d", "æ", "ae", "Æ", "ae", "ı", "i").Replace(out)
	return normName(out)
}

// nameIndex is a guest list by folded first name and full name.
type nameIndex struct {
	first map[string][]*Who
	full  map[string][]*Who
}

func (n *nameIndex) add(first, last string, w *Who) {
	f := foldName(first)
	if f == "" {
		return
	}
	full := strings.TrimSpace(f + " " + foldName(last))
	for _, have := range n.full[full] {
		if foldName(have.Name) == foldName(w.Name) {
			return // a speaker who also registered: one person, not two
		}
	}
	// Only the first word of a first name counts as "the first name"
	// (Mary Jane types Mary), the full name keeps all of it.
	n.first[strings.Fields(f)[0]] = append(n.first[strings.Fields(f)[0]], w)
	n.full[full] = append(n.full[full], w)
}

var (
	nameIxMu    sync.Mutex
	nameIxCache = map[*GuestIndex]map[string]*nameIndex{} // guest index -> speakers key -> names
)

// namesFor builds (once per guest list and speaker list) the name index of
// an event: approved guests first, then the speakers on the schedule who
// aren't registered.
func namesFor(ix *GuestIndex, speakers []Who) *nameIndex {
	var key strings.Builder
	for _, s := range speakers {
		key.WriteString(s.Name)
		key.WriteByte('|')
	}
	nameIxMu.Lock()
	defer nameIxMu.Unlock()
	if m := nameIxCache[ix]; m != nil {
		if n := m[key.String()]; n != nil {
			return n
		}
	}
	n := &nameIndex{first: map[string][]*Who{}, full: map[string][]*Who{}}
	for _, g := range ix.Guests() {
		first, last := g.FirstName, g.LastName
		if first == "" {
			first, last, _ = strings.Cut(strings.TrimSpace(g.Name), " ")
		}
		name := strings.TrimSpace(g.Name)
		if name == "" {
			name = strings.TrimSpace(first + " " + last)
		}
		n.add(first, last, &Who{Guest: g.ID, Name: name, Title: shownField(g.Title), Company: shownField(g.Company), LinkedIn: g.LinkedIn})
	}
	for i := range speakers {
		s := speakers[i]
		first, last, _ := strings.Cut(strings.TrimSpace(s.Name), " ")
		n.add(first, last, &s)
	}
	if nameIxCache[ix] == nil {
		if len(nameIxCache) > 32 { // old guest lists: let them go
			nameIxCache = map[*GuestIndex]map[string]*nameIndex{}
		}
		nameIxCache[ix] = map[string]*nameIndex{}
	}
	nameIxCache[ix][key.String()] = n
	return n
}

// shownField drops Luma's "403 not found" placeholder.
func shownField(s string) string {
	if strings.EqualFold(strings.TrimSpace(s), "403 not found") {
		return ""
	}
	return strings.TrimSpace(s)
}

// matchName finds the one person a typed name can only mean, or nil.
//
//	"Peter"          the only Peter at the event
//	"Peter Smith"    the only Peter Smith (middle names skipped: "Peter J Smith")
//	"Peter S"        the only Peter whose last name starts with S
func matchName(typed string, ix *GuestIndex, speakers []Who) *Who {
	words := strings.Fields(foldName(typed))
	if len(words) == 0 || len(words) > 5 {
		return nil
	}
	n := namesFor(ix, speakers)
	one := func(ws []*Who) *Who {
		if len(ws) == 1 {
			w := *ws[0]
			return &w
		}
		return nil
	}
	if len(words) == 1 {
		return one(n.first[words[0]])
	}
	if w := one(n.full[strings.Join(words, " ")]); w != nil {
		return w
	}
	// First name plus last name (middle names skipped either side), or a
	// last initial: "Peter Smith" for Peter J Smith, "Peter S".
	last := words[len(words)-1]
	var hits []*Who
	for _, w := range n.first[words[0]] {
		parts := strings.Fields(foldName(w.Name))
		if len(parts) < 2 {
			continue
		}
		if wl := parts[len(parts)-1]; wl == last || len(last) == 1 && strings.HasPrefix(wl, last) {
			hits = append(hits, w)
		}
	}
	return one(hits)
}
