package snoop

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Call is one trace: a Meet that mark@sreday.com was invited to, joined, or
// owned. The calendar is the master list; ad-hoc Meets (no calendar event)
// come from the Meet API or the extension.
type Call struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	MeetingCode string     `json:"meeting_code"`
	Start       time.Time  `json:"start"`
	End         time.Time  `json:"end"`
	EventID     string     `json:"event_id,omitempty"` // calendar event (instance) id; empty for ad-hoc Meets
	Organizer   string     `json:"organizer,omitempty"`
	Owned       bool       `json:"owned"` // Marek organises it, so Snoop can switch on transcripts + notes
	Attendees   []Attendee `json:"attendees,omitempty"`
	Declined    bool       `json:"declined,omitempty"`
	Cancelled   bool       `json:"cancelled,omitempty"`

	Armed    bool   `json:"armed,omitempty"`     // transcription (+ Gemini notes) switched on via spaces.patch
	NoGemini bool   `json:"no_gemini,omitempty"` // Gemini notes not available to the account: notes come from the extension
	ArmError string `json:"arm_error,omitempty"` // why arming didn't work (not the owner, no permission)

	// Source A: the Meet API.
	ConferenceID   string `json:"conference_id,omitempty"`
	TranscriptDoc  string `json:"transcript_doc,omitempty"`  // Meet's own transcript Google Doc
	TranscriptDone bool   `json:"transcript_done,omitempty"` // FILE_GENERATED, entries fetched
	NotesDoc       string `json:"notes_doc,omitempty"`       // Gemini "Notes by Gemini" Google Doc
	APILines       []Line `json:"api_lines,omitempty"`
	APINotes       *Notes `json:"api_notes,omitempty"`

	// Source B: the Chrome extension (Meet's live captions).
	ExtLines   []Line    `json:"ext_lines,omitempty"`
	ExtNotes   *Notes    `json:"ext_notes,omitempty"`
	ExtEnded   bool      `json:"ext_ended,omitempty"`
	ExtDevice  string    `json:"ext_device,omitempty"`
	ExtLastAt  time.Time `json:"ext_last_at,omitempty"`
	ExtProblem string    `json:"ext_problem,omitempty"` // e.g. "captions not found"

	DocID   string `json:"doc_id,omitempty"`   // Snoop's own Drive doc
	DocHash string `json:"doc_hash,omitempty"` // hash of the HTML last written, so unchanged calls aren't rewritten

	Ticked map[int]bool `json:"ticked,omitempty"` // next steps marked done on /admin
	Error  string       `json:"error,omitempty"`  // last sync error for this call

	Updated time.Time `json:"updated"`
}

// Attendee is one person on the calendar invite.
type Attendee struct {
	Name     string `json:"name,omitempty"`
	Email    string `json:"email"`
	Response string `json:"response,omitempty"` // accepted / declined / tentative / needsAction
}

// Line is one transcript line.
type Line struct {
	At      time.Time `json:"at"`
	Speaker string    `json:"speaker"`
	Text    string    `json:"text"`
}

// Notes are the actionable part: what was said, and what to do next.
type Notes struct {
	Summary   string   `json:"summary"`
	NextSteps []string `json:"next_steps"`
	By        string   `json:"by"` // "Gemini in Meet" or "Chrome on-device AI"
}

// Lines returns the best transcript: the Meet API one when there is one,
// otherwise the captions the extension read.
func (c *Call) Lines() []Line {
	if len(c.APILines) > 0 {
		return c.APILines
	}
	return c.ExtLines
}

// TranscriptSource names where Lines comes from.
func (c *Call) TranscriptSource() string {
	switch {
	case len(c.APILines) > 0:
		return "Meet transcript"
	case len(c.ExtLines) > 0:
		return "Meet captions (Snoop extension)"
	}
	return ""
}

// Notes returns Gemini's notes when there are any, otherwise the extension's.
func (c *Call) Notes() *Notes {
	if c.APINotes != nil && (c.APINotes.Summary != "" || len(c.APINotes.NextSteps) > 0) {
		return c.APINotes
	}
	if c.ExtNotes != nil && (c.ExtNotes.Summary != "" || len(c.ExtNotes.NextSteps) > 0) {
		return c.ExtNotes
	}
	return nil
}

