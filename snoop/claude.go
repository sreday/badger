package snoop

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sreday/badger/bridge"
	"github.com/sreday/badger/sites"
)

// Claude reads the calls (Marek 2026-11-12: "for Snap and Snoop, I need
// Claude to be as performant as possible, proposing aggressive solutions as
// well, making sure there's no loose ends at any point").
//
// Same model as Snap, but Snoop's own: a Claude Code routine on Marek's
// subscription fetches GET /api/claude/queue (bearer token in
// DataDir/snoop/claude-token), reads each finished call (transcript,
// Gemini's notes, the invite, the Calendly answers) and posts its notes to
// POST /api/claude/answers. Claude's notes then win over Gemini's and the
// on-device ones: a summary, every commitment and open question as a next
// step with an owner and a date, and a few "Push:" plays to get more out of
// the call. A call is read again when its transcript or notes change.

// claudeReadVersion changes when Claude's answer gains a field every call
// needs (v2: the 3 options; v3: the TLDR, the two sides, the proofread; v4: read in context, the points; v5: the house rules; v6: brief forms), so
// calls read before come back once.
const claudeReadVersion = "v7-steps\n"

const (
	claudeBatch  = 4                   // calls per queue: transcripts are long
	claudeWindow = 45 * 24 * time.Hour // calls older than this aren't read any more
	maxInput     = 90000               // characters of one call handed over
	claudeLease  = 15 * time.Minute    // a call handed out isn't handed out again this long, unless it changed
)

type claudeState struct {
	token    string
	mu       sync.Mutex
	events   string
	eventsAt time.Time
	seen     time.Time // the routine's last visit (sides.go: claudeAlive)

	lastFire time.Time // when Snoop last started the reading routine itself
	firedFor string    // the queue it was started for

	// handed: the calls a fetch of the queue took and hasn't answered yet, so
	// two runs that overlap don't read the same calls. Memory only: a restart
	// forgets them, and the calls are handed out again.
	handed map[string]handOut
}

// handOut is one call handed to a run: what it was given (the hash), and when.
type handOut struct {
	read string
	at   time.Time
}

