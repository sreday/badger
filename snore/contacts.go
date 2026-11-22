package snore

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Contacts: Claude looks for a speaker's LinkedIn profile and an email they
// publish themselves, while scouting and whenever the team presses the
// orange LinkedIn dot. The email button opens a Gmail draft of the speaker
// invitation. Everything Snore learns is in the activity log and in the
// contacts CSV, both behind the admin login.

const (
	contactsPerQueue = 25
	actMax           = 1000
)

// Event is one line of the activity log.
type Event struct {
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`
	Text string    `json:"text"`
}

// note adds a line to the activity log (lock held).
func (s *Server) note(kind, format string, a ...any) {
	s.act = append(s.act, Event{At: s.now(), Kind: kind, Text: clip(fmt.Sprintf(format, a...), 400)})
	if len(s.act) > actMax {
		s.act = s.act[len(s.act)-actMax:]
	}
	writeJSON(filepath.Join(s.dir, "activity.json"), s.act)
}

var emailRe = regexp.MustCompile(`^[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}$`)

func cleanEmail(e string) string {
	e = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(e), "mailto:")))
	if !emailRe.MatchString(e) {
		return ""
	}
	return e
}

func linkedInProfile(u string) string {
	u = strings.TrimSpace(u)
	if strings.HasPrefix(u, "https://") && strings.Contains(u, "linkedin.com/in/") {
		return u
	}
	return ""
}

// askedCards: cards the team asked about that Claude hasn't looked at since.
func (s *Server) askedCards() []*Card {
	var out []*Card
	seen := map[string]bool{}
	for _, cs := range s.st.Cards {
		for _, c := range cs {
			if !c.Asked.IsZero() && c.Asked.After(c.Looked) && !seen[c.Key] {
				seen[c.Key] = true
				out = append(out, c)
			}
		}
	}
	return out
}

// neverLooked: cards from before scouting looked for contacts; they go
// after the ones the team asked about.
func (s *Server) neverLooked() []*Card {
	var out []*Card
	seen := map[string]bool{}
	for _, cs := range s.st.Cards {
		for _, c := range cs {
			if c.Looked.IsZero() && c.Asked.IsZero() && !seen[c.Key] {
				seen[c.Key] = true
				out = append(out, c)
			}
		}
	}
	return out
}

// contactData: the asks that go out with this queue, each leased like a job.
func (s *Server) contactData(now time.Time) (out []map[string]any, remaining int) {
	out = []map[string]any{}
	for _, c := range append(s.askedCards(), s.neverLooked()...) {
		if !c.Handed.IsZero() && now.Sub(c.Handed) < leaseFor {
			continue
		}
		if len(out) >= contactsPerQueue {
			remaining++
			continue
		}
		c.Handed = now
		out = append(out, map[string]any{"name": c.Name, "title": c.Title, "company": c.Company,
			"city": c.Base.City, "country": c.Base.Country, "linkedin_url": c.LinkedIn, "email": c.Email,
			"talks": cardTalks(c.Talks), "sources": c.Links})
	}
	return out, remaining
}

// handleContacts: POST /api/contacts {"contacts": [{"name", "linkedin_url",
// "email"}]}: what Claude found for the people it was asked about. Every name
// asked comes back, with empty fields when nothing public was found.
func (s *Server) handleContacts(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Contacts []struct {
			Name     string `json:"name"`
			LinkedIn string `json:"linkedin_url"`
			Email    string `json:"email"`
			Reach    Reach  `json:"reach"`
		} `json:"contacts"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		http.Error(w, "Bad JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	took := 0
	for _, x := range in.Contacts {
		key := normName(x.Name)
		li, em := linkedInProfile(x.LinkedIn), cleanEmail(x.Email)
		hit := false
		for _, cs := range s.st.Cards {
			for _, c := range cs {
				if c.Key != key {
					continue
				}
				hit = true
				if li != "" && c.LinkedIn == "" { // never over what the team typed in
					c.LinkedIn = li
				}
				if em != "" && c.Email == "" {
					c.Email = em
				}
				if x.Reach.LinkedIn > 0 && c.Reach.LinkedInSrc != "confirmed" {
					c.Reach.LinkedIn, c.Reach.LinkedInSrc, c.Reach.LinkedInAt = x.Reach.LinkedIn, "search", now
				}
				for _, p := range []struct {
					to *int
					v  int
				}{{&c.Reach.X, x.Reach.X}, {&c.Reach.Bluesky, x.Reach.Bluesky}, {&c.Reach.Mastodon, x.Reach.Mastodon}, {&c.Reach.YouTube, x.Reach.YouTube}, {&c.Reach.GitHub, x.Reach.GitHub}} {
					if p.v > 0 {
						*p.to = p.v
					}
				}
				c.Looked, c.Handed = now, time.Time{}
			}
		}
		if !hit {
			continue
		}
		took++
		var found []string
		if li != "" {
			found = append(found, "LinkedIn")
		}
		if em != "" {
			found = append(found, "email")
		}
		if len(found) == 0 {
			s.note("contacts", "%s: no public LinkedIn or email found", x.Name)
		} else {
			s.note("contacts", "%s: %s found", x.Name, strings.Join(found, " and "))
		}
	}
	s.save()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"taken": took})
}

