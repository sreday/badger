package snore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Claude scouts on Marek's subscription: the routine "Snore: scout
// speakers" GETs /api/queue with the bearer token, scouts each topic it is
// handed and POSTs /api/answers. Every day from 05:00 UTC the clock queues a
// job per tab with room (every tab on Saturdays); "Scout now" on the board queues them at once. The routine looks
// at the queue every hour on its own schedule (no API trigger, no token).

const (
	jobLife  = 7 * 24 * time.Hour
	leaseFor = 90 * time.Minute // a job handed to a run isn't handed to the next one before this
	perQueue = 3                // topics per GET: each one is a long search
	daily0   = 5                // the day's jobs are queued from 05:00 UTC
)

type Job struct {
	ID     string
	Topic  string
	Week   string
	Auto   bool
	Asked  time.Time
	Handed time.Time
	Done   time.Time
	Answer string
}

func (j *Job) pending(now time.Time) bool { return j.Done.IsZero() && now.Sub(j.Asked) < jobLife }

func (j *Job) free(now time.Time) bool {
	return j.pending(now) && (j.Handed.IsZero() || now.Sub(j.Handed) > leaseFor)
}

func (s *Server) saveJobs() {
	if len(s.jobs) > 400 {
		s.jobs = s.jobs[len(s.jobs)-400:]
	}
	writeJSON(filepath.Join(s.dir, "jobs.json"), s.jobs)
}

// queue adds a job per topic (once per topic and week). The routine picks
// them up on its own schedule: Snore never starts it through the API.
func (s *Server) queue(topics []string, week string, auto bool, now time.Time) int {
	n := 0
	for _, t := range topics {
		dup := false
		for _, j := range s.jobs {
			if j.Topic == t && (j.pending(now) || (auto && j.Auto && j.Week == week)) {
				dup = true
				if !auto && j.pending(now) && !j.Handed.IsZero() {
					j.Handed = time.Time{} // a button press: hand it out again now (the last run may have stopped)
					n++
				}
			}
		}
		if dup {
			continue
		}
		s.jobs = append(s.jobs, &Job{ID: newID(), Topic: t, Week: week, Auto: auto, Asked: now})
		n++
	}
	if n > 0 {
		s.saveJobs()
	}
	return n
}

// dueJobs: every day from 05:00 UTC, a job for each tab that has room (fewer
// than MaxPerTopic cards); on Saturdays, every tab. The routine's own
// schedule picks them up.
func (s *Server) dueJobs(now time.Time) {
	u := now.UTC()
	if u.Hour() < daily0 {
		return
	}
	var ids []string
	for _, t := range s.fp.Topics(now) {
		if u.Weekday() == time.Saturday || len(s.st.Cards[t.ID]) < MaxPerTopic {
			ids = append(ids, t.ID)
		}
	}
	if n := s.queue(ids, u.Format(dayFmt), true, now); n > 0 {
		s.note("queue", "Daily scouting queued for %d tab(s)", n)
	}
}

