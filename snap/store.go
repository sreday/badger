package snap

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
}

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
	Skip      string    `json:"skip,omitempty"`     // why Snap leaves it alone: automated, no reply, too old
	Snippet   string    `json:"snippet,omitempty"`  // Gmail's snippet of the last message

	// Claude's read, for HistoryID == AnalyzedHistory.
	AnalyzedHistory string    `json:"analyzed_history,omitempty"`
	AnalyzedAt      time.Time `json:"analyzed_at,omitempty"`
	AnalyzedBy      string    `json:"analyzed_by,omitempty"`    // model, or "rules" without Claude
	ClaudeHistory   string    `json:"claude_history,omitempty"` // the history id Claude last read
	Category        string    `json:"category,omitempty"`       // sponsors, speakers, team, others
	Loose           bool      `json:"loose,omitempty"`
	Label           string    `json:"label,omitempty"` // the loose end in a few words
	Why             string    `json:"why,omitempty"`
	Summary         string    `json:"summary,omitempty"`
	Waiting         string    `json:"waiting,omitempty"`   // "you", "them" or ""
	FollowUp        string    `json:"follow_up,omitempty"` // YYYY-MM-DD: "touch base in 6 months"
	Priority        string    `json:"priority,omitempty"`  // high, medium, low
	Company         string    `json:"company,omitempty"`
	Event           string    `json:"event,omitempty"`
	Actions         []Action  `json:"actions,omitempty"`
	CancelName      string    `json:"cancel_name,omitempty"`  // a speaker who pulled out
	CancelEvent     string    `json:"cancel_event,omitempty"` // and the event they named
	Error           string    `json:"error,omitempty"`

	// The lineup check, redone on every sync: where a speaker who
	// cancelled is still listed ("" = not on any upcoming lineup).
	StillListed string    `json:"still_listed,omitempty"`
	ListedAt    time.Time `json:"listed_at,omitempty"`

	// Marek's.
	Done        bool         `json:"done,omitempty"`
	DoneAt      time.Time    `json:"done_at,omitempty"`
	SnoozeUntil time.Time    `json:"snooze_until,omitempty"`
	Ticked      map[int]bool `json:"ticked,omitempty"`
}

// Pending: Gmail moved on since Claude last read it.
func (t *Thread) Pending() bool { return t.Skip == "" && t.HistoryID != t.ClaudeHistory }

// Open is a loose end still to deal with at now: not done, not snoozed, not
// a follow-up date in the future.
func (t *Thread) Open(now time.Time) bool {
	return t.IsLoose() && !t.Done && !t.SnoozeUntil.After(now) && !t.Later(now)
}

// IsLoose: Claude (or the rules) said so, or a speaker who cancelled is
// still on a lineup.
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
		return t.Waiting == "you"
	}
	return t.Loose
}

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
	if t.Ticked != nil {
		c.Ticked = make(map[int]bool, len(t.Ticked))
		for k, v := range t.Ticked {
			c.Ticked[k] = v
		}
	}
	return c
}
