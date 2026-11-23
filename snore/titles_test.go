package snore

import "testing"

func TestRoleOf(t *testing.T) {
	for title, want := range map[string]string{
		"Field Marketing Manager, EMEA":    RoleMarketing,
		"VP Marketing":                     RoleMarketing,
		"Head of Events":                   RoleMarketing,
		"Senior Developer Advocate":        RoleDevRel,
		"Community Manager":                RoleDevRel,
		"Founder & CTO":                    RoleFounder,
		"Co-Founder":                       RoleFounder,
		"CEO":                              RoleFounder,
		"Geschäftsführer":                  RoleFounder,
		"Account Executive":                RolePartners,
		"Head of Partnerships":             RolePartners,
		"Solutions Engineer":               RolePartners,
		"Technical Recruiter":              RoleTalent,
		"Talent Acquisition Lead":          RoleTalent,
		"Senior Site Reliability Engineer": RoleEngineering,
		"CTO":                              RoleEngineering,
		"Platform Lead":                    RoleEngineering,
		"Inżynier oprogramowania":          RoleEngineering,
		"PhD Student":                      RoleStudent,
		"Software Engineering Intern":      RoleStudent,
		"":                                 RoleOther,
		"Wizard":                           RoleOther,
		"Prevention specialist":            RoleOther, // "event" inside a word is no match
	} {
		if got := roleOf(title); got != want {
			t.Errorf("roleOf(%q) = %q, want %q", title, got, want)
		}
	}
}
