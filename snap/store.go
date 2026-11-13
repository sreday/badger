package snap

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

// Person is someone on a thread other than Mark.
type Person struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
}

// Action is a next step Claude proposes. Prompt is what "Copy for Claude"
// puts on the clipboard, ready to paste into Claude Code.
type Action struct {
	Title  string `json:"title"`
	Prompt string `json:"prompt"`
	Kind   string `json:"kind,omitempty"`  // reply (Claude drafts it), task (Mark's own work), snooze, deadend
	Until  string `json:"until,omitempty"` // snooze: YYYY-MM-DD
}

// UntilDate: a snooze option's date, if it's a real one ahead of now.
func (a Action) UntilDate(now time.Time) (time.Time, bool) {
	if a.Kind != "snooze" {
		return time.Time{}, false
	}
	d, err := time.Parse("2006-01-02", a.Until)
	return d, err == nil && d.After(now)
}

// Reply: this step is an email, so the card offers "Draft reply with Claude".
// Older reads have no kind: a step about the website, talks.csv, a
// contract or an invoice is a task, the rest replies.
func (a Action) Reply() bool {
	switch a.Kind {
	case "reply":
		return true
	case "task", "snooze", "deadend":
		return false
	}
	return !taskWords.MatchString(a.Title)
}

var taskWords = regexp.MustCompile(`(?i)talks\.csv|website|lineup|\bsite\b|\brepo\b|\bcsv\b|contract|invoice|update the|remove .*talk|rename .*talk|take .* off`)

// Thread is one Gmail thread Snap knows about. What Gmail says is refreshed
// on every sync; what Claude says is redone only when the thread changes
// (its history id moves); what Marek says (done, snoozed, ticked) is his.
type Thread struct {
	ID        string    `json:"id"`
	Subject   string    `json:"subject"`
	People    []Person  `json:"people,omitempty"`
	Messages  int       `json:"messages"`
	FirstAt   time.Time `json:"first_at"`
	LastAt    time.Time `json:"last_at"`
	LastFrom  string    `json:"last_from,omitempty"` // display name of whoever wrote last; "me" for Mark
	HistoryID string    `json:"history_id"`
	Internal  bool      `json:"internal,omitempty"` // only the team's own domains
	Sponsor   bool      `json:"sponsor,omitempty"`  // looks like sponsorship or hosting (keywords, a sponsor's domain)
	Form      string    `json:"form,omitempty"`     // the last word is a website form's: "lead" (hot) or "speaker" (relaxed, Speakers tab)
	Skip      string    `json:"skip,omitempty"`     // why Snap leaves it alone: automated, no reply, too old
	Snippet   string    `json:"snippet,omitempty"`  // Gmail's snippet of the last message

	// Claude's read, for HistoryID == AnalyzedHistory.
	AnalyzedHistory string    `json:"analyzed_history,omitempty"`
	AnalyzedAt      time.Time `json:"analyzed_at,omitempty"`
	AnalyzedBy      string    `json:"analyzed_by,omitempty"`    // model, or "rules" without Claude
	ClaudeHistory   string    `json:"claude_history,omitempty"` // the history id Claude last read
	Category        string    `json:"category,omitempty"`       // sponsors, speakers, events
	MovedTo         string    `json:"moved_to,omitempty"`       // the tab Marek dragged it to: Claude never overrides it
	Loose           bool      `json:"loose,omitempty"`
	Label           string    `json:"label,omitempty"` // the loose end in a few words
	Why             string    `json:"why,omitempty"`
	Summary         string    `json:"summary,omitempty"`
	Waiting         string    `json:"waiting,omitempty"`   // "you", "them", "between" (others talk, Mark is on Cc) or ""
	Between         string    `json:"between,omitempty"`   // with "between": "Rod and Kyndryl"
	FollowUp        string    `json:"follow_up,omitempty"` // YYYY-MM-DD: "touch base in 6 months"
	Priority        string    `json:"priority,omitempty"`  // high, medium, low
	Company         string    `json:"company,omitempty"`
	Topic           string    `json:"topic,omitempty"`      // Claude's topic key: threads on one matter group into one card (topics.go)
	RelatedAt       time.Time `json:"related_at,omitempty"` // newest message in the related threads
	Event           string    `json:"event,omitempty"`
	Actions         []Action  `json:"actions,omitempty"`
	CancelName      string    `json:"cancel_name,omitempty"`  // a speaker who pulled out
	CancelEvent     string    `json:"cancel_event,omitempty"` // and the event they named
	Error           string    `json:"error,omitempty"`

	// "Draft reply with Claude": when Marek asked, and whether a draft now
	// sits in the thread in Gmail.
	DraftAsked time.Time `json:"draft_asked,omitempty"`
	HasDraft   bool      `json:"has_draft,omitempty"`

	// The lineup check, redone on every sync: where a speaker who
	// cancelled is still listed ("" = not on any upcoming lineup).
	StillListed string    `json:"still_listed,omitempty"`
	ListedAt    time.Time `json:"listed_at,omitempty"`

	// Marek's.
	Done        bool         `json:"done,omitempty"`
	DeadEnd     bool         `json:"dead_end,omitempty"`   // Marek gave up on it: it leaves Snap for good (unless new mail comes)
	BouncedAt   time.Time    `json:"bounced_at,omitempty"` // the bounce that made it a dead end (each bounce acts once)
	DoneAt      time.Time    `json:"done_at,omitempty"`
	SnoozeUntil time.Time    `json:"snooze_until,omitempty"`
	SentAt      time.Time    `json:"sent_at,omitempty"` // Mark's own reply that parked the card in Snoozed (sent.go)
	Ticked      map[int]bool `json:"ticked,omitempty"`

	// Quick questions Marek asked Claude about this thread, and the context
	// he chose to add to it from the answers (qa.go). Context goes into the
	// excerpt every time Claude reads the thread.
	QA      []QA     `json:"qa,omitempty"`
	Context []string `json:"context,omitempty"`
	Enrich  Enrich   `json:"enrich,omitempty"` // "Enrich the thread": the chat re-wires the card (qa.go)
	Fix     Fix      `json:"fix,omitempty"`    // "Correct draft": Mark's version on its way into Gmail (preview.go)
}