func newClaude(dir string) (*claudeState, error) {
	path := dir + string(os.PathSeparator) + "claude-token"
	if data, err := os.ReadFile(path); err == nil && len(bytes.TrimSpace(data)) >= 32 {
		return &claudeState{token: string(bytes.TrimSpace(data))}, nil
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	tok := hex.EncodeToString(b)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &claudeState{token: tok}, os.WriteFile(path, []byte(tok+"\n"), 0o600)
}

// ClaudeInstructions go out with every queue, so the routine's own prompt
// stays short and a change here ships with Snout.
const ClaudeInstructions = `You read Mark's calls and write the notes he and his team act on. Follow-up emails are drafted from your notes, so a note that is slightly wrong becomes an email that promises the wrong thing. Getting it exactly right matters more than being brief.

HOUSE RULES. The section "HOUSE RULES" at the end of these instructions says who Mark's company is, how it really works, and what never goes into Snoop. It outranks everything else here: read it first.

Each item in "calls" is one finished Google Meet: the invite, the Calendly answers when it was booked as a sales call, the transcript, and context around it: Mark's corrections, his mail with these people, earlier calls with them, and the automatic notes.

WHAT THE PAGE IS FOR. Mark and his team open it right after the call, and again months later to remember who this was. It must read in under a minute: who we met, what was really said, the few actions that take priority. Short bullets, never a wall of text; nobody should feel they need two hours to digest a 30-minute call. The full context stays one click away behind "Read more", so write the short forms for scanning and the long forms for accuracy.

HOW TO READ A CALL.
1. The transcript is the only record of what was said. It is speech-to-text: words are misheard, sentences break off, speakers talk over each other. Read all of it before writing anything.
2. The automatic notes (Gemini, the browser) are a machine summary and are often wrong in exactly the places that matter. Never take a claim from them: find it in the transcript or leave it out.
3. Read the context. "From Mark's mail" and "Earlier calls" show what the relationship really is and what was already agreed. For anything the call touches that you can't judge from it alone (how a similar event, city, sponsor or deal was handled before; who somebody named on the call is), look it up before you write, with the two lookups in "lookups" (Mark's mail, Mark's other calls) and a web search for public facts. A handful of lookups per call is expected; none is rarely right for a call with outside people.
4. For every decision, condition and commitment, be exact about WHAT depends on WHAT, and who said it. "We decide later who travels" and "we decide later whether the event happens" are different promises. When a garbled passage can be read two ways, take the reading that fits the house rules, the context and the rest of the call. If it is still open, say so in plain words ("unclear whether ...") instead of smoothing it into a confident sentence.
5. "Mark's corrections" are the truth: where he says the notes were wrong, he is right, and your new notes must say it his way. "LESSONS" are his corrections on earlier calls: the same mistakes must not come back.
6. Keep to the house rules' off-limits topics: nothing they exclude goes into anything you write, not as a point, not as a warning, not in a brief for an email, and you don't mention that something was left out.
7. Before you send, check every sentence of your answer against the transcript lines it rests on. Anything you can't point to, cut or mark as unclear.

OUR WORDS. The transcript is speech-to-text and Gemini doesn't know Mark's world, so it mishears names. You do know it: SREday, LLMday, PLATFORMday, PEC (Prompt Engineering Conference), Conf42, Reliaburger (reliaburger.com; often misheard, e.g. as "Reliabar"), Hockeystick (their show, hockeystick.show), cfp.ninja, Luma, Calendly, in10t, and Mark's own tools Badger, Snout, Snoop, Snap, Sniff, Snug. People: Mark (Marek) Pawlikowski, Miko Pawlikowski, Aleksandra Los, Anna, Maxim. Plus the cities and events under UPCOMING EVENTS, the people on the invite, and the companies named in the Calendly answers. Read the transcript with that in mind and write the right names everywhere in your answer.

Return for EVERY call:
- id and read: copied exactly from the queue.
- tldr_short: the headline of the card, ONE sentence of at most 25 words: who the guest is and what they want ("Jane Doe, marketing lead at Acme (NYC, observability): wants a booth at SREday NYC, budget decided in January").
- tldr: the full card behind "Read more", the one Mark reads in five months to say "OK, so that was Jane". 3 to 6 plain sentences about the guest, not a recap of the conversation: who they are (role, company, what the company does, where they are based, anything about their background that matters to Mark), what they want from Mark, what Mark wants from them, and where it stands. Start with the person's name. Where the call leaves gaps (their role, company, career), fill them with a web search or two on the person's full name and company, LinkedIn included, and use only what clearly matches this person. For an internal team call, say what the call was for and what was decided.
- points: what was actually said that matters, AT MOST 6, the ones with money, dates, conditions and positions first. Each {"short": the point in at most 14 words, a bullet somebody reads in two seconds, exact even when short ("Venue: Acme hosts if catering is on us"), "more": the full version behind "Read more": one or two plain sentences that would hold up if the guest read them: who said it, the position, and its exact conditions}. Our positions and theirs, where each side stands, what is not agreed yet. This is the part people rely on when writing to the guest, so no guessing. Shortening must never change the meaning: if a condition doesn't fit in 14 words, name the condition and leave the detail to "more".
- checks: for the 3 to 6 most consequential statements in your answer (conditions, commitments, money, dates), {"claim": the statement, "lines": the transcript line numbers it rests on}. A claim with no lines does not belong in the answer.
- links: up to 3 useful links to go under the TLDR, only ones you actually found and that clearly belong to this person or company: their LinkedIn profile, the company website, an event or product they talked about. Each {"title": a short label that fits a button and says whose it is: a person's profile always carries their full name ("Jane Doe LinkedIn", never a bare "LinkedIn"), a company or event carries its name ("Acme website", "DevOpsDays TLV"), "url": the full https address}. Never guess or build a URL; an empty list is fine.
- summary: 3 to 6 plain sentences on the call itself: what was agreed and on what conditions, what was only floated, money and dates mentioned, who has the ball. A follow-up email is drafted from this, so it must be as exact as the points.
- ours: what Mark and his team have to do, AT MOST 5, the most critical for the business first: money, a sponsor or host deal, a speaker or event that depends on it, a promise with a date. Each one short line of at most 16 words: owner first ("Mark:", "Aleksandra:"), then the concrete action, then a due date ("by Fri 14 Nov", worked out from the call date; if nobody named one, pick a sensible one). No explanations in a step: the why is in the points. One of the 5 may be a "Push:" play: a bold, concrete move that gets more out of this relationship, grounded in what was said (ask for the decision with a date, a multi-event bundle, an event near their offices from UPCOMING EVENTS, a speaker or host from their side, going a level up to whoever signs). Never invent prices, discounts or packages that weren't mentioned.
- theirs: what the guest (or their company) said they would do, AT MOST 5, again the ones that matter most to Mark's business first. Each one short line of at most 16 words starting with their first name ("Jane:"), then the action and a date when one was named. On an internal team call, "ours" is Mark's own steps and "theirs" is everybody else's.
  For both lists: leave out small talk and courtesies ("send pictures of the dog", "grab a coffee some time") unless they carry real business. Fewer than 5 is fine; an empty list is fine. Leave out steps the "Already done" list covers.
- fixes: your proofread of the transcript. Every word or name speech-to-text got wrong, as {"wrong": "Reliabar", "right": "Reliaburger"}: product, company, event and people names first, then other clear mishearings that change the meaning. "wrong" is the exact text as it appears in the transcript (a word or short phrase, 3+ letters, never a common word that is right elsewhere in the call); it is replaced everywhere in the transcript. Up to 40; an empty list when the transcript is clean.
- cut: trim the fat. The numbers (the #N at the start of each transcript line) of the lines Snoop must not show: (a) lines that carry nothing: "gimme a minute", "can you hear me", "sorry, you go", "let me share my screen", connection trouble, pure filler and back-channel ("yeah", "right", "okay" on their own), small talk with no bearing on the people or the business; (b) every line on the house rules' off-limits topics, mild ones included. Snoop takes "uh", "umm", "yyy" out of the rest by itself. Keep anything that says who the guest is professionally, what anybody wants, any number, name, date or promise. For (a), when unsure, keep the line; for (b), when unsure, cut it.
- rewrites: for a line that mixes business with off-limits material, so cutting it would lose something that matters: {"line": N, "text": the same line with that material taken out and nothing added}. Keep the speaker's own words otherwise. Check the lines you keep as well as your notes: after cut and rewrites, nothing off-limits may be left anywhere in the transcript.
- actions: ALWAYS exactly 3 options for what Mark does next, shown to him as a flowchart he clicks. The FIRST is your recommendation, the move most likely to win; the other two are genuinely different plays, not variations of one email, and at least one is hard-nosed (ask for the decision with a date, propose closing now, a bundle, going a level up). Each: {"title": short imperative that fits a button (max 6 words, e.g. "Send Jane the Gold package", "Lock SREday NYC by Friday"), "kind": "reply" (a follow-up email to the people on the call, which Claude drafts in Gmail when Mark clicks) or "task" (Mark's own work outside email), "prompt": a self-contained brief for the drafter: who, what to say, the facts from the call it rests on, with every condition worded exactly as in your points, "steps": 2 to 4 bullets that tell Mark at a glance what this move is, in order (who to contact, the one thing to say or ask, what to expect back), each at most 12 words and no full stop; the page shows these and folds the prompt behind "Read more", so never repeat the whole prompt in them}. Prefer replies. For an internal call with nothing to follow up by email, use tasks.
- company_facts: for a sponsor, host or partner call, a quick background check with web search (one or two searches on the company name only): headquarters and offices, rough size, what they sell, recent news. One or two sentences, or "" if not useful. Use it to pick events and plays that fit.

Write plainly, as Mark's sharp chief of staff would: no fluff, no em dashes. Never put transcript text into a web search: search names of people and companies only. Answer with JSON only: {"model": "<your model id>", "calls": [ ... ]}.`

// claudeInput is what Claude reads for one call; the hash says whether it
// changed since Claude's last read (ticks don't count).
func (s *Server) claudeInput(c *Call) (input, hash string) {
	var b strings.Builder
	fmt.Fprintf(&b, "Title: %s\n", c.Title)
	fmt.Fprintf(&b, "When: %s to %s (%s)\n", c.Start.In(s.tz).Format("Mon 2 Jan 2006, 15:04"), c.End.In(s.tz).Format("15:04"), s.tz)
	if c.Organizer != "" {
		fmt.Fprintf(&b, "Organiser: %s\n", c.Organizer)
	}
	if len(c.Attendees) > 0 {
		var who []string
		for _, a := range c.Attendees {
			w := a.Email
			if a.Name != "" {
				w = a.Name + " <" + a.Email + ">"
			}
			if a.Response != "" {
				w += " (" + a.Response + ")"
			}
			who = append(who, w)
		}
		fmt.Fprintf(&b, "Invited: %s\n", strings.Join(who, "; "))
	}
	if c.Lang != "" {
		fmt.Fprintf(&b, "Language: %s (the lines are Meet's live captions in it, not Meet's transcript)\n", c.LangName())
	}
	if len(c.Joined) > 0 {
		fmt.Fprintf(&b, "Joined (a Meet made on the spot, no invite; the names Meet showed): %s\n", strings.Join(c.Joined, "; "))
	}
	if c.Calendly {
		fmt.Fprintf(&b, "Booked through Calendly: %s\n", c.CalendlyEvent)
		keys := make([]string, 0, len(c.CalendlyAnswers))
		for k := range c.CalendlyAnswers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "  %s: %s\n", k, c.CalendlyAnswers[k])
		}
	}
	if len(c.Corrections) > 0 {
		b.WriteString("Mark's corrections (he read the earlier notes on this call and says this is what was really meant):\n")
		for _, x := range c.Corrections {
			fmt.Fprintf(&b, "  - %s\n", x.Text)
		}
	}
	for _, n := range []*Notes{c.APINotes, c.ExtNotes} {
		if n == nil || (n.Summary == "" && len(n.NextSteps) == 0) {
			continue
		}
		fmt.Fprintf(&b, "Automatic notes by %s (a machine summary, often wrong: check everything against the transcript):\n%s\n", n.By, n.Summary)
		for _, st := range n.NextSteps {
			fmt.Fprintf(&b, "  - %s\n", st)
		}
	}
	if lines := c.Lines(); len(lines) > 0 {
		fmt.Fprintf(&b, "Transcript (%s):\n", c.TranscriptSource())
		for i, l := range lines {
			fmt.Fprintf(&b, "#%d [%s] %s: %s\n", i+1, l.At.In(s.tz).Format("15:04"), l.Speaker, l.Text)
		}
	}
	if known := s.knownFromSnap(c); known != "" {
		fmt.Fprintf(&b, "Known about %s (from Mark's mail, via Snap): %s\n", c.Company(), known)
	}
	input = b.String()
	if len(input) > maxInput {
		input = input[:maxInput] + "\n[transcript cut here: the call was very long]\n"
	}
	sum := sha256.Sum256([]byte(claudeReadVersion + input))
	hash = hex.EncodeToString(sum[:8])
	if done := c.doneSteps(); len(done) > 0 {
		input += "Already done (Mark ticked these off): " + strings.Join(done, "; ") + "\n"
	}
	return input, hash
}

