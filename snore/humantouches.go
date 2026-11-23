package snore

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// The team's outreach, read from the humans app as touches: who on the
// team reached whom, what came back and when, and who registered for or
// came to an event. Each person is tied to a company by work email domain
// when one is known, else by the company name on their profile.

// freeMail domains say nothing about who someone works for.
var freeMail = map[string]bool{
	"gmail.com": true, "googlemail.com": true, "outlook.com": true, "hotmail.com": true, "live.com": true, "msn.com": true,
	"yahoo.com": true, "ymail.com": true, "icloud.com": true, "me.com": true, "mac.com": true, "aol.com": true,
	"proton.me": true, "protonmail.com": true, "pm.me": true, "gmx.com": true, "gmx.de": true, "gmx.net": true, "web.de": true,
	"mail.com": true, "zoho.com": true, "yandex.com": true, "yandex.ru": true, "mail.ru": true, "qq.com": true, "163.com": true,
	"duck.com": true, "hey.com": true, "fastmail.com": true, "tutanota.com": true, "tuta.io": true, "wp.pl": true, "o2.pl": true,
	"interia.pl": true, "onet.pl": true, "rediffmail.com": true, "btinternet.com": true, "sky.com": true, "t-online.de": true,
	"orange.fr": true, "free.fr": true, "laposte.net": true, "libero.it": true, "seznam.cz": true, "naver.com": true,
}

var freeMailLike = regexp.MustCompile(`^(yahoo|hotmail|outlook|live)\.[a-z.]{2,6}$`)

// workDomain: the registrable domain of a work address, "" for private
// mail, schools and anything that is not an address.
func workDomain(email string) string {
	email = strings.ToLower(strings.TrimSpace(email))
	i := strings.LastIndexByte(email, '@')
	if i < 1 || i == len(email)-1 {
		return ""
	}
	host := email[i+1:]
	if freeMail[host] || freeMailLike.MatchString(host) {
		return ""
	}
	d := companyDomain(host)
	if d == "" || freeMail[d] || strings.HasSuffix(d, ".edu") || strings.Contains(d, ".ac.") || strings.Contains(d, ".edu.") {
		return ""
	}
	return d
}

var (
	legalWords = map[string]bool{"inc": true, "ltd": true, "limited": true, "llc": true, "gmbh": true, "plc": true, "corp": true,
		"corporation": true, "co": true, "company": true, "sa": true, "bv": true, "ag": true, "pvt": true, "private": true,
		"sro": true, "oy": true, "ab": true, "as": true, "srl": true, "spa": true, "pte": true, "lp": true, "llp": true, "the": true}
	notCompany = map[string]bool{"": true, "na": true, "n a": true, "none": true, "self": true, "self employed": true, "selfemployed": true,
		"freelance": true, "freelancer": true, "independent": true, "student": true, "unemployed": true, "home": true,
		"stealth": true, "stealth startup": true, "open to work": true, "retired": true, "personal": true}
	nonWord = regexp.MustCompile(`[^\p{L}\p{N}]+`)
)

// normCompany folds a typed company name: case, punctuation and legal
// endings go ("Example Ltd." and "example limited" are one). "" for words
// that name no company.
func normCompany(s string) string {
	words := strings.Fields(nonWord.ReplaceAllString(strings.ToLower(s), " "))
	for len(words) > 1 && legalWords[words[len(words)-1]] {
		words = words[:len(words)-1]
	}
	if len(words) > 1 && words[0] == "the" {
		words = words[1:]
	}
	k := strings.Join(words, " ")
	if notCompany[k] {
		return ""
	}
	return k
}

// What a word in the outreach log means for the ladder. A visit to a
// profile and a private note are no contact and are never read.
var outreachKinds = map[string]string{
	"connected": TouchConnected,
	// sent by the team, nothing back yet
	"pending": TouchInvited, "messaged": TouchInvited, "message": TouchInvited, "followup": TouchInvited, "no-response": TouchInvited,
	// the person answered, whatever the answer
	"interested": TouchReplied, "mark interested": TouchReplied, "maybe": TouchReplied, "next-time": TouchReplied,
	"can't come": TouchReplied, "win": TouchReplied, "interaction": TouchReplied, "volunteer": TouchReplied,
	"not interested": TouchReplied, "mark not interested": TouchReplied,
}

type humanProfile struct{ slug, name, title, company string }

type humanAttendee struct {
	slug, email, name, event string
	registered, checkedIn    time.Time
}