// Instructions go out with every queue. The private recipe (sources,
// weights) is appended from the data dir: it is not in this repo.
const Instructions = `You scout speaker prospects for Mark, who runs the conferences SREday, LLMday, PLATFORMday (in person, in many cities), the Prompt Engineering Conference and Conf42 (online). Each job is one topic. Return the best public voices for it: people we would love on stage and who would say yes.

GATES. Use only open gates: public web pages and public APIs that need no login and whose terms allow reading them. Never log in anywhere. Never open linkedin.com or x.com pages yourself: a LinkedIn or X follower count comes only from what a search engine shows about the profile (the result snippet), and is marked as such. Follow the PRIVATE RECIPE below for the list of gates and the weights.

LOCAL ONLY (in-person topics). We don't pay for travel. Recommend someone only for our cities within easy reach of the city they work from (same metro first, then a few hours by train or car in the same country or region; never across continents). Their work city is what they state publicly in a speaker bio or profile, their employer's office, or where they spoke recently. Never look up where someone lives, and never judge by how a name sounds. If no work city is public, leave them out of in-person topics. Online (Conf42) topics have no location rule. IN PERSON FIRST: someone local to one of our cities belongs on the in-person tab, even when they are already on a Conf42 tab (Snore moves them). Conf42 is where speakers from places we hold no conferences in shine: for Conf42 topics, favour great speakers far from our cities.

NEVER RECOMMEND anyone in "never" (marked not a fit, or invited lately), and nobody who is or was on one of our lineups: POST {"names": [...], "topic": "<the job's topic_id>"} to /api/check and drop every name it returns in "excluded".

REACH, the speaker's public karma, every number you can find, weighted in this order: LinkedIn followers (heaviest), the published attendee counts of the conferences they spoke at in the last two years, then X, Bluesky and Mastodon followers, then GitHub and YouTube. A count from "confirmed" in the job was typed in by the team: use it as it is.

CANDIDATES. Each job carries "candidates": speakers Snore already read from public conference speaker feeds (name, tagline, bio, photo, links, talks with the conference and date). Start there: judge them first, and take their photo, LinkedIn URL, links and talks as given. A tagline is often "title @ company". Their work city is often in the bio; if it isn't, a quick search can find what they state publicly.

FEEDS. Snore can only judge speakers it can read. In each run, look for new public speaker sources (see the PRIVATE RECIPE for the kinds, what each looks like and how to find its id) for events in or near the job's cities, or on the job's topic, in the last two years or coming up. POST them to /api/sources as {"sources": [{"kind": "<one of the kinds the PRIVATE RECIPE lists; leave it out for the default>", "id": "", "event": "<conference name and year>", "city": "", "country": "", "date": "YYYY-MM-DD", "url": "<the conference's page>"}]}. Skip what is in "known_feeds" (kind:id). Snore reads each one at once; then GET /api/candidates?topic=<the job's topic_id> for the fresh candidate list.


RELEVANT, AND WOMEN FIRST. Only people who really speak on the job's topic. Among comparably strong people, put women first, and aim for at least half the cards being women when the field allows it: Mark wants more women on our stages. Judge only by what is published: a stated she/her, or the pronouns their bio is written in. Never by a name or a photo. Candidates already carry "pronouns" where it is known.
ANSWER every job by its id in one POST to /api/answers:
{"model": "<your model>", "answers": [{"id": "<job id>", "note": "<one line: what you looked at>", "cards": [CARD, ...]}]}
Up to 40 cards per topic, best first (Snore keeps the top 30 after its own checks). A CARD:
{"name": "", "photo": "<https URL of their headshot, if a page you read shows one>", "title": "<current job title>", "company": "<current affiliation>",
 "base": {"city": "", "country": "", "sure": "stated|inferred|unknown"}, "our_cities": ["<only names from the job's cities list>"],
 "linkedin_url": "<their profile URL as linked from a gate or a search result>", "email": "<see CONTACTS>", "keynote": <true if they gave a keynote at a conference>, "pronouns": "<she, he or they, only as they or the event publish it; empty when not stated>",
 "reach": {"linkedin": 0, "talk_audiences": [{"event": "", "attendees": 0}], "x": 0, "bluesky": 0, "mastodon": 0, "github": 0, "youtube": 0},
 "talks": [{"title": "", "event": "<the conference's name and year, e.g. SomeConf Europe 2026; required, never the talk title, never a placeholder like \"a listed conference\">", "date": "YYYY-MM-DD", "url": "<the talk's own page on the conference site>"}], "topics": ["<their recent topics, short>"],
 "match": 0, "parts": {"reach": 0, "topic": 0, "local": 0, "activity": 0}, "why": "<one short line>", "sources": ["<every gate URL you used>"]}
match and each part are 0-100. Unknown numbers are 0. NO REPEATS: everyone in "on_board" is already on the board (on this tab or another). Skip them, don't research them again: spend the time finding new people. Snore drops any repeat.
CONTACTS, a cheap lookup, never a research task. LinkedIn: one web search per person, "<name> <company> LinkedIn"; take a linkedin.com/in/ address from the results only when the name matches and the company or title matches too. Never open linkedin.com, never search twice. Email: only an address you already saw on a page you read anyway (their own site, a speaker bio, a talk page); never search for an email, never guess one from a name pattern. Leave a field empty when there is nothing.
The queue also carries "contacts": people already on the board nobody has looked up. Do the same cheap lookup for each (follower counts only from what that one search shows) and POST {"contacts": [{"name": "", "linkedin_url": "", "email": "", "reach": {"linkedin": 0, "x": 0, "bluesky": 0, "mastodon": 0, "youtube": 0, "github": 0}}]} to /api/contacts, one entry for every name asked, with empty fields and 0 counts when you found nothing public. Follower counts follow the REACH rules above.
Then GET /api/queue again and go on until "jobs" and "contacts" are both empty.`