const (
	contextCalls   = 4
	contextThreads = 5
	lessonsKept    = 25
)

// claudeContext is what surrounds a call: the earlier calls with its people
// and Mark's mail with them. It goes out with the call but isn't part of
// what is hashed, so a new email doesn't send the call back for a re-read.
func (s *Server) claudeContext(c *Call) string {
	var b strings.Builder
	people := s.people(c)
	var outside []string
	for _, p := range people {
		if !teamDomain(p.Email) {
			outside = append(outside, p.Email)
		}
	}
	n := 0
	for _, o := range s.CallsWith(bridge.Emails(people), c.Company()) {
		if o.ID == c.ID || !o.Start.Before(c.Start) || n == contextCalls {
			continue
		}
		oc, ok := s.store.Get(o.ID)
		if !ok || (oc.TLDR == "" && o.Summary == "") {
			continue
		}
		if n == 0 {
			b.WriteString("\nEarlier calls with these people:\n")
		}
		n++
		fmt.Fprintf(&b, "- %s, %s: %s %s\n", o.Start.In(s.tz).Format("2 Jan 2006"), o.Title, oc.TLDR, o.Summary)
		for _, pt := range oc.Points {
			fmt.Fprintf(&b, "    . %s\n", pt)
		}
		for _, st := range o.Steps {
			done := ""
			if st.Done {
				done = " (done)"
			}
			fmt.Fprintf(&b, "    step: %s%s\n", st.Text, done)
		}
	}
	if s.mail != nil && len(outside) > 0 {
		q := "{" + strings.Join(outside, " ") + "} newer_than:2y"
		if threads, err := s.mail.Search(q, contextThreads); err == nil && len(threads) > 0 {
			b.WriteString("\nFrom Mark's mail (the newest conversations with the people on this call):\n")
			for _, th := range threads {
				fmt.Fprintf(&b, "- Subject: %s\n", th.Subject)
				for _, m := range th.Mails {
					fmt.Fprintf(&b, "    [%s] %s: %s\n", m.Date, m.From, strings.Join(strings.Fields(m.Text), " "))
				}
			}
		} else if err == nil {
			b.WriteString("\nFrom Mark's mail: no conversation with the people on this call yet.\n")
		}
	}
	return b.String()
}