// Call statuses shown on /admin.
const (
	StatusUpcoming     = "upcoming"
	StatusLive         = "live"
	StatusWaiting      = "waiting"
	StatusDone         = "done"
	StatusTranscript   = "transcript only"
	StatusNoTranscript = "no transcript"
	StatusDeclined     = "declined"
	StatusCancelled    = "cancelled"
)

const (
	transcriptWait = 2 * time.Hour    // Meet's transcript normally lands within minutes; give up after this
	notesWait      = 90 * time.Minute // write the doc without notes after this
	extQuiet       = 15 * time.Minute // extension silent this long after the end = it's not coming back
)

// Status says where the call is, and why when there's nothing to show.
func (c *Call) Status(now time.Time) (status, reason string) {
	switch {
	case c.Cancelled:
		return StatusCancelled, ""
	case c.Declined && len(c.Lines()) == 0:
		return StatusDeclined, ""
	case now.Before(c.Start):
		switch {
		case c.Armed && c.NoGemini:
			return StatusUpcoming, "transcript switched on (no Gemini notes on this account: the Snoop extension writes the notes)"
		case c.Armed:
			return StatusUpcoming, "transcript + Gemini notes switched on"
		case c.Owned && c.ArmError != "":
			return StatusUpcoming, "couldn't switch on transcripts: " + c.ArmError
		case !c.Owned:
			return StatusUpcoming, "hosted by " + c.Organizer + ": captions via the Snoop extension"
		}
		return StatusUpcoming, ""
	case now.Before(c.End):
		return StatusLive, ""
	}
	lines := len(c.Lines()) > 0
	switch {
	case lines && c.Notes() != nil:
		return StatusDone, ""
	case lines && c.waitingForNotes(now):
		return StatusWaiting, "transcript in, waiting for notes"
	case lines:
		return StatusTranscript, "no notes came"
	case c.waitingForTranscript(now):
		return StatusWaiting, "waiting for the transcript"
	}
	return StatusNoTranscript, c.noTranscriptReason()
}

func (c *Call) waitingForTranscript(now time.Time) bool {
	if c.Owned && now.Before(c.End.Add(transcriptWait)) {
		return true
	}
	return !c.ExtLastAt.IsZero() && !c.ExtEnded && now.Before(c.ExtLastAt.Add(extQuiet))
}

func (c *Call) waitingForNotes(now time.Time) bool {
	if c.Owned && !c.NoGemini && c.APINotes == nil && now.Before(c.End.Add(notesWait)) {
		return true
	}
	// The extension writes its notes once the call ends.
	return len(c.ExtLines) > 0 && c.ExtNotes == nil && !c.ExtEnded && now.Before(c.ExtLastAt.Add(extQuiet))
}

func (c *Call) noTranscriptReason() string {
	switch {
	case c.ExtProblem != "":
		return c.ExtProblem
	case c.Owned && c.ConferenceID == "":
		return "nobody joined, or joined from a phone with transcription off"
	case c.Owned:
		return "Meet made no transcript"
	}
	return "not Marek's Meet, and no captions from the Snoop extension (joined from a phone, or not joined)"
}

// Store keeps one JSON file per call in DataDir/snoop/calls.
type Store struct {
	dir   string
	mu    sync.Mutex
	calls map[string]*Call

	devices map[string]Device // sha256(token) -> device
}

// Device is one Chrome running the Snoop extension.
type Device struct {
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
	Seen    time.Time `json:"seen,omitempty"`
}