type humanEvent struct {
	slug, kind, by string
	at             time.Time
}

type humanSpeaker struct {
	slug, name, event string
	at                time.Time
}

type humanData struct {
	profiles  []humanProfile
	attendees []humanAttendee
	events    []humanEvent
	speakers  []humanSpeaker
	sheet     []sheetSpeaker // the lineup sheets, when the feed is set
}

const (
	qTouchProfiles = `SELECT DISTINCT ON (linked_in) linked_in, coalesce(name,''), coalesce(title,''), coalesce(company,'')
		FROM profile_metadata WHERE deleted_at IS NULL AND coalesce(linked_in,'') <> '' ORDER BY linked_in, observed_at DESC NULLS LAST`
	qTouchAttendees = `SELECT coalesce(linked_in,''), coalesce(email,''), coalesce(name,''), coalesce(event_name,''),
		coalesce(registered_at, created_at), checked_in_at FROM attendees WHERE deleted_at IS NULL`
	qTouchEvents = `SELECT linked_in, event_type, coalesce(observed_by,''), coalesce("timestamp", created_at) FROM user_events
		WHERE deleted_at IS NULL AND coalesce(linked_in,'') <> '' AND event_type NOT IN ('visit','note')`
	qTouchSpeakers = `SELECT coalesce(linked_in,''), coalesce(name,''), coalesce(event_luma_id,''), created_at FROM speakers WHERE deleted_at IS NULL`
)

