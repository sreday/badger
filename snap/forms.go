package snap

import (
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Website forms (Marek 2026-11-13: "by the new Snap rules and routines,
// these emails should land straight into the Hot tab"). The sponsor lead
// form on the conference sites emails the lead in Mark's name ("Hey Olga,
// Thank you for submitting the form, here's what we're working with... Mark
// will reply and share the relevant prospectuses soon"), with Mark on the
// recipients. To Gmail that is Mark's own sent mail, and Snap used to file
// it as outreach nobody answered. It is the opposite: a lead just wrote in
// and was promised a reply. Such a message counts as written by the lead,
// with the ball in Mark's park, until Mark really answers in the thread.

var formAck = regexp.MustCompile(`(?i)thank you for submitting the form`)

// mine: Mark's own addresses are his mailbox's name at any of the team's
// domains (mark@sreday.com, mark@llmday.com, mark@conf42.com...). Set by New.
var mine struct {
	local   string
	domains []string
}

func setMine(account string, domains []string) {
	local := account
	if i := strings.Index(account, "@"); i >= 0 {
		local = account[:i]
	}
	mine.local, mine.domains = strings.ToLower(local), domains
}

func isMine(addr string) bool {
	addr = strings.ToLower(strings.TrimSpace(addr))
	i := strings.LastIndex(addr, "@")
	if mine.local == "" || i < 0 || addr[:i] != mine.local {
		return false
	}
	for _, d := range mine.domains {
		if addr[i+1:] == d {
			return true
		}
	}
	return false
}

// formLead: who filled in the form, from the message's recipients (the one
// who isn't on the team).
func formLead(ml Mail, team []string) (mail.Address, bool) {
	for _, a := range ml.To {
		if !isTeam(a.Address, team) {
			return a, true
		}
	}
	return mail.Address{}, false
}

// recheckRecent has the next read look again, once, at every thread of the
// last two weeks, so mail filed under older rules (form leads taken for
// Mark's own outreach, his other addresses taken for someone else) is
// sorted again.
func (s *Server) recheckRecent(version string) {
	mark := filepath.Join(s.dir, "recheck-"+version)
	if _, err := os.Stat(mark); err == nil {
		return
	}
	cut := time.Now().AddDate(0, 0, -14)
	s.store.UpdateAll(func(t *Thread) {
		if (t.LastAt.After(cut) || formAck.MatchString(t.Snippet)) && !t.Done && !t.DeadEnd {
			t.HistoryID = ""
		}
	})
	os.WriteFile(mark, []byte(time.Now().UTC().Format(time.RFC3339)), 0o600)
}
