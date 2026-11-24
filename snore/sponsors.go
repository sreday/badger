package snore

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// The Sponsors board: per brand, the few companies to act on now. The list
// and its order come from our own records (candidates.go) and show at once.
// Once a day Claude adds to the top of it who decides about sponsoring at
// the company and two short texts: one to a person we know there, one to
// that decision maker.

const (
	sponsorTop      = 10               // leads shown per brand; "Show more" adds this many
	sponsorNew      = 10               // leads the daily read writes for, per brand
	sponsorLook     = 20               // how far down the list the daily read looks
	sponsorLease    = 90 * time.Minute // how long a handed-out job is Claude's
	sponsorStale    = 30 * 24 * time.Hour
	sponsorFacts    = 7 * 24 * time.Hour     // texts are written again this long after our facts changed
	sponsorPassFor  = 90 * 24 * time.Hour    // a company Claude turned down is not asked about again for this long
	sponsorHour     = 5                      // UTC: when the day's jobs are queued
	offerFrom       = 6 * 7 * 24 * time.Hour // an event nearer than this is too late to offer
	offerUntil      = 9 * month
	sponsorJobsKept = 60
	cardVer         = 2 // cards from before the two texts are written again
)

type SponsorPerson struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Profile string `json:"profile,omitempty"`
	Email   string `json:"email,omitempty"`
	Guessed string `json:"guessed,omitempty"` // what a guessed address rests on, "" for a published one
	Proof   string `json:"proof,omitempty"`   // a page that shows the role today
	Country string `json:"country,omitempty"` // where they work, two letters
}

// sponsorText is one ready message.
type sponsorText struct {
	To      string `json:"to,omitempty"` // who it is for, by name
	Subject string `json:"subject,omitempty"`
	Text    string `json:"text,omitempty"`
}

// SponsorCard is what the daily read added to a lead.
type SponsorCard struct {
	Key     string `json:"key"`
	Domain  string `json:"domain,omitempty"`
	Company string `json:"company"`
	Brand   string `json:"brand"`

	Name   string         `json:"name,omitempty"` // the company's name as its own site writes it
	Boss   *SponsorPerson `json:"boss,omitempty"` // who decides about sponsoring events
	Known  sponsorText    `json:"to_known"`       // to a person we know
	ToBoss sponsorText    `json:"to_boss"`        // to the decision maker
	Facts  string         `json:"facts,omitempty"`

	FirstSeen time.Time `json:"first_seen"`
	Refreshed time.Time `json:"refreshed"`
	Reached   time.Time `json:"reached,omitempty"`
	Model     string    `json:"model,omitempty"`
	Ver       int       `json:"ver,omitempty"`
}

// written: the card carries a text in today's form.
func (c *SponsorCard) written() bool {
	return c != nil && c.Ver >= cardVer && (c.Known.Text != "" || c.ToBoss.Text != "")
}

type sponsorMark struct {
	Reason string    `json:"reason,omitempty"`
	At     time.Time `json:"at"`
}

type sponsorJob struct {
	ID     string    `json:"id"`
	Brand  string    `json:"brand"`
	Day    string    `json:"day"`
	Asked  time.Time `json:"asked"`
	Handed time.Time `json:"handed,omitempty"`
	Done   time.Time `json:"done,omitempty"`
	Keys   []string  `json:"keys,omitempty"` // the companies handed out
	Note   string    `json:"note,omitempty"`
}

type sponsorState struct {
	Cards  map[string][]*SponsorCard `json:"cards"`  // brand -> what was written for its leads
	Never  map[string]sponsorMark    `json:"never"`  // crossed out by the team: for good
	Passed map[string]sponsorMark    `json:"passed"` // turned down by Claude: for a while
	Jobs   []*sponsorJob             `json:"jobs"`
}

var sponsorBrands = []struct{ ID, Name string }{
	{"sreday", "SREday"}, {"llmday", "LLMday"}, {"platformday", "PLATFORMday"}, {"conf42", "Conf42"},
}

