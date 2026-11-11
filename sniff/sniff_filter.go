package sniff

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Sniff's quiet filter: a question (or a name, or a rating's comment) with
// a slur, a sexual word or an insult never reaches the speaker. The asker
// is thanked as usual and never told. Words match whole, after folding
// accents, leetspeak (n1gg3r) and stretched letters (fuuuck); a few that
// are never innocent also match inside other words or spelled out
// (n i g g e r). Words with everyday tech or English meanings (kill, dump,
// naked pods, niggle, Scunthorpe, stupid question) stay allowed.

// harmfulWords match a whole word.
var harmfulWords = stretchSet(
	// slurs
	"nigger", "niggers", "nigga", "niggas", "coon", "coons", "spic", "spics", "chink", "chinks",
	"gook", "gooks", "kike", "kikes", "wetback", "wetbacks", "beaner", "beaners", "paki", "pakis", "towelhead", "raghead",
	"fag", "fags", "faggot", "faggots", "fagot", "dyke", "dykes", "tranny", "trannies", "shemale",
	"retard", "retards", "retarded", "tard", "spaz", "mongoloid", "heil", "kys",
	// sexual
	"porn", "porno", "pornhub", "horny", "boobs", "boobies", "tits", "titties", "titty", "pussy", "pussies",
	"cock", "cocks", "dickhead", "dickheads", "blowjob", "handjob", "cumshot", "dildo", "penis", "vagina",
	"anal", "rape", "raped", "rapist", "raping", "slut", "sluts", "slutty", "whore", "whores", "hooker", "milf", "bdsm",
	"orgasm", "masturbate", "masturbating", "jerkoff", "wank", "wanker", "wankers", "nudes", "sexting", "onlyfans",
	// insults and profanity
	"bitch", "bitches", "bitchy", "bastard", "bastards", "asshole", "assholes", "arsehole", "ass", "arse",
	"shit", "shits", "shitty", "bullshit", "shithead", "shithole", "twat", "twats", "prick", "pricks",
	"idiot", "idiots", "idiotic", "moron", "morons", "moronic", "imbecile", "dumbass", "dipshit", "jackass",
	"scumbag", "piss", "pissed", "bollocks", "douche", "douchebag", "stfu", "gtfo", "wtf", "fck", "fuk",
	// misspelt on purpose
	"phuck", "phuk", "phucking", "fuq", "fuqing", "fukin", "fukking", "biatch", "beotch", "biotch", "kunt", "coont", "cuntt", "shyt", "azz", "azzhole", "phag", "fagg", "niga", "nigah", "nigguh",
	"fatso", "fatass", "pathetic", "worthless", "clueless", "incompetent", "sucks",
)

// harmfulParts match inside a word, or spelled out across words.
var harmfulParts = stretchList("nigger", "nigga", "faggot", "fuck", "motherf", "cocksuck", "cunt")

// innocentWords contain a harmful part but aren't harmful.
var innocentWords = map[string]bool{"scunthorpe": true}

// stretchy matches a word with any of its letters held: fuuuck, shiiit.
// Letters can repeat but never go missing, so Niger is not n-gger.
func stretchy(w string) string {
	var b strings.Builder
	for _, r := range w {
		b.WriteString(regexp.QuoteMeta(string(r)) + "+")
	}
	return b.String()
}

func stretchList(words ...string) []*regexp.Regexp {
	var out []*regexp.Regexp
	for _, w := range words {
		out = append(out, regexp.MustCompile(stretchy(w)))
	}
	return out
}

// stretchSet is the whole words by their squeezed form, each with its
// stretchy pattern.
func stretchSet(words ...string) map[string][]*regexp.Regexp {
	m := map[string][]*regexp.Regexp{}
	for _, w := range words {
		k := squeeze(w)
		m[k] = append(m[k], regexp.MustCompile("^"+stretchy(w)+"$"))
	}
	return m
}