// houseRules: who the company is, how it works and what stays out of Snoop.
// Private, so it lives on the server (DataDir/snoop/house.md), not in this
// repository.
func (s *Server) houseRules() string {
	data, err := os.ReadFile(s.cfg.DataDir + string(os.PathSeparator) + "snoop" + string(os.PathSeparator) + "house.md")
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		return ""
	}
	return "\n\nHOUSE RULES\n" + strings.TrimSpace(string(data)) + "\n"
}

// lessons: Mark's corrections across all calls, newest first. They lead
// every later read, so the same mistake doesn't come back.
func (s *Server) lessons() string {
	type l struct {
		at   time.Time
		text string
	}
	var all []l
	for _, c := range s.store.All() {
		for _, x := range c.Corrections {
			all = append(all, l{x.At, x.Text})
		}
	}
	if len(all) == 0 {
		return ""
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at.After(all[j].at) })
	if len(all) > lessonsKept {
		all = all[:lessonsKept]
	}
	var b strings.Builder
	b.WriteString("\n\nLESSONS (Mark's corrections to notes on earlier calls; each one is a mistake not to repeat, and says something about how the company really works):\n")
	for _, x := range all {
		fmt.Fprintf(&b, "- %s\n", x.text)
	}
	return b.String()
}

// doneSteps: the next steps Mark ticked off.
func (c *Call) doneSteps() []string {
	n := c.Notes()
	if n == nil {
		return nil
	}
	var out []string
	for i, st := range n.NextSteps {
		if c.Ticked[i] {
			out = append(out, st)
		}
	}
	return out
}