// Pending: Gmail moved on since Claude last read it.
func (t *Thread) Pending() bool {
	return t.Skip == "" && (t.HistoryID != t.ClaudeHistory || t.RelatedMoved())
}

// RelatedMoved: an open loose end whose related threads (same people or
// company) moved after Claude's last read; Claude reads it again in that
// light (topics.go).
func (t *Thread) RelatedMoved() bool { return t.Flagged() && t.RelatedAt.After(t.AnalyzedAt) }

// Open is a loose end still to deal with at now: not done, not snoozed, not
// a follow-up date in the future.
func (t *Thread) Open(now time.Time) bool {
	return t.IsLoose() && !t.Done && !t.DeadEnd && !t.SnoozeUntil.After(now) && !t.Later(now)
}

// IsLoose: Claude said so, or a speaker who cancelled is still on a lineup.
// The rules' guess is never shown (Marek 2026-11-12: Snap lists only the
// high-stakes threads Claude picked out, not all mail).
func (t *Thread) IsLoose() bool {
	if t.Skip != "" {
		return false
	}
	if t.StillListed != "" {
		return true
	}
	if t.CancelName != "" && !t.ListedAt.IsZero() {
		// The speaker pulled out and the lineup already says so: only a
		// reply still owed keeps it open.
		return t.Waiting == "you" && t.ByClaude()
	}
	return t.Loose && t.ByClaude()
}

// Ripe: worth Claude's time yet. While Marek answers promptly and threads
// keep moving, Snap has nothing to do (Marek 2026-11-12). A thread goes to
// Claude when someone has waited on him 24 hours, when a speaker thread
// (a drop-off, a talk change) has sat 48 hours, or when it has been quiet
// 2 weeks (a deal gone cold).
func (t *Thread) Ripe(now time.Time) bool {
	age := now.Sub(t.LastAt)
	if t.ByClaude() && t.ClaudeHistory == "" {
		return true // sent back to Claude on purpose (Read again, new context, new rules)
	}
	if !t.SentAt.IsZero() && t.LastFrom == "me" && t.Flagged() {
		return !t.SnoozeUntil.After(now) // Mark answered: Claude reads it again when the snooze ends (sent.go)
	}
	if t.Form == "speaker" {
		return age >= hotSettle // a speaker form: read on the next hourly run, no day's wait (forms.go)
	}
	if t.FreshInbound(now) {
		// The fast lane (hot.go): fresh mail from a person goes to Claude
		// within minutes; the short wait lets a burst be read once.
		return age >= hotSettle
	}
	if t.CancelName != "" || t.Category == "speakers" {
		return age >= ripeSpeaker
	}
	return (t.Waiting == "you" && age >= ripeYou) || age >= ripeQuiet
}

const (
	ripeYou     = 24 * time.Hour
	ripeSpeaker = 48 * time.Hour
	ripeQuiet   = 14 * 24 * time.Hour
)

// Queued: Gmail moved on since Claude's last read, and it's ripe.
func (t *Thread) Queued(now time.Time) bool { return t.Pending() && t.Ripe(now) }