func badWord(w string) bool {
	for _, re := range harmfulWords[squeeze(w)] {
		if re.MatchString(w) {
			return true
		}
	}
	return false
}

func badPart(w string) bool {
	for _, re := range harmfulParts {
		if re.MatchString(w) {
			return true
		}
	}
	return false
}

// leet folds look-alike digits and signs to letters.
var leet = strings.NewReplacer("0", "o", "1", "i", "3", "e", "4", "a", "5", "s", "7", "t", "@", "a", "$", "s", "!", "i", "|", "i", "€", "e")

// foldText is text as the filter compares it: lower case, no accents,
// leetspeak read as letters.
func foldText(s string) string {
	s = norm.NFD.String(strings.ToLower(s))
	var b strings.Builder
	for _, r := range s {
		if unicode.Is(unicode.Mn, r) {
			continue // the accent of an é
		}
		b.WriteRune(r)
	}
	return leet.Replace(b.String())
}

// squeeze takes stretched letters down to one: fuuuck -> fuck.
func squeeze(w string) string {
	var b strings.Builder
	var last rune
	for _, r := range w {
		if r != last {
			b.WriteRune(r)
		}
		last = r
	}
	return b.String()
}

// harmful says whether text has a word the speaker never needs to see.
func harmful(text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	words := strings.FieldsFunc(foldText(text), func(r rune) bool { return !unicode.IsLetter(r) })
	for _, w := range words {
		if !innocentWords[w] && (badWord(w) || badPart(w)) {
			return true
		}
	}
	// Spelled out: "n i g g e r", "f.u.c.k". Only runs of single letters
	// are joined, so ordinary words never run into each other.
	var run strings.Builder
	check := func() bool {
		s := run.String()
		run.Reset()
		return len(s) >= 3 && (badWord(s) || badPart(s))
	}
	for _, w := range words {
		if len([]rune(w)) == 1 {
			run.WriteString(w)
			continue
		}
		if check() {
			return true
		}
	}
	if check() {
		return true
	}
	return splitWord(words) || maskedWord(text)
}

// maskSigns stand for a letter someone left out: "wh@re", "sh*t", "c*nt".
const maskSigns = "@*#%?_"

// maskedWord tries each sign between letters as every vowel (two signs a
// word at most). Only words with such a sign are tried, so "where" never
// becomes "whore".
func maskedWord(text string) bool {
	for _, tok := range strings.Fields(strings.ToLower(text)) {
		r := []rune(tok)
		var at []int
		for i, c := range r {
			if strings.ContainsRune(maskSigns, c) && i > 0 && i < len(r)-1 && unicode.IsLetter(r[i-1]) && unicode.IsLetter(r[i+1]) {
				at = append(at, i)
			}
		}
		if len(at) == 0 || len(at) > 2 {
			continue
		}
		var try func(k int) bool
		try = func(k int) bool {
			if k == len(at) {
				w := strings.Map(func(c rune) rune {
					if unicode.IsLetter(c) {
						return c
					}
					return -1
				}, foldText(string(r)))
				return len([]rune(w)) >= 3 && !innocentWords[w] && (badWord(w) || badPart(w))
			}
			for _, v := range "aeiou" {
				r[at[k]] = v
				if try(k + 1) {
					return true
				}
			}
			return false
		}
		if try(0) {
			return true
		}
	}
	return false
}

// splitWord catches a word cut into pieces ("Nigg er", "f uck", "sh it"):
// up to three neighbouring short pieces (four letters at most each) are
// joined and checked again. Longer words are never glued together, so
// "picnic until" stays a picnic.
func splitWord(words []string) bool {
	for i := range words {
		joined := ""
		for j := i; j < len(words) && j < i+3; j++ {
			if len([]rune(words[j])) > 4 {
				break
			}
			joined += words[j]
			if j > i && !innocentWords[joined] && (badWord(joined) || badPart(joined)) {
				return true
			}
		}
	}
	return false
}