// claudeDue: a finished call with something to read that Claude hasn't read
// in this form yet.
func (s *Server) claudeDue(c *Call, now time.Time) (string, string, bool) {
	if c.Declined || c.Cancelled || now.Sub(c.End) > claudeWindow || c.Empty(now) {
		return "", "", false
	}
	switch st, _ := c.Status(now); st {
	case StatusDone, StatusTranscript, StatusNoTranscript:
	default:
		return "", "", false // still on, or still waiting for the transcript
	}
	if len(c.Lines()) == 0 && (c.APINotes == nil || c.APINotes.Summary == "") && (c.ExtNotes == nil || c.ExtNotes.Summary == "") {
		return "", "", false
	}
	in, hash := s.claudeInput(c)
	return in, hash, hash != c.ClaudeRead
}

type claudeItem struct {
	ID    string `json:"id"`
	Read  string `json:"read"`
	Input string `json:"input"`
}

func (s *Server) claudeQueue(now time.Time) []claudeItem {
	calls := s.store.All()
	sort.Slice(calls, func(i, j int) bool { return calls[i].End.After(calls[j].End) }) // newest first
	var out []claudeItem
	for i := range calls {
		if in, hash, ok := s.claudeDue(&calls[i], now); ok {
			out = append(out, claudeItem{calls[i].ID, hash, in})
		}
	}
	return out
}

// claudeFree is the queue (all) without the calls another fetch took in the last
// claudeLease and hasn't answered: what a run fetching now would get. The
// first take of them are handed out (0 = just looking). A call that changed
// since (new lines, a correction) is free at once, and an answered one has
// left the queue anyway.
func (s *Server) claudeFree(all []claudeItem, now time.Time, take int) []claudeItem {
	cs := s.claude
	cs.mu.Lock()
	defer cs.mu.Unlock()
	due := make(map[string]string, len(all))
	free := []claudeItem{}
	for _, it := range all {
		due[it.ID] = it.Read
		if h, ok := cs.handed[it.ID]; ok && h.read == it.Read && now.Sub(h.at) < claudeLease {
			continue
		}
		free = append(free, it)
	}
	for id, h := range cs.handed { // answered, changed or run out
		if due[id] != h.read || now.Sub(h.at) >= claudeLease {
			delete(cs.handed, id)
		}
	}
	for i := 0; i < take && i < len(free); i++ {
		if cs.handed == nil {
			cs.handed = map[string]handOut{}
		}
		cs.handed[free[i].ID] = handOut{free[i].Read, now}
	}
	return free
}

func (s *Server) requireClaude(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if s.claude == nil || subtle.ConstantTimeCompare([]byte(got), []byte(s.claude.token)) != 1 {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// handleClaudeQueue: GET /api/claude/queue hands out the next calls to read.
// "remaining" counts only calls a next fetch would get: the ones another run
// holds aren't in it, so an empty "calls" always comes with 0 and no run
// keeps asking for calls it won't be given.
func (s *Server) handleClaudeQueue(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	s.claude.mu.Lock()
	s.claude.seen = now
	s.claude.mu.Unlock()
	all := s.claudeFree(s.claudeQueue(now), now, claudeBatch)
	items := all
	if len(items) > claudeBatch {
		items = items[:claudeBatch]
	}
	for i := range items { // the context only for the calls handed out: it costs Gmail reads
		if c, ok := s.store.Get(items[i].ID); ok {
			items[i].Input += s.claudeContext(&c)
		}
	}
	api := s.cfg.PublicURL + s.cfg.Base + "/api/claude/"
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"today":        now.In(s.tz).Format("Monday 2 January 2006"),
		"instructions": ClaudeInstructions + s.upcomingEvents(now) + s.lessons() + s.houseRules(),
		"lookups": map[string]string{
			"how":   "GET with the same Authorization header as the queue; q is URL-encoded. Both return JSON.",
			"mail":  api + "mail?q=<a Gmail search, e.g. \"Berlin\" hosting newer_than:2y, or from:someone@company.com>  (Mark's mailbox: up to 8 conversations with their mails)",
			"calls": api + "calls?q=<words, e.g. Berlin host>  (Mark's other calls: notes, steps and the transcript lines that match)",
		},
		"calls":     items,
		"remaining": len(all) - len(items),
	})
}