type qCard struct {
	Name     string `json:"name"`
	Title    string `json:"title,omitempty"`
	Company  string `json:"company,omitempty"`
	Base     Base   `json:"base"`
	Match    int    `json:"match"`
	LinkedIn int    `json:"linkedin,omitempty"`
	Src      string `json:"linkedin_source,omitempty"`
}

func (s *Server) jobData(j *Job, t Topic) map[string]any {
	d := map[string]any{"id": j.ID, "topic": t.Name, "topic_id": t.ID, "online": t.Online}
	if !t.Online {
		d["brand"] = brandNames[t.Brand]
		d["cities"] = t.Cities
	} else {
		d["brand"] = "Conf42"
	}
	var cur []qCard
	for _, c := range s.st.Cards[t.ID] {
		cur = append(cur, qCard{c.Name, c.Title, c.Company, c.Base, c.Match, c.Reach.LinkedIn, c.Reach.LinkedInSrc})
	}
	d["current_cards"] = cur
	var on []string
	for id, cs := range s.st.Cards {
		if !t.Online && strings.HasPrefix(id, "conf42-") {
			continue // in person beats online: Conf42 speakers may move here
		}
		for _, c := range cs {
			on = append(on, c.Name)
		}
	}
	d["on_board"] = on
	d["candidates"] = s.candidates(t, s.now())
	var known []string
	for _, src := range s.sources {
		known = append(known, src.kind()+":"+src.ID)
	}
	d["known_feeds"] = known
	var never []string
	for k := range s.st.Rejected[t.ID] {
		never = append(never, k)
	}
	for k, at := range s.st.Invited {
		if s.now().Sub(at) < invitedFor {
			never = append(never, k)
		}
	}
	d["never"] = never
	conf := map[string]int{}
	for k, c := range s.st.Confirmed {
		conf[k] = c.Count
	}
	d["confirmed_linkedin"] = conf
	return d
}

func (s *Server) instructions() string {
	out := Instructions
	if b, err := os.ReadFile(filepath.Join(s.dir, "recipe.txt")); err == nil && len(bytes.TrimSpace(b)) > 0 {
		out += "\n\nPRIVATE RECIPE\n" + strings.TrimSpace(string(b))
	}
	return out
}

// handleQueue: GET /api/queue.
func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.dueJobs(now)
	topics := map[string]Topic{}
	for _, t := range s.fp.Topics(now) {
		topics[t.ID] = t
	}
	jobs := []map[string]any{}
	remaining := 0
	for _, j := range s.jobs {
		if !j.free(now) {
			continue
		}
		t, ok := topics[j.Topic]
		if !ok {
			j.Done, j.Answer = now, "topic gone"
			continue
		}
		if len(jobs) >= perQueue {
			remaining++
			continue
		}
		j.Handed = now
		jobs = append(jobs, s.jobData(j, t))
	}
	contacts, more := s.contactData(now)
	remaining += more
	s.saveJobs()
	if len(contacts) > 0 {
		s.save()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"today": now.UTC().Format("Monday 2 January 2006"), "instructions": s.instructions(), "jobs": jobs, "contacts": contacts, "remaining": remaining})
}

