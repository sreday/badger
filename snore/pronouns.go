package snore

import (
	"regexp"
	"sort"
	"strings"
)

// Women first: among relevant speakers, women go first. What the speaker or
// the event publishes wins (a stated "she/her", the pronouns a bio is written
// in); otherwise a common women's first name, or Claude's judgement from the
// name and photo. It only orders cards and candidates, it is never shown.

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
	if c.Pronouns == "" && likelyWoman(c.Name) {
		c.Pronouns = "she"
	}
}

// womenNames: common women's first names across the languages our speakers
// write in. Names used for men too (Andrea, Kim, Sam, Alex...) are left out.
// A match only orders candidates; it never shows on a card.
var womenNames = map[string]bool{}

func init() {
	for _, n := range strings.Fields(`
abby abigail ada adriana agata agata agnes agnieszka aileen aisha aleksandra alexandra alice alicia alina alison
allison amanda amber amelia amy ana anastasia anca anja ann anna annabel anne annette annie antonia ania anya
april ariana ariel asha astrid aurelie aurora barbara beata beatrice beatriz becky belinda bella beth bethany
bianca birgit bogna bozena brenda bridget brigitte britta brittany camila camille carla carmen carol caroline
carolyn cassandra catalina catherine cecilia celine charlotte chelsea chiara christina christine claire clara
claudia colleen constance cristina cynthia dagmara daniela danielle daria debora deborah denise diana dominika
donna dorota dorothy edyta elena eleanor elif elisa elisabeth elise eliza elizabeth ella ellen elsa emily emma
erica erika esther eva evelyn ewa fatima federica fernanda fiona florence francesca gabriela gabriella gabrielle
georgia gina giulia gloria grace grazyna greta hanna hannah heather heidi helen helena helga holly ilona ines
ingrid irene irina iris isabel isabella isabelle iwona izabela jacqueline jana jane janet janina jasmine jennifer
jenny jessica jill joan joanna jocelyn johanna josephine joyce judith julia juliana julie justyna karen karina
karolina katarzyna kate katharina katherine kathleen kathryn katie katrin katya kayla kelly klara kristen kristina
krystyna laura lauren lea leah lena lidia lila lillian lily linda lisa liz liza lorena lorraine louise lucia lucy
luisa lydia madeleine magda magdalena maja malgorzata margaret margarita maria mariana marie marina marisa marta
martha martina mary maya megan melanie melissa mia michaela michelle milena miriam molly monica monika nadia
nadine nancy naomi natalia natalie natasha nicole nina noemi nora olga olivia paola patricia paula paulina pauline
petra pia priya rachel rebecca regina renata rita roberta rosa rose ruth sabina sabrina sandra sara sarah shannon
sharon sheila silvia simona sofia sonia sophia sophie stella stephanie susan susanne suzanne svetlana sylvia
tamara tanya tatiana teresa tessa tina valentina valeria vanessa vera veronica veronika victoria viktoria
violeta virginia wanda wendy wiktoria yasmin yulia yvonne zofia zoe zuzanna anjali deepa divya kavya lakshmi meera
neha pooja preeti priyanka radhika rani shreya sneha sunita swati aditi ananya ayesha fatma zeynep ayse leila
layla maryam noor sara yael tamar noa shira michal efrat`) {
		womenNames[n] = true
	}
}

// likelyWoman: the first name is a common women's name.
func likelyWoman(name string) bool {
	f := strings.Fields(normName(name))
	return len(f) >= 2 && womenNames[f[0]]
}