// Flagged: a loose end Claude flagged that Marek hasn't closed. It never
// expires and never leaves the store, even past Gmail's year.
func (t *Thread) Flagged() bool { return t.Loose && t.ByClaude() && !t.Done && !t.DeadEnd }

// ByClaude: Claude read this thread (not just the rules).
func (t *Thread) ByClaude() bool { return t.AnalyzedBy != "" && t.AnalyzedBy != "rules" }

// Later: a follow-up date still ahead ("touch base in 6 months").
func (t *Thread) Later(now time.Time) bool {
	d, ok := t.FollowUpDate()
	return ok && d.After(now) && t.StillListed == ""
}

// Due: the follow-up date has come.
func (t *Thread) Due(now time.Time) bool {
	d, ok := t.FollowUpDate()
	return ok && !d.After(now)
}

func (t *Thread) FollowUpDate() (time.Time, bool) {
	if t.FollowUp == "" {
		return time.Time{}, false
	}
	d, err := time.Parse("2006-01-02", t.FollowUp)
	return d, err == nil
}

// Store keeps every thread in one file, written atomically.
type Store struct {
	mu      sync.Mutex
	path    string
	threads map[string]*Thread
}

func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dir, "threads.json"), threads: map[string]*Thread{}}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var list []*Thread
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	for _, t := range list {
		if t.Category == "team" {
			t.Category = "events" // the Team tab became Events (2026-11-12)
		}
		if t.Category == "others" {
			t.Category = "events" // the Other tab is gone (2026-11-13): Claude sorts it again on its next read
		}
		if t.MovedTo == "others" || t.MovedTo == "team" {
			t.MovedTo = ""
		}
		if t.Skip == "older than 30 days" {
			t.Skip = "" // the 30-day window is gone (2026-11-12): a year of every kind is read
		}
		s.threads[t.ID] = t
	}
	return s, nil
}

func (s *Store) saveLocked() error {
	list := make([]*Thread, 0, len(s.threads))
	for _, t := range s.threads {
		list = append(list, t)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	data, err := json.Marshal(list)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Get returns a copy.
func (s *Store) Get(id string) (Thread, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.threads[id]
	if !ok {
		return Thread{}, false
	}
	return clone(t), true
}

// All returns copies of every thread.
func (s *Store) All() []Thread {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Thread, 0, len(s.threads))
	for _, t := range s.threads {
		out = append(out, clone(t))
	}
	return out
}

// HistoryIDs is thread id -> the history id Snap last saw.
func (s *Store) HistoryIDs() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.threads))
	for id, t := range s.threads {
		out[id] = t.HistoryID
	}
	return out
}

// Update changes a thread (created if new) and saves.
func (s *Store) Update(id string, fn func(t *Thread)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.threads[id]
	if !ok {
		t = &Thread{ID: id}
		s.threads[id] = t
	}
	fn(t)
	return s.saveLocked()
}

// Prune drops threads Gmail no longer lists (moved out of Primary, older
// than a year, deleted) and saves.
func (s *Store) Prune(keep map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, t := range s.threads {
		if !keep[id] && !t.Flagged() && t.StillListed == "" {
			delete(s.threads, id)
			n++
		}
	}
	if n > 0 {
		s.saveLocked()
	}
}

// UpdateAll changes every thread and saves once.
func (s *Store) UpdateAll(fn func(t *Thread)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.threads {
		fn(t)
	}
	return s.saveLocked()
}

func clone(t *Thread) Thread {
	c := *t
	c.People = append([]Person(nil), t.People...)
	c.Actions = append([]Action(nil), t.Actions...)
	c.QA = append([]QA(nil), t.QA...)
	c.Context = append([]string(nil), t.Context...)
	if t.Ticked != nil {
		c.Ticked = make(map[int]bool, len(t.Ticked))
		for k, v := range t.Ticked {
			c.Ticked[k] = v
		}
	}
	return c
}

// Stage changes a thread (created if new) without saving; Save writes
// everything staged. For the first read of a year of mail, which would
// otherwise rewrite the file thousands of times.
func (s *Store) Stage(id string, fn func(t *Thread)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.threads[id]
	if !ok {
		t = &Thread{ID: id}
		s.threads[id] = t
	}
	fn(t)
}

// Save writes the store.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

// Bucket is where the thread sits for Marek: "active" (open, the default
// view), "closed" (done or a dead end) or "snoozed" (snoozed, or a
// follow-up date still ahead).
func (t *Thread) Bucket(now time.Time) string {
	switch {
	case t.Done || t.DeadEnd:
		return "closed"
	case t.SnoozeUntil.After(now) || t.Later(now):
		return "snoozed"
	}
	return "active"
}