// handleCheck: POST /api/check {"names": [...]} -> {"excluded": [...]}: who
// is or was on one of our lineups, marked not a fit or invited lately.
func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Names []string `json:"names"`
		Topic string   `json:"topic"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		http.Error(w, "Bad JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	ex := s.exclusions()
	now := s.now()
	out := []string{}
	board := s.onBoard("")
	if t, ok := s.topic(in.Topic); ok {
		board = s.onBoardFor(t)
		for _, c := range s.st.Cards[t.ID] {
			board[c.Key] = true
		}
	}
	for _, n := range in.Names {
		hit := false
		if board[normName(n)] {
			hit = true // already on the board
		}
		for _, k := range nameKeys(n) {
			if _, ok := ex.lineups[k]; ok {
				hit = true
			}
			if at, ok := ex.invited[k]; ok && now.Sub(at) < invitedFor {
				hit = true
			}
			for t, rej := range ex.rejected {
				if _, ok := rej[k]; ok && (in.Topic == "" || t == in.Topic) {
					hit = true
				}
			}
		}
		if hit {
			out = append(out, n)
		}
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"excluded": out})
}

// handleAnswers: POST /api/answers.
func (s *Server) handleAnswers(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Model   string `json:"model"`
		Answers []struct {
			ID    string  `json:"id"`
			Note  string  `json:"note"`
			Cards []*Card `json:"cards"`
		} `json:"answers"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&in); err != nil {
		http.Error(w, "Bad JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	now := s.now()
	ex := s.exclusions()
	topics := map[string]Topic{}
	for _, t := range s.fp.Topics(now) {
		topics[t.ID] = t
	}
	taken, kept := 0, 0
	for _, a := range in.Answers {
		var j *Job
		for _, o := range s.jobs {
			if o.ID == a.ID && o.Done.IsZero() {
				j = o
			}
		}
		if j == nil {
			continue
		}
		t, ok := topics[j.Topic]
		if !ok {
			continue
		}
		taken++
		before := map[string]bool{}
		for _, c := range s.st.Cards[t.ID] {
			before[c.Key] = true
		}
		list := merge(t, s.st.Cards[t.ID], a.Cards, ex, s.onBoardFor(t), s.st.Confirmed, now)
		if !t.Online {
			s.leaveOnline(t, list)
		}
		var added []string
		for _, c := range list {
			if !before[c.Key] {
				added = append(added, c.Name)
			}
		}
		if len(added) == 0 {
			s.note("scout", "%s: Claude answered, no new speakers", t.Name)
		} else {
			s.note("scout", "%s: %d new: %s", t.Name, len(added), strings.Join(added, ", "))
		}
		for _, c := range list {
			s.stampHumans(c)
			s.stampPronouns(c)
		}
		s.st.Cards[t.ID] = list
		s.st.Fresh[t.ID] = now
		kept += len(list)
		note := strings.TrimSpace(a.Note)
		if len(note) > 300 {
			note = note[:300]
		}
		j.Done, j.Answer = now, fmt.Sprintf("%d cards kept (%s). %s", len(list), in.Model, note)
	}
	s.save()
	s.saveJobs()
	s.mu.Unlock()
	if taken > 0 {
		s.photosLater()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"taken": taken, "cards_kept": kept})
}

// onBoard: the speakers on every tab but this one. Each person shows on one
// tab only.
func (s *Server) onBoard(except string) map[string]bool {
	out := map[string]bool{}
	for id, cs := range s.st.Cards {
		if id == except {
			continue
		}
		for _, c := range cs {
			out[c.Key] = true
		}
	}
	return out
}

// onBoardFor: who counts as already on the board for a job on t. In person
// beats online: for an in-person tab, a speaker on a Conf42 tab is not a
// repeat (Snore moves them over when Claude finds them local).
func (s *Server) onBoardFor(t Topic) map[string]bool {
	out := map[string]bool{}
	for id, cs := range s.st.Cards {
		if id == t.ID || (!t.Online && strings.HasPrefix(id, "conf42-")) {
			continue
		}
		for _, c := range cs {
			out[c.Key] = true
		}
	}
	return out
}

// leaveOnline: a speaker who made an in-person tab leaves the Conf42 tabs.
func (s *Server) leaveOnline(t Topic, list []*Card) {
	here := map[string]bool{}
	for _, c := range list {
		here[c.Key] = true
	}
	for id, cs := range s.st.Cards {
		if !strings.HasPrefix(id, "conf42-") {
			continue
		}
		var keep []*Card
		for _, c := range cs {
			if here[c.Key] {
				s.note("move", "%s moved to %s (in person beats online)", c.Name, t.Name)
				continue
			}
			keep = append(keep, c)
		}
		s.st.Cards[id] = keep
	}
}