// handleClaudeMail: GET /api/claude/mail?q= runs a Gmail search through Snap,
// so Claude can read how a similar situation went.
func (s *Server) handleClaudeMail(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || len(q) > 500 {
		http.Error(w, "q: a Gmail search, up to 500 characters", http.StatusBadRequest)
		return
	}
	if s.mail == nil {
		http.Error(w, "Snap isn't connected", http.StatusServiceUnavailable)
		return
	}
	threads, err := s.mail.Search(q, 8)
	if err != nil {
		http.Error(w, "Gmail: "+err.Error(), http.StatusBadGateway)
		return
	}
	if threads == nil {
		threads = []bridge.MailThread{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"q": q, "threads": threads})
}

// handleClaudeCalls: GET /api/claude/calls?q= looks through Mark's other calls.
func (s *Server) handleClaudeCalls(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || len(q) > 200 {
		http.Error(w, "q: a few words, up to 200 characters", http.StatusBadRequest)
		return
	}
	type hit struct {
		Title   string   `json:"title"`
		When    string   `json:"when"`
		Company string   `json:"company,omitempty"`
		TLDR    string   `json:"tldr,omitempty"`
		Summary string   `json:"summary,omitempty"`
		Points  []string `json:"points,omitempty"`
		Steps   []string `json:"steps,omitempty"`
		Lines   []string `json:"lines,omitempty"`
	}
	out := []hit{}
	for _, h := range s.SearchCalls(q, 8) {
		c, _ := s.store.Get(h.Call.ID)
		x := hit{Title: h.Call.Title, When: h.Call.Start.In(s.tz).Format("Mon 2 Jan 2006"), Company: h.Call.Company, TLDR: c.TLDR, Summary: h.Call.Summary, Points: c.Points, Lines: h.Lines}
		for _, st := range h.Call.Steps {
			x.Steps = append(x.Steps, st.Text)
		}
		out = append(out, x)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"q": q, "calls": out})
}

// ClaudeAnswer is Claude's read of one call.
type ClaudeAnswer struct {
	ID        string     `json:"id"`
	Read      string     `json:"read"`
	Summary   string     `json:"summary"`
	TLDR      string     `json:"tldr"`
	Ours      []string   `json:"ours"`
	Theirs    []string   `json:"theirs"`
	NextSteps []string   `json:"next_steps"` // the old flat list: taken as ours when no sides came
	Links     []Link     `json:"links"`
	TLDRShort string     `json:"tldr_short"`
	Points    []pointAns `json:"points"`
	Fixes     []Fix      `json:"fixes"`
	Cut       []int      `json:"cut"`
	Rewrites  []struct {
		Line int    `json:"line"`
		Text string `json:"text"`
	} `json:"rewrites"`
	CompanyFacts string       `json:"company_facts"`
	Actions      []CallAction `json:"actions"`
}

