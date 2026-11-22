package snore

import (
	"regexp"
	"sort"
	"strings"
)

// Women first: among relevant speakers, the ones whose published words say
// "she" go first. Only what the speaker or the event publishes counts: a
// stated "she/her" or the pronouns their bio is written in. Never a guess
// from a name or a photo.

var statedPronouns = regexp.MustCompile(`(?i)\b(she|he|they)\s*/\s*(her|hers|him|his|them|theirs)\b`)
var pronounWord = regexp.MustCompile(`(?i)\b(she|her|hers|herself|he|him|his|himself)\b`)

// pronounsOf: "she", "he", "they", or "" when the text does not say.
func pronounsOf(text string) string {
	if m := statedPronouns.FindStringSubmatch(text); m != nil {
		return strings.ToLower(m[1])
	}
	she, he := 0, 0
	for _, w := range pronounWord.FindAllString(text, -1) {
		switch strings.ToLower(w) {
		case "she", "her", "hers", "herself":
			she++
		case "he", "him", "his", "himself":
			he++
		}
	}
	switch {
	case she >= 2 && she > 2*he:
		return "she"
	case he >= 2 && he > 2*she:
		return "he"
	}
	return ""
}

// womenFirst orders cards for the board: women first, each group as it was
// (best match first). Which cards make the board stays a matter of match.
func womenFirst(cs []*Card) []*Card {
	out := append([]*Card(nil), cs...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Pronouns == "she" && out[j].Pronouns != "she" })
	return out
}

// stampPronouns fills a card's pronouns from the speaker's published bio
// in the pool when Claude gave none (lock held).
func (s *Server) stampPronouns(c *Card) {
	if c.Pronouns != "" {
		return
	}
	if p := s.pool[c.Key]; p != nil {
		c.Pronouns = p.Pronouns
	}
}
