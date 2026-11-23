package snore

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

// The Sponsors board: companies worth asking to sponsor, each with the
// proof for it. The server keeps the candidate pool and a queue; once a
// day Claude reads the dossiers, checks each company against the gates and
// answers with cards. A card is kept only when a claim on it can be read,
// word for word, on the page it cites.

const (
	sponsorsMax     = 30               // cards per brand
	sponsorNew      = 5                // new companies handed out per brand and day
	sponsorRecheck  = 5                // older cards looked at again per brand and day
	sponsorLease    = 90 * time.Minute // how long a handed-out job is Claude's
	sponsorStale    = 30 * 24 * time.Hour
	sponsorPassFor  = 90 * 24 * time.Hour // a company Claude turned down is not asked about again for this long
	sponsorHour     = 5                   // UTC: when the day's jobs are queued
	claimsMax       = 6
	quoteMin        = 12
	offerFrom       = 6 * 7 * 24 * time.Hour // an event nearer than this is too late to offer
	offerUntil      = 9 * month
	sponsorJobsKept = 60
)

// Claim is one fact on a card, with where it can be read.
type Claim struct {
	Text    string `json:"text"`
	Quote   string `json:"quote"` // word for word from the page
	URL     string `json:"url"`
	Date    string `json:"date,omitempty"` // when the page says it happened
	Checked bool   `json:"checked,omitempty"`
}

type SponsorPerson struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Profile string `json:"profile,omitempty"`
	Email   string `json:"email,omitempty"`   // only one they published themselves
	Proof   string `json:"proof,omitempty"`   // a page that shows the role today
	Country string `json:"country,omitempty"` // where they work, two letters
	Start   bool   `json:"start,omitempty"`   // the one to write to first
	Known   string `json:"known,omitempty"`   // how we know them already
}