// handleClaudeAnswers: POST /api/claude/answers.
func (s *Server) handleClaudeAnswers(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Model string         `json:"model"`
		Calls []ClaudeAnswer `json:"calls"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&in); err != nil {
		http.Error(w, "bad JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	by := "Claude"
	if m := strings.TrimSpace(in.Model); m != "" {
		by = "Claude (" + m + ")"
	}
	now := s.now()
	applied, stale, unknown := 0, 0, 0
	for _, a := range in.Calls {
		c, ok := s.store.Get(a.ID)
		if !ok {
			unknown++
			continue
		}
		if _, hash := s.claudeInput(&c); hash != a.Read {
			stale++ // the call changed meanwhile: it comes back in the queue
			continue
		}
		s.store.Update(a.ID, func(c *Call) { applyClaude(c, a, by, now) })
		applied++
	}
	if applied > 0 {
		s.syncNow() // rewrites the Drive docs
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"applied": applied, "stale": stale, "unknown": unknown, "remaining": len(s.claudeFree(s.claudeQueue(now), now, 0))})
}

// pointAns is one point of Claude's answer: {"short", "more"}, or just the
// text (the older form).
type pointAns struct {
	Short string `json:"short"`
	More  string `json:"more"`
}

func (p *pointAns) UnmarshalJSON(data []byte) error {
	var s string
	if json.Unmarshal(data, &s) == nil {
		p.Short = s
		return nil
	}
	type plain pointAns
	return json.Unmarshal(data, (*plain)(p))
}

// sideSteps: at most 5 steps a side, the most critical first (Claude's order).
const sideSteps = 5

// applyClaude stores Claude's notes; steps Mark had ticked or marked as
// important stay so when Claude kept them word for word.
func applyClaude(c *Call, a ClaudeAnswer, by string, now time.Time) {
	key := func(st string) string { return strings.ToLower(strings.TrimSpace(st)) }
	done, important := map[string]bool{}, map[string]bool{}
	if n := c.Notes(); n != nil {
		for i, st := range n.NextSteps {
			if c.Ticked[i] {
				done[key(st)] = true
			}
			if c.Important[i] {
				important[key(st)] = true
			}
		}
	}
	clean := func(in []string, max int) []string {
		var out []string
		for _, st := range in {
			if st = strings.TrimSpace(st); st != "" && (max == 0 || len(out) < max) {
				out = append(out, st)
			}
		}
		return out
	}
	ours, theirs := clean(a.Ours, sideSteps), clean(a.Theirs, sideSteps)
	if len(ours)+len(theirs) == 0 {
		ours = clean(a.NextSteps, 0)
	}
	steps := append(append([]string{}, ours...), theirs...)
	c.ClaudeNotes = &Notes{Summary: strings.TrimSpace(a.Summary), NextSteps: steps, By: by, Ours: len(ours), Sided: true}
	c.TLDR = strings.TrimSpace(a.TLDR)
	c.Links = cleanLinks(a.Links)
	c.TLDRShort = strings.TrimSpace(a.TLDRShort)
	c.Points, c.PointsShort = nil, nil
	for _, pt := range a.Points {
		short, more := strings.TrimSpace(pt.Short), strings.TrimSpace(pt.More)
		if more == "" {
			short, more = "", short
		}
		if more != "" && len(c.Points) < 10 {
			c.Points, c.PointsShort = append(c.Points, more), append(c.PointsShort, short)
		}
	}
	c.Fixes = cleanFixes(a.Fixes)
	c.Cut = nil
	n := len(c.Lines())
	for _, i := range a.Cut {
		if i >= 1 && i <= n {
			c.Cut = append(c.Cut, i)
		}
	}
	c.Rewrites = nil
	for _, rw := range a.Rewrites {
		if text := strings.TrimSpace(rw.Text); rw.Line >= 1 && rw.Line <= n && text != "" && len(text) <= len(c.Lines()[rw.Line-1].Text)+200 {
			if c.Rewrites == nil {
				c.Rewrites = map[int]string{}
			}
			c.Rewrites[rw.Line] = text
		}
	}
	c.CompanyFacts = strings.TrimSpace(a.CompanyFacts)
	c.Actions = nil
	for _, x := range a.Actions {
		if x.Title = strings.TrimSpace(x.Title); x.Title != "" && len(c.Actions) < 3 {
			var steps []string
			for _, st := range x.Steps {
				if st = strings.TrimSpace(st); st != "" && len(steps) < 4 {
					steps = append(steps, st)
				}
			}
			x.Steps = steps
			c.Actions = append(c.Actions, x)
		}
	}
	c.ClaudeRead, c.ClaudeAt = a.Read, now
	c.Ticked, c.Important = nil, nil
	for i, st := range steps {
		if done[key(st)] {
			if c.Ticked == nil {
				c.Ticked = map[int]bool{}
			}
			c.Ticked[i] = true
		}
		if important[key(st)] {
			if c.Important == nil {
				c.Important = map[int]bool{}
			}
			c.Important[i] = true
		}
	}
}

var eventDirRe = regexp.MustCompile(`^20\d\d-`)

// upcomingEvents lists the events on the websites (re-read every 6 hours),
// so the Push plays can name a real event near the company.
func (s *Server) upcomingEvents(now time.Time) string {
	cs := s.claude
	if cs == nil {
		return ""
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if !cs.eventsAt.IsZero() && now.Sub(cs.eventsAt) < 6*time.Hour {
		return cs.events
	}
	type ev struct {
		at   time.Time
		line string
	}
	var evs []ev
	for _, repo := range sites.Repos() {
		body, err := sites.Get(sites.APIURL + "/repos/" + repo + "/contents/")
		if err != nil {
			continue
		}
		var entries []struct{ Name, Type string }
		if json.Unmarshal(body, &entries) != nil {
			continue
		}
		for _, e := range entries {
			if e.Type != "dir" || !eventDirRe.MatchString(e.Name) {
				continue
			}
			meta, err := sites.Get(sites.RawURL + "/" + repo + "/HEAD/" + url.PathEscape(e.Name) + "/metadata.yml")
			if err != nil {
				continue
			}
			var brand, city string
			var start time.Time
			for _, line := range strings.Split(string(meta), "\n") {
				k, v, ok := strings.Cut(sites.StripComment(line), ":")
				if !ok || strings.HasPrefix(line, " ") {
					continue
				}
				v = sites.Unquote(strings.TrimSpace(v))
				switch strings.TrimSpace(k) {
				case "brand_name":
					brand = v
				case "city_name":
					city = v
				case "start_time":
					start, _ = time.Parse(time.RFC3339, v)
				}
			}
			if brand == "" || start.IsZero() || start.Before(now) {
				continue
			}
			evs = append(evs, ev{start, fmt.Sprintf("- %s %s, %s", brand, city, start.In(s.tz).Format("2 Jan 2006"))})
		}
	}
	sort.Slice(evs, func(i, j int) bool { return evs[i].at.Before(evs[j].at) })
	var lines []string
	for _, e := range evs {
		lines = append(lines, e.line)
	}
	cs.events, cs.eventsAt = "", now
	if len(lines) > 0 {
		cs.events = "\n\nUPCOMING EVENTS (from the websites; Conf42 events are online):\n" + strings.Join(lines, "\n") + "\n"
	}
	return cs.events
}

// --- Starting the reading routine -------------------------------------------

// Snoop starts "Snoop: read the calls" itself when calls wait for Claude
// (Marek 2026-11-17: most hourly runs found nothing to read, so the schedule
// is only the safety net). It needs the routine's id and an API trigger's
// token: pasted once on a call page (DataDir/snoop/read.json) or from the env
// (SNOOP_READ_ROUTINE, SNOOP_READ_TOKEN). Without them nothing is started.

// readFireGap: between two starts of the reading routine, and how long after
// its last visit it counts as still at work.
const readFireGap = 10 * time.Minute

func (s *Server) readPath() string { return filepath.Join(s.cfg.DataDir, "snoop", "read.json") }

func (s *Server) readCfg() draftCfg {
	c := draftCfg{Routine: s.cfg.ReadRoutine, Token: s.cfg.ReadToken}
	var f draftCfg
	if data, err := os.ReadFile(s.readPath()); err == nil {
		json.Unmarshal(data, &f)
	}
	if f.Routine != "" && f.Token != "" {
		c = f // what was pasted wins over the env
	}
	return c
}

// CanRead: Snoop can start the reading routine itself.
func (s *Server) CanRead() bool { c := s.readCfg(); return c.Routine != "" && c.Token != "" }

// fireIfWaiting starts the reading routine when calls wait for it: at most
// once every ten minutes, not while the routine is visiting, and once per
// queue. When a start changes nothing (it failed, or the run couldn't settle
// a call), the same queue waits for the hourly run instead of being fired at
// all day.
func (s *Server) fireIfWaiting(now time.Time) {
	cfg := s.readCfg()
	if cfg.Routine == "" || cfg.Token == "" {
		return // the hourly run reads them
	}
	all := s.claudeQueue(now)
	q := s.claudeFree(all, now, 0) // calls a run already holds don't start another
	keys := make([]string, 0, len(all))
	for _, it := range all {
		keys = append(keys, it.ID+"@"+it.Read)
	}
	sort.Strings(keys)
	mark := strings.Join(keys, "\n")
	cs := s.claude
	cs.mu.Lock()
	busy := len(q) == 0 || now.Sub(cs.lastFire) < readFireGap || now.Sub(cs.seen) < readFireGap || mark == cs.firedFor
	if len(all) == 0 {
		cs.firedFor = ""
	} else if !busy {
		cs.lastFire, cs.firedFor = now, mark
	}
	cs.mu.Unlock()
	if busy {
		return
	}
	text := fmt.Sprintf("SNOOP CALLS\n\n%d call(s) wait in Snoop's queue. Read the queue now and answer it, exactly as your prompt says for a scheduled run.", len(q))
	if _, err := s.fire(cfg, text); err != nil {
		log.Printf("Snoop: starting Claude for %d waiting call(s): %v", len(q), err)
		return
	}
	log.Printf("Snoop: started Claude: %d call(s) waiting", len(q))
}

// handleReadSetup: POST /admin/read-setup saves the reading routine's id and
// API token (made in the routine's Edit screen: Trigger, +, API).
func (s *Server) handleReadSetup(w http.ResponseWriter, r *http.Request) {
	cfg := draftCfg{Routine: strings.TrimSpace(r.FormValue("routine")), Token: strings.TrimSpace(r.FormValue("token"))}
	if !routineID.MatchString(cfg.Routine) || !strings.HasPrefix(cfg.Token, "sk-ant-") {
		http.Error(w, "That doesn't look like a routine id (trig_...) and its API token (sk-ant-...)", http.StatusBadRequest)
		return
	}
	data, _ := json.Marshal(cfg)
	if err := writeFileAtomic(s.readPath(), data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.claude.mu.Lock()
	s.claude.lastFire, s.claude.firedFor = time.Time{}, ""
	s.claude.mu.Unlock()
	s.syncNow() // calls that wait don't wait for the next pass
	http.Redirect(w, r, r.Header.Get("Referer"), http.StatusSeeOther)
}
