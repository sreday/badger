package snore

import (
	"regexp"
	"strings"
)

// A job title sorted into what the person does, by plain word rules. The
// first rule that matches wins; the order matters (a "VP Marketing" is
// marketing, a "Founder & CTO" is a founder).

const (
	RoleFounder     = "founder"   // founders and the top of the company
	RoleMarketing   = "marketing" // marketing, events, field, brand, growth
	RoleDevRel      = "devrel"    // developer relations, advocates, community
	RolePartners    = "partners"  // sales, partnerships, alliances, business development
	RoleTalent      = "talent"    // recruiting and people teams
	RoleStudent     = "student"
	RoleEngineering = "engineering" // everyone who builds and runs things
	RoleOther       = "other"
)

type titleRule struct {
	role string
	re   *regexp.Regexp
}

func words(list string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)(^|[^\p{L}])(` + list + `)([^\p{L}]|$)`)
}

var titleRules = []titleRule{
	{RoleStudent, words(`student|undergrad(uate)?|phd candidate|intern|trainee|apprentice|estudiante|étudiant|studentka?`)},
	{RoleFounder, words(`co-?founder|founder|founding|owner|ceo|chief executive|president|managing director|managing partner|geschäftsführer(in)?|gründer(in)?|fondateur|fondatrice|właściciel(ka)?|prezes|fundador(a)?`)},
	{RoleDevRel, words(`devrel|developer relations|developer advocate|developer advocacy|advocate|evangelist|community (manager|lead|director|builder)|head of community|developer experience|developer marketing`)},
	{RoleMarketing, words(`marketing|marketer|cmo|events?|field|brand|growth|demand gen(eration)?|communications|comms|content|public relations|pr manager|marketingleiter(in)?|kierownik marketingu|responsable marketing|mercadotecnia`)},
	{RoleTalent, words(`recruit(er|ing|ment)?|talent|sourcer|people (partner|operations|ops|team|lead)|human resources|hr|headhunter|employer brand(ing)?|personalleiter(in)?|rekruter(ka)?`)},
	{RolePartners, words(`sales|account (executive|manager|director)|business development|bd|bdr|sdr|partnerships?|partner (manager|lead|director)|alliances?|commercial|revenue|cro|customer success|solutions? (engineer|architect|consultant)|pre-?sales|vertrieb|sprzedaż(y)?|ventas`)},
	{RoleEngineering, words(`engineer(ing)?|developer|programmer|sre|devops|devsecops|platform|infrastructure|cloud|architect|cto|cio|ciso|technical|technology|software|data|scientist|machine learning|ml|ai|analyst|security|reliability|operations|ops|sysadmin|administrator|tester|qa|product|entwickler(in)?|ingenieur(in)?|inżynier|programista|ingénieur|ingeniero|desarrollador(a)?`)},
}

// roleOf sorts one title.
func roleOf(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return RoleOther
	}
	for _, r := range titleRules {
		if r.re.MatchString(title) {
			return r.role
		}
	}
	return RoleOther
}

// insideRole: someone who can open the door to a sponsorship, because
// events and budgets are their job or their company.
func insideRole(role string) bool {
	return role == RoleFounder || role == RoleMarketing || role == RoleDevRel || role == RolePartners
}