// LinkedInState: "has" (a profile) or "none" (grey: a click types one in).
func (v cardView) LinkedInState() string {
	if v.LinkedInURL() != "" {
		return "has"
	}
	return "none"
}

// Followers: every public follower count added up.
func (v cardView) Followers() int {
	r := v.Reach
	return r.LinkedIn + r.X + r.Bluesky + r.Mastodon + r.YouTube + r.GitHub
}

// FollowersBy: where the followers come from, for the tooltip.
func (v cardView) FollowersBy() string {
	r := v.Reach
	var parts []string
	for _, p := range []struct {
		n string
		v int
	}{{"LinkedIn", r.LinkedIn}, {"X", r.X}, {"Bluesky", r.Bluesky}, {"Mastodon", r.Mastodon}, {"YouTube", r.YouTube}, {"GitHub", r.GitHub}} {
		if p.v > 0 {
			parts = append(parts, p.n+" "+fmtCount(p.v))
		}
	}
	return strings.Join(parts, " · ")
}

// Hint: the one tag under the picture, the most telling first.
func (v cardView) Hint() string {
	switch {
	case v.Match >= 85:
		return "nobrainer"
	case v.Keynote:
		return "keynote-ready"
	case v.Followers() >= 10_000:
		return "many followers"
	}
	return ""
}

// inviteEvent: the event a card's invitation is for: the tab's Conf42
// event, or our brand in the speaker's nearest city.
func (v cardView) inviteEvent() (name, when string) {
	t := v.T
	if t.Online {
		return "Conf42 " + t.Name, t.Next
	}
	brand := brandNames[t.Brand]
	for _, n := range v.Cities {
		for _, c := range t.Cities {
			if normName(c.Name) == normName(n) {
				return brand + " " + c.Name, c.Next
			}
		}
	}
	return brand, ""
}

// GmailURL: a Gmail draft of the speaker invitation, or "" without an email.
func (v cardView) GmailURL() string {
	if v.Email == "" {
		return ""
	}
	first := strings.Fields(v.Name)[0]
	ev, when := v.inviteEvent()
	on := ""
	if d, err := time.Parse(dayFmt, when); err == nil {
		on = " on " + d.Format("2 January 2006")
	}
	saw := ""
	if ts := cardTalks(v.Talks); len(ts) > 0 {
		saw = "I came across your talk at " + ts[0].Event + ", and "
	}
	body := fmt.Sprintf("Hey %s,\n\nI'm Mark, I run %s%s. %sI think you'd be a great fit for our stage!\n\nWould you be up for giving a talk? Happy to share all the details.\n\nCheers,\nMark", first, ev, on, saw)
	q := url.Values{"view": {"cm"}, "fs": {"1"}, "to": {v.Email}, "su": {"Speaking at " + ev + "?"}, "body": {body}}
	if v.Gmail != "" {
		q.Set("authuser", v.Gmail)
	}
	return "https://mail.google.com/mail/?" + q.Encode()
}

// handleCSV: GET /contacts.csv, every card on the board with what Snore knows.
func (s *Server) handleCSV(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="snore-contacts-`+now.UTC().Format(dayFmt)+`.csv"`)
	w.Write([]byte("\xef\xbb\xbf")) // so spreadsheet apps read it as UTF-8
	cw := csv.NewWriter(w)
	cw.Write([]string{"tab", "nearest city of ours", "name", "title", "company", "city", "country", "linkedin", "email",
		"followers", "hint", "pronouns", "match", "humans app status", "cfp.ninja", "conferences", "first seen"})
	for _, t := range s.fp.Topics(now) {
		for _, c := range s.st.Cards[t.ID] {
			v := cardView{Card: c, Brand: t.Brand, T: t}
			near := ""
			if len(c.Cities) > 0 {
				near = c.Cities[0]
			}
			var confs []string
			for _, tk := range cardTalks(c.Talks) {
				confs = append(confs, tk.Event)
			}
			cfp := ""
			if c.CFP.Event != "" {
				cfp = strings.TrimSpace(c.CFP.Event + " " + c.CFP.Status)
			}
			row := []string{t.Name, near, c.Name, c.Title, c.Company, c.Base.City, c.Base.Country, v.LinkedInURL(), c.Email,
				strconv.Itoa(v.Followers()), v.Hint(), c.Pronouns, strconv.Itoa(c.Match), c.Humans.Status, cfp, strings.Join(confs, "; "), c.First.UTC().Format(dayFmt)}
			for i, f := range row {
				if f != "" && strings.ContainsRune("=+-@", rune(f[0])) {
					row[i] = "'" + f // never a formula
				}
			}
			cw.Write(row)
		}
	}
	cw.Flush()
}

// handleLog: GET /log, the activity log, newest first.
func (s *Server) handleLog(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	ev := make([]Event, 0, len(s.act))
	for i := len(s.act) - 1; i >= 0; i-- {
		ev = append(ev, s.act[i])
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "log.html", ev); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// normalizeLinkedIn accepts what people paste: "linkedin.com/in/x",
// "www.linkedin.com/in/x?trk=..." or the full address.
func normalizeLinkedIn(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return ""
	}
	if !strings.HasPrefix(u, "http") {
		u = "https://" + u
	}
	u = strings.Replace(u, "http://", "https://", 1)
	if i := strings.IndexAny(u, "?#"); i > 0 {
		u = u[:i]
	}
	return u
}