func (s *Server) loadSponsors() {
	readJSON(filepath.Join(s.dir, "sponsors.json"), &s.sp)
	if s.sp.Cards == nil {
		s.sp.Cards = map[string][]*SponsorCard{}
	}
	if s.sp.Never == nil {
		s.sp.Never = map[string]sponsorMark{}
	}
	if s.sp.Passed == nil {
		s.sp.Passed = map[string]sponsorMark{}
	}
}

func (s *Server) saveSponsors() { writeJSON(filepath.Join(s.dir, "sponsors.json"), s.sp) }

// SponsorInstructions is what Claude is told with every sponsor job. The
// private recipe (sponsors-recipe.txt in the data folder) is added to it.

const SponsorInstructions = `Each job is one brand with the event to offer and its leads: companies whose people we already know, the best first.
A lead carries "why_now" (our own facts: use them, add none) and "people_we_know" (who they are and what they did
with us). For every lead answer with a card or a refusal, following the private recipe below.
A card holds two short texts. "to_known" is for one of the people we know ("to" is their name exactly as given).
"to_boss" is for the person who decides about sponsoring events at the company: find them and give a page that
shows they hold the role today. "email": "yes" or "guessed" on a person means their text goes by email; without
it the text is a LinkedIn message. Refuse a company that would never sponsor an event like ours, in a few words.

Answer: POST {"model": "<you>", "answers": [{"id": "<job id>", "note": "<one line>", "cards": [CARD...],
"refused": [{"key": "<lead key>", "reason": "<a few words>"}]}]}
CARD: {"key": "<lead key, unchanged>", "name": "<the company's name as its own website writes it>",
"boss": {"name": "", "title": "", "profile": "<LinkedIn address>", "proof": "<https page that shows the role>",
"email": "<only one they published themselves>", "country": "GB"},
"to_known": {"to": "", "subject": "", "text": ""}, "to_boss": {"subject": "", "text": ""}}
Leave "boss" and "to_boss" out when nobody can be shown to hold the role. Leave "to_known" out when the lead has
no people. Every lead of a job is either a card or refused.
The "company" of a lead is what people typed and can be the new employer of someone who left: the company is the
one behind the key's domain. A lead's "talk" is an old conversation about sponsoring and "with" says who it was with.`

// sponsorRecipe: the private part of the instructions, "" when not set.
func (s *Server) sponsorRecipe() string {
	b, _ := os.ReadFile(filepath.Join(s.dir, "sponsors-recipe.txt"))
	return strings.TrimSpace(string(b))
}

func (s *Server) sponsorInstructions() string {
	return SponsorInstructions + "\n\nPRIVATE RECIPE\n" + s.sponsorRecipe()
}

// dueSponsorJobs queues one job per brand and day, from 05:00 UTC (lock held).
func (s *Server) dueSponsorJobs(now time.Time) {
	now = now.UTC()
	if now.Hour() < sponsorHour {
		return
	}
	day := now.Format(dayFmt)
	have := map[string]bool{}
	for _, j := range s.sp.Jobs {
		if j.Day == day {
			have[j.Brand] = true
		}
	}
	added := false
	for _, b := range sponsorBrands {
		if !have[b.ID] {
			s.sp.Jobs = append(s.sp.Jobs, &sponsorJob{ID: "sp-" + b.ID + "-" + day, Brand: b.ID, Day: day, Asked: now})
			added = true
		}
	}
	if len(s.sp.Jobs) > sponsorJobsKept {
		s.sp.Jobs = s.sp.Jobs[len(s.sp.Jobs)-sponsorJobsKept:]
	}
	if added {
		s.saveSponsors()
	}
}

type offerEvent struct {
	Name   string `json:"name"`
	City   string `json:"city,omitempty"`
	Date   string `json:"date"`
	Online bool   `json:"online,omitempty"`
}

var brandTitles = map[string]string{"sreday": "SREday", "llmday": "LLMday", "platformday": "PLATFORMday", "pec": "Prompt Engineering Conference", "conf42": "Conf42"}