// readTouchData loads the outreach log, read-only.
func readTouchData(ctx context.Context, dsn string) (*humanData, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, err
	}
	defer conn.Close(context.Background())
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	d := &humanData{}
	val := func(t *time.Time) time.Time {
		if t == nil {
			return time.Time{}
		}
		return *t
	}

	rows, err := tx.Query(ctx, qTouchProfiles)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p humanProfile
		if rows.Scan(&p.slug, &p.name, &p.title, &p.company) == nil {
			d.profiles = append(d.profiles, p)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = tx.Query(ctx, qTouchAttendees)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var a humanAttendee
		var reg, in *time.Time
		if rows.Scan(&a.slug, &a.email, &a.name, &a.event, &reg, &in) == nil {
			a.registered, a.checkedIn = val(reg), val(in)
			d.attendees = append(d.attendees, a)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = tx.Query(ctx, qTouchEvents)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var e humanEvent
		var at *time.Time
		if rows.Scan(&e.slug, &e.kind, &e.by, &at) == nil {
			e.at = val(at)
			d.events = append(d.events, e)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = tx.Query(ctx, qTouchSpeakers)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var sp humanSpeaker
		var at *time.Time
		if rows.Scan(&sp.slug, &sp.name, &sp.event, &at) == nil {
			sp.at = val(at)
			d.speakers = append(d.speakers, sp)
		}
	}
	return d, rows.Err()
}

// nameKey: a company known only by its typed name.
func nameKey(norm string) string { return "name:" + norm }

// nameFits: the name could be the company behind the domain ("Example AI"
// and example.ai, "Example" and example-labs.com).
func nameFits(domain, name string) bool {
	if strings.HasPrefix(domain, "name:") {
		return false
	}
	label := domain
	if i := strings.IndexByte(label, '.'); i > 0 {
		label = label[:i]
	}
	label = nonWord.ReplaceAllString(strings.ToLower(label), "")
	n := nonWord.ReplaceAllString(strings.ToLower(name), "")
	if len(label) < 3 || len(n) < 3 {
		return false
	}
	return strings.HasPrefix(n, label) || strings.HasPrefix(label, n)
}

// buildTouches turns the outreach log into touches and gives each company
// key a name to show. A person with no company on record is left out:
// there is nothing to attach them to.
func buildTouches(d *humanData) (touches []Touch, names map[string]string) {
	prof := map[string]humanProfile{}
	for _, p := range d.profiles {
		prof[p.slug] = p
	}
	// A person's work domain, from any registration with a work address.
	domainOf := map[string]string{}
	for _, a := range d.attendees {
		if a.slug == "" {
			continue
		}
		if dom := workDomain(a.email); dom != "" {
			domainOf[a.slug] = dom
		}
	}
	// Typed company name -> the domain most of its people write from.
	votes := map[string]map[string]int{}
	shown := map[string]map[string]int{}
	for slug, dom := range domainOf {
		p := prof[slug]
		if n := normCompany(p.company); n != "" {
			if votes[n] == nil {
				votes[n] = map[string]int{}
			}
			votes[n][dom]++
		}
	}
	for _, sp := range d.sheet {
		if dom, n := workDomain(sp.Email), normCompany(sp.Org); dom != "" && n != "" {
			if votes[n] == nil {
				votes[n] = map[string]int{}
			}
			votes[n][dom]++
		}
	}
	alias := map[string]string{}
	for n, v := range votes {
		best, top := "", 0
		for dom, c := range v {
			if c > top || (c == top && dom < best) {
				best, top = dom, c
			}
		}
		alias[n] = best
	}
	see := func(key, name string) {
		if name = strings.TrimSpace(name); name == "" {
			return
		}
		if shown[key] == nil {
			shown[key] = map[string]int{}
		}
		shown[key][name]++
	}
	// keyFor: the company of one person, "" when unknown.
	keyFor := func(slug, email string) string {
		p := prof[slug]
		n := normCompany(p.company)
		key := workDomain(email)
		if key == "" {
			key = domainOf[slug]
		}
		if key == "" && n != "" {
			if key = alias[n]; key == "" {
				key = nameKey(n)
			}
		}
		if key != "" {
			see(key, p.company)
		}
		return key
	}
	person := func(slug, fallback string) (name, title, profile string) {
		p := prof[slug]
		name = p.name
		if name == "" {
			name = fallback
		}
		if slug != "" {
			profile = "https://www.linkedin.com/in/" + strings.ToLower(slug) + "/"
		}
		return name, p.title, profile
	}

	for _, a := range d.attendees {
		key := keyFor(a.slug, a.email)
		if key == "" {
			continue
		}
		name, title, profile := person(a.slug, a.name)
		if name == "" {
			continue
		}
		if !a.registered.IsZero() {
			touches = append(touches, Touch{Company: key, Person: name, Profile: profile, Title: title, Kind: TouchRegistered, Event: a.event, At: a.registered})
		}
		if !a.checkedIn.IsZero() {
			touches = append(touches, Touch{Company: key, Person: name, Profile: profile, Title: title, Kind: TouchCheckedIn, Event: a.event, At: a.checkedIn})
		}
	}
	for _, e := range d.events {
		kind := outreachKinds[strings.ToLower(strings.TrimSpace(e.kind))]
		if kind == "" || e.at.IsZero() {
			continue
		}
		key := keyFor(e.slug, "")
		if key == "" {
			continue
		}
		name, title, profile := person(e.slug, "")
		if name == "" {
			continue
		}
		touches = append(touches, Touch{Company: key, Person: name, Profile: profile, Title: title, By: strings.TrimSpace(e.by), Kind: kind, Note: e.kind, At: e.at})
	}
	for _, sp := range d.speakers {
		if sp.at.IsZero() {
			continue
		}
		key := keyFor(sp.slug, "")
		if key == "" {
			continue
		}
		name, title, profile := person(sp.slug, sp.name)
		if name == "" {
			continue
		}
		touches = append(touches, Touch{Company: key, Person: name, Profile: profile, Title: title, By: Organizer, Kind: TouchSpoke, Event: sp.event, At: sp.at})
	}
	for _, sp := range d.sheet {
		at := tabDate(sp.Tab)
		if at.IsZero() || strings.TrimSpace(sp.Name) == "" {
			continue
		}
		n := normCompany(sp.Org)
		key := workDomain(sp.Email)
		if key == "" && n != "" {
			if key = alias[n]; key == "" {
				key = nameKey(n)
			}
		}
		if key == "" {
			continue
		}
		see(key, sp.Org)
		kind := TouchSpoke
		if notOnStage.MatchString(sp.Status) {
			kind = TouchSubmitted
		}
		touches = append(touches, Touch{Company: key, Person: strings.TrimSpace(sp.Name), Profile: profileURL(sp.LinkedIn), Title: sp.Title,
			By: Organizer, Kind: kind, Event: strings.TrimSpace(sp.Book + " " + sp.Tab), At: at})
	}

	names = map[string]string{}
	for key, v := range shown {
		// People keep an old work address after they move on, so the
		// loudest name can be another company's: a name that matches the
		// domain goes first.
		best, top, fits := "", 0, false
		for n, c := range v {
			f := nameFits(key, n)
			if (f && !fits) || (f == fits && (c > top || (c == top && n < best))) {
				best, top, fits = n, c, f
			}
		}
		names[key] = best
	}
	sort.SliceStable(touches, func(i, j int) bool { return touches[i].At.Before(touches[j].At) })
	return touches, names
}