// OpenStore loads every call from disk.
func OpenStore(dataDir string) (*Store, error) {
	dir := filepath.Join(dataDir, "snoop")
	if err := os.MkdirAll(filepath.Join(dir, "calls"), 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, calls: make(map[string]*Call), devices: make(map[string]Device)}
	files, err := filepath.Glob(filepath.Join(dir, "calls", "*.json"))
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var c Call
		if err := json.Unmarshal(data, &c); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		s.calls[c.ID] = &c
	}
	if data, err := os.ReadFile(filepath.Join(dir, "devices.json")); err == nil {
		if err := json.Unmarshal(data, &s.devices); err != nil {
			return nil, fmt.Errorf("devices.json: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return s, nil
}

var unsafeID = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// CallID turns a calendar event id, or a meeting code + date for ad-hoc
// Meets, into a file-safe id.
func CallID(s string) string {
	return unsafeID.ReplaceAllString(s, "_")
}

// Get returns a copy of the call.
func (s *Store) Get(id string) (Call, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.calls[id]
	if !ok {
		return Call{}, false
	}
	return *c, true
}

// Update runs fn on the call (creating it when missing) and saves it.
func (s *Store) Update(id string, fn func(c *Call)) (Call, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.calls[id]
	if !ok {
		c = &Call{ID: id}
	}
	before, _ := json.Marshal(c)
	fn(c)
	after, _ := json.Marshal(c)
	if ok && string(before) == string(after) {
		return *c, nil
	}
	c.Updated = time.Now().UTC()
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return *c, err
	}
	path := filepath.Join(s.dir, "calls", id+".json")
	if err := writeFileAtomic(path, data); err != nil {
		return *c, err
	}
	s.calls[id] = c
	return *c, nil
}

// All returns every call, newest first.
func (s *Store) All() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Call, 0, len(s.calls))
	for _, c := range s.calls {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.After(out[j].Start) })
	return out
}

// FindByCode returns the call with this meeting code closest to t, within
// the window a call can plausibly run over its calendar slot.
func (s *Store) FindByCode(code string, t time.Time) (Call, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *Call
	var bestGap time.Duration
	for _, c := range s.calls {
		if c.MeetingCode != code || c.Cancelled {
			continue
		}
		if t.Before(c.Start.Add(-45*time.Minute)) || t.After(c.End.Add(3*time.Hour)) {
			continue
		}
		gap := t.Sub(c.Start)
		if gap < 0 {
			gap = -gap
		}
		if best == nil || gap < bestGap {
			best, bestGap = c, gap
		}
	}
	if best == nil {
		return Call{}, false
	}
	return *best, true
}

// Search returns the calls whose title, people, notes or transcript contain
// every word of q (case-insensitive).
func Search(calls []Call, q string) []Call {
	words := strings.Fields(strings.ToLower(q))
	if len(words) == 0 {
		return calls
	}
	var out []Call
	for _, c := range calls {
		hay := strings.ToLower(searchText(&c))
		match := true
		for _, w := range words {
			if !strings.Contains(hay, w) {
				match = false
				break
			}
		}
		if match {
			out = append(out, c)
		}
	}
	return out
}

func searchText(c *Call) string {
	var b strings.Builder
	b.WriteString(c.Title + "\n" + c.Organizer + "\n")
	for _, a := range c.Attendees {
		b.WriteString(a.Name + " " + a.Email + "\n")
	}
	if n := c.Notes(); n != nil {
		b.WriteString(n.Summary + "\n" + strings.Join(n.NextSteps, "\n") + "\n")
	}
	for _, l := range c.Lines() {
		b.WriteString(l.Speaker + ": " + l.Text + "\n")
	}
	return b.String()
}

// --- Devices (Snoop extension tokens) --------------------------------------

// NewDevice issues a token for one Chrome. Only its hash is kept.
func (s *Store) NewDevice(name string) (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := "snp_" + hex.EncodeToString(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.devices[tokenHash(token)] = Device{Name: name, Created: time.Now().UTC()}
	return token, s.saveDevicesLocked()
}

// Device returns the device a token belongs to, and marks it seen.
func (s *Store) Device(token string) (Device, bool) {
	if token == "" {
		return Device{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h := tokenHash(token)
	d, ok := s.devices[h]
	if !ok {
		return Device{}, false
	}
	if time.Since(d.Seen) > time.Minute {
		d.Seen = time.Now().UTC()
		s.devices[h] = d
		_ = s.saveDevicesLocked()
	}
	return d, true
}

// Devices lists the devices by hash, for /admin.
func (s *Store) Devices() map[string]Device {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]Device, len(s.devices))
	for k, v := range s.devices {
		out[k] = v
	}
	return out
}

// RemoveDevice revokes a device's token.
func (s *Store) RemoveDevice(hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.devices, hash)
	return s.saveDevicesLocked()
}

func (s *Store) saveDevicesLocked() error {
	data, err := json.MarshalIndent(s.devices, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(s.dir, "devices.json"), data)
}

func tokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