// offerEvents: the brand's events far enough ahead to offer and near
// enough to matter. The prompt engineering events go with LLMday.
func (s *Server) offerEvents(brand string, now time.Time) []offerEvent {
	s.fp.mu.Lock()
	defer s.fp.mu.Unlock()
	var out []offerEvent
	for _, f := range s.fp.F {
		b := f.Brand
		if b == "pec" {
			b = "llmday"
		}
		if b != brand || f.Start.Before(now.Add(offerFrom)) || f.Start.After(now.Add(offerUntil)) {
			continue
		}
		e := offerEvent{Date: f.Start.Format(dayFmt), Online: f.Brand == "conf42", Name: folderTitle(f)}
		if !e.Online {
			e.City = f.Name
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// cardOf: what was written for a lead, nil when nothing yet (lock held).
func (s *Server) cardOf(brand, key string) *SponsorCard {
	for _, c := range s.sp.Cards[brand] {
		if c.Key == key {
			return c
		}
	}
	return nil
}

// sponsorLeads: a brand's leads in order, without the companies the team
// crossed out and the ones Claude turned down lately (lock held).
func (s *Server) sponsorLeads(pool []Candidate, brand string, now time.Time) []Candidate {
	var out []Candidate
	for _, c := range pool {
		if c.Brand != brand {
			continue
		}
		if _, never := s.sp.Never[c.Key]; never {
			continue
		}
		if p, ok := s.sp.Passed[c.Key]; ok && now.Sub(p.At) < sponsorPassFor {
			continue
		}
		out = append(out, c)
	}
	return out
}

// handleSponsorQueue: GET /api/sponsors/queue.
func (s *Server) handleSponsorQueue(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	pool := s.sponsorPool(now) // takes the lock itself
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dueSponsorJobs(now)
	jobs := []map[string]any{}
	if s.sponsorRecipe() == "" {
		// Without the recipe Claude would write blind: hand out nothing.
		writeJSONTo(w, map[string]any{"today": now.UTC().Format(dayFmt), "jobs": jobs, "note": "no recipe set"})
		return
	}
	for _, j := range s.sp.Jobs {
		if !j.Done.IsZero() || (!j.Handed.IsZero() && now.Sub(j.Handed) < sponsorLease) || now.Sub(j.Asked) > 48*time.Hour {
			continue
		}
		events := s.offerEvents(j.Brand, now)
		if len(events) == 0 {
			j.Done, j.Note = now, "no event to offer"
			continue
		}
		// The top of the list that has no text yet, or an old one.
		var leads []Candidate
		looked := 0
		for _, c := range s.sponsorLeads(pool, j.Brand, now) {
			card := s.cardOf(j.Brand, c.Key)
			if card != nil && !card.Reached.IsZero() {
				continue
			}
			if looked++; looked > sponsorLook || len(leads) >= sponsorNew {
				break
			}
			if card.written() && now.Sub(card.Refreshed) < sponsorStale && (card.Facts == c.Why || now.Sub(card.Refreshed) < sponsorFacts) {
				continue
			}
			leads = append(leads, c)
		}
		if len(leads) == 0 {
			j.Done, j.Note = now, "nothing to write"
			continue
		}
		j.Handed, j.Keys = now, nil
		for _, c := range leads {
			j.Keys = append(j.Keys, c.Key)
		}
		jobs = append(jobs, map[string]any{"id": j.ID, "brand": brandTitles[j.Brand], "events": events, "leads": leads})
	}
	s.saveSponsors()
	writeJSONTo(w, map[string]any{"today": now.UTC().Format(dayFmt), "instructions": s.sponsorInstructions(), "jobs": jobs})
}

func writeJSONTo(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// foldText: letters and digits only, lower case, single spaces: how two
// spellings of a name are compared.
func foldText(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		} else {
			space = true
		}
	}
	return b.String()
}

// noColdMail: countries where a first email to a named person needs their
// consent, also between companies.
var noColdMail = map[string]bool{"DE": true, "PL": true, "ES": true, "AT": true, "IT": true}

type sponsorAnswer struct {
	ID    string `json:"id"`
	Note  string `json:"note"`
	Cards []struct {
		Key    string         `json:"key"`
		Name   string         `json:"name"`
		Boss   *SponsorPerson `json:"boss"`
		Known  sponsorText    `json:"to_known"`
		ToBoss sponsorText    `json:"to_boss"`
	} `json:"cards"`
	Refused []struct {
		Key    string `json:"key"`
		Reason string `json:"reason"`
	} `json:"refused"`
}

func cleanText(t sponsorText) sponsorText {
	return sponsorText{To: clip(strings.TrimSpace(t.To), 80), Subject: clip(strings.TrimSpace(t.Subject), 120), Text: clip(strings.TrimSpace(t.Text), 1200)}
}

// handleSponsorAnswers: POST /api/sponsors/answers.
func (s *Server) handleSponsorAnswers(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Model   string          `json:"model"`
		Answers []sponsorAnswer `json:"answers"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&in); err != nil {
		http.Error(w, "bad JSON", http.StatusBadRequest)
		return
	}
	now := s.now()
	pool := map[string]Candidate{}
	for _, c := range s.sponsorPool(now) {
		pool[c.Brand+"\x00"+c.Key] = c
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	shapes := s.mailShapes
	kept, dropped := 0, 0
	for _, a := range in.Answers {
		var job *sponsorJob
		for _, j := range s.sp.Jobs {
			if j.ID == a.ID && j.Done.IsZero() {
				job = j
			}
		}
		if job == nil {
			continue
		}
		handed := map[string]bool{}
		for _, k := range job.Keys {
			handed[k] = true
		}
		for _, ref := range a.Refused {
			if handed[ref.Key] {
				s.sp.Passed[ref.Key] = sponsorMark{Reason: clip(ref.Reason, 160), At: passedAt(ref.Reason, now)}
				s.dropSponsorCard(job.Brand, ref.Key)
			}
		}
		for _, c := range a.Cards {
			cand, ok := pool[job.Brand+"\x00"+c.Key]
			if !ok || !handed[c.Key] {
				dropped++
				continue
			}
			card := &SponsorCard{Key: cand.Key, Domain: cand.Domain, Company: cand.Company, Brand: job.Brand, Facts: cand.Why,
				FirstSeen: now, Refreshed: now, Model: clip(in.Model, 60), Ver: cardVer}
			// Claude read the company's own site: its name for the company
			// counts when it matches the domain, or when ours does not.
			if n := clip(strings.TrimSpace(c.Name), 60); n != "" && cand.Domain != "" && (nameFits(cand.Domain, n) || !nameFits(cand.Domain, cand.Company)) {
				card.Name = n
			}
			// The text to someone we know goes to one of the people we hold.
			if t := cleanText(c.Known); t.Text != "" && len(cand.People) > 0 {
				to := cand.People[0].Name
				for _, p := range cand.People {
					if foldText(p.Name) == foldText(t.To) {
						to = p.Name
					}
				}
				t.To = to
				card.Known = t
			}
			// The decision maker needs a name and a page that shows the role.
			if b := c.Boss; b != nil && strings.TrimSpace(b.Name) != "" {
				q := &SponsorPerson{Name: clip(strings.TrimSpace(b.Name), 80), Title: clip(strings.TrimSpace(b.Title), 120), Profile: profileURL(b.Profile),
					Email: cleanEmail(b.Email), Country: strings.ToUpper(clip(strings.TrimSpace(b.Country), 2))}
				if strings.HasPrefix(b.Proof, "https://") {
					q.Proof = b.Proof
				}
				if q.Email == "" {
					q.Email, q.Guessed = guessMail(shapes, cand.Domain, q.Name)
				}
				if q.Profile != "" || q.Proof != "" {
					card.Boss = q
					if t := cleanText(c.ToBoss); t.Text != "" {
						t.To = q.Name
						card.ToBoss = t
					}
				}
			}
			if !card.written() {
				dropped++
				continue
			}
			s.putSponsorCard(card)
			kept++
		}
		job.Done, job.Note = now, clip(a.Note, 200)
	}
	s.note("sponsors", "sponsor read: %d lead(s) written, %d dropped", kept, dropped)
	s.saveSponsors()
	writeJSONTo(w, map[string]any{"kept": kept, "dropped": dropped})
}

// handleSponsorAgain: POST /admin/sponsors/again with b. One more read of
// that brand today, on top of the daily one.
func (s *Server) handleSponsorAgain(w http.ResponseWriter, r *http.Request) {
	brand, now := r.FormValue("b"), s.now().UTC()
	if !knownBrand(brand) {
		http.Error(w, "no such brand", http.StatusNotFound)
		return
	}
	s.mu.Lock()
	s.sp.Jobs = append(s.sp.Jobs, &sponsorJob{ID: "sp-" + brand + "-" + now.Format(dayFmt) + "-" + now.Format("150405"), Brand: brand, Day: now.Format(dayFmt), Asked: now})
	s.saveSponsors()
	s.mu.Unlock()
	http.Redirect(w, r, s.cfg.Base+"/sponsors?b="+brand, http.StatusSeeOther)
}

func knownBrand(brand string) bool {
	for _, b := range sponsorBrands {
		if b.ID == brand {
			return true
		}
	}
	return false
}

func (s *Server) dropSponsorCard(brand, key string) {
	var keep []*SponsorCard
	for _, c := range s.sp.Cards[brand] {
		if c.Key != key {
			keep = append(keep, c)
		}
	}
	s.sp.Cards[brand] = keep
}

// putSponsorCard adds or renews what was written for a lead.
func (s *Server) putSponsorCard(card *SponsorCard) {
	if old := s.cardOf(card.Brand, card.Key); old != nil {
		card.FirstSeen, card.Reached = old.FirstSeen, old.Reached
		s.dropSponsorCard(card.Brand, card.Key)
	}
	s.sp.Cards[card.Brand] = append(s.sp.Cards[card.Brand], card)
}

// sponsorRow is one lead on the page.
type sponsorRow struct {
	Candidate
	Card    *SponsorCard // nil until the daily read wrote for it
	KnownTo *leadPerson  // who the first text is for
	Reached time.Time
	Logo    string // the kept logo's file name
}

// Initial: the letter shown where a logo is missing.
func (r *sponsorRow) Initial() string {
	for _, c := range r.Company {
		if unicode.IsLetter(c) || unicode.IsDigit(c) {
			return strings.ToUpper(string(c))
		}
	}
	return "?"
}

// Others: the addresses of the people we know there besides the one being
// written to, for the Cc line.
func (r *sponsorRow) Others(to string) string {
	var out []string
	for _, p := range r.People {
		if p.Email != "" && !strings.EqualFold(p.Email, to) {
			out = append(out, p.Email)
		}
	}
	return strings.Join(out, ",")
}

// BossMail: the decision maker's address, when a first email to them is allowed.
func (r *sponsorRow) BossMail() string {
	if r.Card == nil || r.Card.Boss == nil || noColdMail[r.Card.Boss.Country] {
		return ""
	}
	return r.Card.Boss.Email
}

type sponsorsData struct {
	Brands []tab
	Cur    string
	Name   string
	Rows   []*sponsorRow
	Done   []*sponsorRow // reached out already
	Gmail  string
	Offer  *offerEvent // the event to offer, nil when none is far enough ahead
	Total  int         // leads of this brand not reached yet
	More   int         // how many "Show more" asks for, 0 when all are shown
	Last   string
	Off    bool // the outreach log is not connected
}

// handleSponsors: GET /sponsors?b=<brand>&n=<how many>.
func (s *Server) handleSponsors(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	pool := s.sponsorPool(now)
	cur := r.URL.Query().Get("b")
	if !knownBrand(cur) {
		cur = sponsorBrands[0].ID
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	if n < sponsorTop {
		n = sponsorTop
	}
	d := sponsorsData{Gmail: s.cfg.Gmail, Cur: cur, Off: s.cfg.HumansDB == ""}
	if ev := s.offerEvents(cur, now); len(ev) > 0 {
		d.Offer = &ev[0]
	}
	s.mu.Lock()
	for _, b := range sponsorBrands {
		d.Brands = append(d.Brands, tab{ID: b.ID, Name: b.Name, Brand: b.ID, On: b.ID == cur})
		if b.ID == cur {
			d.Name = b.Name
		}
	}
	// The rows "Show more" brings next: their logos are fetched ahead.
	var ahead []string
	for _, c := range s.sponsorLeads(pool, cur, now) {
		row := &sponsorRow{Candidate: c}
		if card := s.cardOf(cur, c.Key); card != nil {
			row.Reached = card.Reached
			if card.written() {
				cc := *card
				row.Card = &cc
				if cc.Name != "" {
					row.Company = cc.Name
				}
				for i := range row.People {
					if row.People[i].Name == cc.Known.To {
						row.KnownTo = &row.People[i]
					}
				}
			}
		}
		if !row.Reached.IsZero() {
			d.Done = append(d.Done, row)
			continue
		}
		d.Total++
		if len(d.Rows) < n {
			d.Rows = append(d.Rows, row)
		} else if len(ahead) < 2*sponsorTop {
			ahead = append(ahead, c.Domain)
		}
	}
	if d.Total > len(d.Rows) {
		d.More = len(d.Rows) + sponsorTop
	}
	for i := len(s.sp.Jobs) - 1; i >= 0; i-- {
		if j := s.sp.Jobs[i]; j.Brand == cur && !j.Done.IsZero() {
			d.Last = j.Done.UTC().Format("Mon 2 Jan 15:04") + " UTC"
			break
		}
	}
	s.mu.Unlock()
	for _, row := range append(append([]*sponsorRow{}, d.Rows...), d.Done...) {
		if len(row.People) > 3 {
			row.People = row.People[:3]
		}
		if row.Logo = s.logoOf(row.Domain); row.Logo == "" && row.Domain != "" {
			go s.wantLogo(row.Domain) // there on the next visit
		}
	}
	for _, dom := range ahead {
		if dom != "" && s.logoOf(dom) == "" {
			go s.wantLogo(dom)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "sponsors.html", d); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleSponsorAction: POST /admin/sponsor/{action} with key and b.
// reached: the first step was taken; back: undo it; never: not a fit, for
// good, with a reason.
func (s *Server) handleSponsorAction(w http.ResponseWriter, r *http.Request) {
	key, brand := r.FormValue("key"), r.FormValue("b")
	now := s.now()
	var lead *Candidate
	for _, c := range s.sponsorPool(now) {
		if c.Brand == brand && c.Key == key {
			cc := c
			lead = &cc
		}
	}
	s.mu.Lock()
	card := s.cardOf(brand, key)
	found := card != nil || lead != nil
	if found {
		switch r.PathValue("action") {
		case "reached":
			if card == nil {
				// Reached out before anything was written for it.
				card = &SponsorCard{Key: key, Domain: lead.Domain, Company: lead.Company, Brand: brand, FirstSeen: now, Refreshed: now}
				s.sp.Cards[brand] = append(s.sp.Cards[brand], card)
			}
			card.Reached = now
		case "back":
			if card != nil {
				card.Reached = time.Time{}
			}
		case "never":
			s.sp.Never[key] = sponsorMark{Reason: clip(strings.TrimSpace(r.FormValue("reason")), 80), At: now}
			for _, b := range sponsorBrands {
				s.dropSponsorCard(b.ID, key)
			}
			s.note("sponsors", "a sponsor lead was crossed out: %s", s.sp.Never[key].Reason)
		}
		s.saveSponsors()
	}
	s.mu.Unlock()
	if !found {
		http.Error(w, "no such lead", http.StatusNotFound)
		return
	}
	http.Redirect(w, r, s.cfg.Base+"/sponsors?b="+brand, http.StatusSeeOther)
}

// handleOldSponsorPage: the lists that used to have their own pages are
// part of the board now; their addresses keep answering.
func (s *Server) handleOldSponsorPage(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, s.cfg.Base+"/sponsors", http.StatusSeeOther)
}

// composeURL: a Gmail compose window with the address, subject and text
// filled in. Nothing is sent until the person presses send.
func composeURL(gmail, to, subject, body string) string {
	q := url.Values{"view": {"cm"}, "fs": {"1"}, "to": {to}, "su": {subject}, "body": {body}}
	if gmail != "" {
		q.Set("authuser", gmail)
	}
	return "https://mail.google.com/mail/?" + q.Encode()
}

// retryAfter: a company refused only because its pages could not be read
// ("Not verified: ...") is asked about again this soon.
const retryAfter = 3 * 24 * time.Hour

// passedAt: when a refusal counts from. One that says the check could not
// be made is dated back, so it runs out after retryAfter.
func passedAt(reason string, now time.Time) time.Time {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(reason)), "not verified") {
		return now.Add(retryAfter - sponsorPassFor)
	}
	return now
}