type SponsorCard struct {
	Key     string `json:"key"`
	Domain  string `json:"domain,omitempty"`
	Company string `json:"company"`
	Brand   string `json:"brand"`
	Label   string `json:"label"`
	Level   int    `json:"level"`
	Owner   string `json:"owner,omitempty"`

	Name     string          `json:"name,omitempty"`      // the company's name as its own site writes it (from the answer)
	Sells    string          `json:"sells,omitempty"`     // what they sell, a few words
	Reason   string          `json:"reason,omitempty"`    // why them, why now: one phrase
	Kind     string          `json:"kind,omitempty"`      // vendor, employer, consultancy
	Why      []Claim         `json:"why"`                 // why them: audience and event-space proof
	Money    string          `json:"money,omitempty"`     // strong, some, none
	MoneyWhy []Claim         `json:"money_why,omitempty"` // what the money word rests on
	Offer    []string        `json:"offer,omitempty"`     // our events that fit
	Risk     string          `json:"risk,omitempty"`
	People   []SponsorPerson `json:"people,omitempty"`
	Channel  string          `json:"channel,omitempty"` // teammate, thread, mail, form, linkedin
	Subject  string          `json:"subject,omitempty"`
	Mail     string          `json:"mail,omitempty"`  // the first email, or the reply in the old thread
	Note     string          `json:"note,omitempty"`  // a short note for a connection request
	Brief    string          `json:"brief,omitempty"` // for the teammate who holds the contact
	FormURL  string          `json:"form_url,omitempty"`
	Thread   string          `json:"thread,omitempty"`      // the old conversation
	With     []string        `json:"thread_with,omitempty"` // who it was with

	FirstSeen time.Time `json:"first_seen"`
	Refreshed time.Time `json:"refreshed"`
	Reached   time.Time `json:"reached,omitempty"`
	Model     string    `json:"model,omitempty"`
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
	Cards  map[string][]*SponsorCard `json:"cards"`  // brand -> cards, best first
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

const SponsorInstructions = `Each job is one brand with its upcoming events and candidate companies, each with what we hold on it.
For every candidate answer with a card or a refusal, following the private recipe below. Every claim on a card
needs a word-for-word quote, the address of the page it stands on (https) and the date the page gives: a claim
whose quote cannot be read on its page is thrown away, and a card left without one is refused.

Answer: POST {"model": "<you>", "answers": [{"id": "<job id>", "note": "<one line>", "cards": [CARD...],
"refused": [{"key": "<candidate key>", "reason": "<a few words>"}]}]}
CARD: {"key": "<candidate key, unchanged>", "name": "<the company's name as its own website writes it>",
"sells": "<what they sell, 8 words at most, nothing else>", "reason": "<why them and why now: ONE phrase, 14 words at most>",
"kind": "vendor|employer|consultancy",
"why": [{"text": "", "quote": "", "url": "", "date": "YYYY-MM-DD"}], "money": "strong|some|none",
"money_why": [CLAIM...], "offer": ["<event name>"], "risk": "", "form_url": "",
"people": [{"name": "", "title": "", "profile": "", "email": "", "proof": "", "country": "GB", "start": true, "known": ""}],
"subject": "", "mail": "", "note": "", "brief": ""}
Every candidate of a job is either a card or refused.
The "company" of a candidate is what people typed and can be the new employer of someone who left: the company
is the one behind the key's domain. A candidate's "talk" is an old conversation and "with" says who it was with.
A reply in it goes to one of them: put that person first in "people" with "start": true, but only when a page
shows they still work there. If none does, the text is a first message to someone new, not a reply.`

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
		e := offerEvent{Date: f.Start.Format(dayFmt), Online: f.Brand == "conf42"}
		if e.Online {
			e.Name = "Conf42 " + f.Name
		} else {
			e.Name, e.City = brandTitles[f.Brand]+" "+f.Name, f.Name
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

// handleSponsorQueue: GET /api/sponsors/queue.
func (s *Server) handleSponsorQueue(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	pool := s.sponsorPool(now) // takes the lock itself
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dueSponsorJobs(now)
	jobs := []map[string]any{}
	if s.sponsorRecipe() == "" {
		// Without the recipe Claude would judge blind: hand out nothing.
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
		onBoard := map[string]*SponsorCard{}
		for _, c := range s.sp.Cards[j.Brand] {
			onBoard[c.Key] = c
		}
		var fresh, again []Candidate
		for _, c := range pool {
			if c.Brand != j.Brand {
				continue
			}
			if _, never := s.sp.Never[c.Key]; never {
				continue
			}
			if p, ok := s.sp.Passed[c.Key]; ok && now.Sub(p.At) < sponsorPassFor {
				continue
			}
			if card, ok := onBoard[c.Key]; ok {
				// A card from before the one-phrase reason is made again.
				if card.Reached.IsZero() && (now.Sub(card.Refreshed) > sponsorStale || card.Reason == "") && len(again) < sponsorRecheck {
					again = append(again, c)
				}
				continue
			}
			if len(fresh) < sponsorNew {
				fresh = append(fresh, c)
			}
		}
		if len(fresh)+len(again) == 0 {
			j.Done, j.Note = now, "no candidate left"
			continue
		}
		j.Handed, j.Keys = now, nil
		for _, c := range append(append([]Candidate{}, fresh...), again...) {
			j.Keys = append(j.Keys, c.Key)
		}
		jobs = append(jobs, map[string]any{"id": j.ID, "brand": brandTitles[j.Brand], "events": events,
			"candidates": fresh, "look_again": again})
	}
	s.saveSponsors()
	writeJSONTo(w, map[string]any{"today": now.UTC().Format(dayFmt), "instructions": s.sponsorInstructions(), "jobs": jobs})
}

func writeJSONTo(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// foldText: letters and digits only, lower case, single spaces: how a
// quote and a page are compared.
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

// checkClaims keeps the claims whose quote stands on the page they cite.
// Called without the lock: it reads the pages, politely.
func (s *Server) checkClaims(in []Claim) []Claim {
	var out []Claim
	pages := map[string]string{}
	for _, c := range in {
		if len(out) >= claimsMax {
			break
		}
		c.Text, c.Quote, c.URL = clip(strings.TrimSpace(c.Text), 240), strings.TrimSpace(c.Quote), strings.TrimSpace(c.URL)
		q := foldText(c.Quote)
		if !strings.HasPrefix(c.URL, "https://") || len(q) < quoteMin || c.Text == "" {
			continue
		}
		page, ok := pages[c.URL]
		if !ok {
			if body, err := s.get(c.URL, nil, true); err == nil {
				page = foldText(plain(string(body)))
			}
			pages[c.URL] = page
		}
		if page == "" || !strings.Contains(page, q) {
			continue
		}
		c.Quote, c.Checked = clip(c.Quote, 300), true
		out = append(out, c)
	}
	return out
}

// noColdMail: countries where a first email to a named person needs their
// consent, also between companies.
var noColdMail = map[string]bool{"DE": true, "PL": true, "ES": true, "AT": true, "IT": true}

type sponsorAnswer struct {
	ID      string        `json:"id"`
	Note    string        `json:"note"`
	Cards   []SponsorCard `json:"cards"`
	Refused []struct {
		Key    string `json:"key"`
		Reason string `json:"reason"`
	} `json:"refused"`
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
		pool[c.Key] = c
	}
	// Read the cited pages before taking the lock.
	type checked struct{ why, money []Claim }
	proof := map[string]checked{}
	for _, a := range in.Answers {
		for _, c := range a.Cards {
			proof[a.ID+"\x00"+c.Key] = checked{s.checkClaims(c.Why), s.checkClaims(c.MoneyWhy)}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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
			cand, ok := pool[c.Key]
			if !ok || !handed[c.Key] {
				dropped++
				continue
			}
			p := proof[a.ID+"\x00"+c.Key]
			money := strings.ToLower(strings.TrimSpace(c.Money))
			if len(p.why) == 0 || money == "negative" {
				reason := "no proof that could be read on the cited pages"
				if money == "negative" {
					reason = "money: negative"
				}
				s.sp.Passed[c.Key] = sponsorMark{Reason: reason, At: now}
				s.dropSponsorCard(job.Brand, c.Key)
				dropped++
				continue
			}
			card := s.buildSponsorCard(c, cand, job.Brand, p.why, p.money, money, now)
			card.Model = clip(in.Model, 60)
			s.putSponsorCard(card)
			kept++
		}
		job.Done, job.Note = now, clip(a.Note, 200)
	}
	s.note("sponsors", "sponsor read: %d card(s) kept, %d dropped", kept, dropped)
	s.saveSponsors()
	writeJSONTo(w, map[string]any{"kept": kept, "dropped": dropped})
}

// buildSponsorCard: what we hold comes from the pool, what Claude found is
// cleaned and cut to size (lock held).
func (s *Server) buildSponsorCard(c SponsorCard, cand Candidate, brand string, why, moneyWhy []Claim, money string, now time.Time) *SponsorCard {
	card := &SponsorCard{Key: cand.Key, Domain: cand.Domain, Company: cand.Company, Brand: brand, Label: cand.Label,
		Level: cand.Level, Owner: cand.Owner, Why: why, MoneyWhy: moneyWhy, FirstSeen: now, Refreshed: now}
	card.Sells, card.Risk = clip(strings.TrimSpace(c.Sells), 90), clip(strings.TrimSpace(c.Risk), 200)
	card.Reason = clip(strings.TrimSpace(c.Reason), 140)
	if card.Reason == "" && len(why) > 0 {
		card.Reason = why[0].Text
	}
	// Claude read the company's own site: its name for the company counts
	// when it matches the domain, or when the name we hold does not.
	if n := clip(strings.TrimSpace(c.Name), 60); n != "" && cand.Domain != "" && (nameFits(cand.Domain, n) || !nameFits(cand.Domain, cand.Company)) {
		card.Company = n
	}
	switch k := strings.ToLower(strings.TrimSpace(c.Kind)); k {
	case "vendor", "employer", "consultancy":
		card.Kind = k
	}
	if money == "strong" || money == "some" || money == "none" {
		card.Money = money
	}
	for _, o := range c.Offer {
		if o = clip(strings.TrimSpace(o), 80); o != "" && len(card.Offer) < 4 {
			card.Offer = append(card.Offer, o)
		}
	}
	if strings.HasPrefix(c.FormURL, "https://") {
		card.FormURL = c.FormURL
	}
	if cand.Talk != nil {
		card.Thread, card.With = cand.Talk.URL, cand.Talk.With
	}
	start := false
	for _, p := range c.People {
		if len(card.People) >= 3 || strings.TrimSpace(p.Name) == "" {
			continue
		}
		q := SponsorPerson{Name: clip(strings.TrimSpace(p.Name), 80), Title: clip(strings.TrimSpace(p.Title), 120), Profile: profileURL(p.Profile),
			Email: cleanEmail(p.Email), Known: clip(strings.TrimSpace(p.Known), 120), Country: strings.ToUpper(clip(strings.TrimSpace(p.Country), 2))}
		if strings.HasPrefix(p.Proof, "https://") {
			q.Proof = p.Proof
		}
		// Someone we do not know yet needs a page that shows the role.
		if q.Known == "" && q.Proof == "" {
			continue
		}
		if p.Start && !start {
			q.Start, start = true, true
		}
		card.People = append(card.People, q)
	}
	if !start && len(card.People) > 0 {
		card.People[0].Start = true
	}
	card.Subject, card.Mail = clip(strings.TrimSpace(c.Subject), 120), clip(strings.TrimSpace(c.Mail), 1200)
	card.Note, card.Brief = clip(strings.TrimSpace(c.Note), 200), clip(strings.TrimSpace(c.Brief), 900)
	card.Channel = sponsorChannel(card)
	return card
}

// sponsorChannel: how the first step goes. A teammate for a warmed up
// company, the old thread for a revived talk; otherwise an email only when
// the start person published one and works where a first email is allowed,
// else the company's own form, else a connection note.
func sponsorChannel(c *SponsorCard) string {
	switch {
	case c.Label == LabelWarmedUp && c.Owner != "" && c.Owner != Organizer:
		return "teammate"
	case c.Thread != "" && threadPerson(c):
		return "thread"
	}
	for _, p := range c.People {
		if p.Start && p.Email != "" && p.Country != "" && !noColdMail[p.Country] {
			return "mail"
		}
	}
	if c.FormURL != "" {
		return "form"
	}
	return "linkedin"
}

// threadPerson: the person to write to first is one the old conversation
// was with. True as well when we do not know who it was with.
func threadPerson(c *SponsorCard) bool {
	if len(c.With) == 0 {
		return true
	}
	for _, p := range c.People {
		if !p.Start {
			continue
		}
		for _, w := range c.With {
			if a, b := foldText(p.Name), foldText(w); a != "" && (a == b || strings.HasPrefix(b, a+" ") || strings.HasPrefix(a, b+" ")) {
				return true
			}
		}
	}
	return false
}

// handleSponsorAgain: POST /admin/sponsors/again with b. One more read of
// that brand today, on top of the daily one.
func (s *Server) handleSponsorAgain(w http.ResponseWriter, r *http.Request) {
	brand, now := r.FormValue("b"), s.now().UTC()
	known := false
	for _, b := range sponsorBrands {
		known = known || b.ID == brand
	}
	if !known {
		http.Error(w, "no such brand", http.StatusNotFound)
		return
	}
	s.mu.Lock()
	s.sp.Jobs = append(s.sp.Jobs, &sponsorJob{ID: "sp-" + brand + "-" + now.Format(dayFmt) + "-" + now.Format("150405"), Brand: brand, Day: now.Format(dayFmt), Asked: now})
	s.saveSponsors()
	s.mu.Unlock()
	http.Redirect(w, r, s.cfg.Base+"/sponsors?b="+brand, http.StatusSeeOther)
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

// putSponsorCard adds or renews a card, keeps the order and the cap.
func (s *Server) putSponsorCard(card *SponsorCard) {
	list := s.sp.Cards[card.Brand]
	for i, c := range list {
		if c.Key == card.Key {
			card.FirstSeen, card.Reached = c.FirstSeen, c.Reached
			list = append(list[:i], list[i+1:]...)
			break
		}
	}
	list = append(list, card)
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Reached.IsZero() != b.Reached.IsZero() {
			return a.Reached.IsZero() // reached ones fold to the end
		}
		if labelRank[a.Label] != labelRank[b.Label] {
			return labelRank[a.Label] < labelRank[b.Label]
		}
		if a.Level != b.Level {
			return a.Level > b.Level
		}
		return a.FirstSeen.Before(b.FirstSeen)
	})
	if len(list) > sponsorsMax {
		list = list[:sponsorsMax]
	}
	s.sp.Cards[card.Brand] = list
}

type sponsorsData struct {
	Brands []tab
	Cur    string
	Name   string
	Cards  []*SponsorCard
	Gmail  string
	Pool   int // candidates waiting for this brand
	Last   string
}

// handleSponsors: GET /sponsors?b=<brand>.
func (s *Server) handleSponsors(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	pool := s.sponsorPool(now)
	cur := r.URL.Query().Get("b")
	d := sponsorsData{Gmail: s.cfg.Gmail}
	s.mu.Lock()
	for _, b := range sponsorBrands {
		if cur == "" {
			cur = b.ID
		}
		d.Brands = append(d.Brands, tab{ID: b.ID, Name: b.Name, Brand: b.ID, On: b.ID == cur})
		if b.ID == cur {
			d.Name = b.Name
		}
	}
	d.Cur = cur
	for _, c := range s.sp.Cards[cur] {
		cc := *c
		d.Cards = append(d.Cards, &cc)
	}
	on := map[string]bool{}
	for _, c := range d.Cards {
		on[c.Key] = true
	}
	for _, c := range pool {
		_, never := s.sp.Never[c.Key]
		if c.Brand == cur && !on[c.Key] && !never {
			d.Pool++
		}
	}
	for i := len(s.sp.Jobs) - 1; i >= 0; i-- {
		if j := s.sp.Jobs[i]; j.Brand == cur && !j.Done.IsZero() {
			d.Last = j.Done.UTC().Format("Mon 2 Jan 15:04") + " UTC"
			break
		}
	}
	s.mu.Unlock()
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
	s.mu.Lock()
	found := false
	for _, c := range s.sp.Cards[brand] {
		if c.Key != key {
			continue
		}
		found = true
		switch r.PathValue("action") {
		case "reached":
			c.Reached = now
		case "back":
			c.Reached = time.Time{}
		}
	}
	if found && r.PathValue("action") == "never" {
		s.sp.Never[key] = sponsorMark{Reason: clip(strings.TrimSpace(r.FormValue("reason")), 80), At: now}
		s.dropSponsorCard(brand, key)
		s.note("sponsors", "a sponsor lead was crossed out: %s", s.sp.Never[key].Reason)
	}
	if found {
		s.saveSponsors()
	}
	s.mu.Unlock()
	if !found {
		http.Error(w, "no such card", http.StatusNotFound)
		return
	}
	http.Redirect(w, r, s.cfg.Base+"/sponsors?b="+brand, http.StatusSeeOther)
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
