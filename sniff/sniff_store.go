package sniff

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sreday/badger/event"
	"github.com/sreday/badger/luma"
	"github.com/sreday/badger/web"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Sniff: live questions and feedback for every session of an event. The
// audience opens /q/<code> from the QR on the speaker's slide; the speaker
// follows along on /sniff/<token>. Sessions are in sniff_sessions.json, posts
// in an append-only events/<event>/sniff.jsonl, apart from the leads, with
// their own lock, so a busy room never slows the sponsors' scanners down.
//
// Privacy: an anonymous post carries no name, no guest and nothing about the
// phone. Votes and ratings use random keys the phone makes per session, so
// nothing links one post to another. Test posts (before the event goes live)
// are kept in memory only and vanish when it does. A question its author
// deletes is taken out of the file too.

// SniffSession is one session with its audience code and speaker link.
type SniffSession struct {
	Code       string    `json:"code"`           // /q/<code>, on the slide
	Token      string    `json:"token"`          // /sniff/<token>, the speaker's page
	Slug       string    `json:"slug,omitempty"` // older /sniff/<slug> by first name; no longer given to new sessions
	PIN        string    `json:"pin"`            // 4 digits: opens the speaker's page from the event's one QR
	EventID    string    `json:"event_id"`
	EventName  string    `json:"event_name"`
	EventTZ    string    `json:"event_tz"`
	EventStart time.Time `json:"event_start"`
	EventEnd   time.Time `json:"event_end"`
	Key        string    `json:"key"` // the talk page's short_url
	Title      string    `json:"title"`
	Who        string    `json:"who"`
	Org        string    `json:"org,omitempty"`
	Room       string    `json:"room,omitempty"`
	Kind       string    `json:"kind,omitempty"`
	Start      time.Time `json:"start,omitempty"`
	End        time.Time `json:"end,omitempty"`
	Hidden     bool      `json:"hidden,omitempty"` // taken off the admin list
	Manual     bool      `json:"manual,omitempty"` // added by hand, not on the schedule
	Gone       bool      `json:"gone,omitempty"`   // no longer on the schedule (the QR keeps working)
	CreatedAt  time.Time `json:"created_at"`
}

func (s SniffSession) window() event.Window {
	return event.Window{Start: s.EventStart, End: s.EventEnd}
}

// Phase is the event's phase: test until 6 hours before the start, live
// until 6 hours after the end, then closed.
func (s SniffSession) Phase(now time.Time) string { return s.window().PhaseAt(now) }

// Open says whether the audience can still post: a day longer than live,
// for feedback on the way home.
func (s SniffSession) Open(now time.Time) bool {
	if s.Phase(now) != event.PhaseClosed {
		return true
	}
	_, closesAt := s.window().Bounds()
	return now.Before(closesAt.Add(event.LateSync))
}

// Speakers are the session's speakers, for name matching.
func (s SniffSession) Speakers() []Who { return speakersOf(s.Who, s.Org) }

// Question is one audience question.
type Question struct {
	ID        string          `json:"id"`
	At        time.Time       `json:"at"`
	Text      string          `json:"text"`
	Name      string          `json:"name,omitempty"` // as typed; empty = anonymous
	Who       *Who            `json:"who,omitempty"`  // the one guest the name matched
	Votes     int             `json:"votes"`
	Answered  bool            `json:"answered,omitempty"`
	Hidden    bool            `json:"hidden,omitempty"`
	Important bool            `json:"important,omitempty"` // the speaker's pick: top of their list, in gold
	Test      bool            `json:"test,omitempty"`
	del       string          // sha256 of the author's delete secret
	voters    map[string]bool // random per-phone, per-session keys
}

// Rating is one phone's rating of a session (the latest one counts).
type Rating struct {
	Key     string    `json:"-"`
	At      time.Time `json:"at"`
	Stars   int       `json:"stars"`
	Comment string    `json:"comment,omitempty"`
	Name    string    `json:"name,omitempty"`
	Who     *Who      `json:"who,omitempty"`
	Test    bool      `json:"test,omitempty"`
}

// sniffRec is one line of sniff.jsonl: a question (q), a vote (v), a rating
// (r), the speaker's answered/hide (m) or a deletion (d, only in memory: the
// file is rewritten without the question).
type sniffRec struct {
	T     string    `json:"t"`
	S     string    `json:"s"`
	ID    string    `json:"id,omitempty"`
	At    time.Time `json:"at"`
	Text  string    `json:"text,omitempty"`
	Name  string    `json:"name,omitempty"`
	Who   *Who      `json:"who,omitempty"`
	Del   string    `json:"del,omitempty"`
	Q     string    `json:"q,omitempty"`
	K     string    `json:"k,omitempty"`
	On    bool      `json:"on,omitempty"`
	Stars int       `json:"stars,omitempty"`
	Mod   string    `json:"mod,omitempty"` // answered, unanswered, hide, show, important, unimportant
}

type sniffData struct {
	version int64
	qs      []*Question
	byID    map[string]*Question
	ratings map[string]*Rating
}

func newSniffData() *sniffData {
	return &sniffData{byID: map[string]*Question{}, ratings: map[string]*Rating{}}
}

// SniffStore keeps the sessions and their posts.
type SniffStore struct {
	dir string

	mu       sync.Mutex
	sessions map[string]*SniffSession // code -> session
	byToken  map[string]string        // speaker token -> code
	data     map[string]*sniffData    // code -> posts
	test     map[string]*sniffData    // code -> test posts, memory only
	logs     map[string][]sniffRec    // event -> the file's records, for rewriting
	events   map[string]string        // event id -> its code: /q/<code> lists every speaker
	secrets  map[string]string        // event id -> its secret: /sniff/<secret>, sniff_links.go
	clock    int64                    // version counter: changes on every post
}

func OpenSniffStore(dir string) (*SniffStore, error) {
	s := &SniffStore{
		dir:      dir,
		sessions: map[string]*SniffSession{},
		byToken:  map[string]string{},
		data:     map[string]*sniffData{},
		test:     map[string]*sniffData{},
		logs:     map[string][]sniffRec{},
		events:   map[string]string{},
		secrets:  map[string]string{},
		clock:    time.Now().UnixMilli() * 1000, // a restart never repeats a version
	}
	raw, err := os.ReadFile(filepath.Join(dir, "sniff_sessions.json"))
	if err == nil {
		var list []*SniffSession
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, fmt.Errorf("parsing sniff_sessions.json: %w", err)
		}
		pins := false
		for _, ss := range list {
			if ss.PIN == "" { // made before PINs
				if ss.PIN, err = newPIN(); err != nil {
					return nil, err
				}
				pins = true
			}
			s.sessions[ss.Code] = ss
			s.byToken[ss.Token] = ss.Code
		}
		if pins {
			if err := s.saveSessions(); err != nil {
				return nil, err
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "sniff_events.json")); err == nil {
		if err := json.Unmarshal(raw, &s.events); err != nil {
			return nil, fmt.Errorf("parsing sniff_events.json: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := s.loadSecrets(); err != nil {
		return nil, fmt.Errorf("parsing sniff_secrets.json: %w", err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "events", "*", "sniff.jsonl"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var rec sniffRec
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				fmt.Fprintf(os.Stderr, "%s:%d: skipping bad line: %v\n", f, i+1, err)
				continue
			}
			ss := s.sessions[rec.S]
			if ss == nil {
				continue
			}
			s.logs[ss.EventID] = append(s.logs[ss.EventID], rec)
			s.apply(s.dataFor(rec.S, false), rec, false)
		}
	}
	return s, nil
}

// apply plays one record onto a session's posts. The caller holds s.mu.
func (s *SniffStore) apply(d *sniffData, rec sniffRec, test bool) {
	s.clock++
	d.version = s.clock
	switch rec.T {
	case "q":
		if d.byID[rec.ID] != nil {
			return
		}
		q := &Question{ID: rec.ID, At: rec.At, Text: rec.Text, Name: rec.Name, Who: rec.Who, Test: test, del: rec.Del, voters: map[string]bool{}}
		d.byID[q.ID] = q
		d.qs = append(d.qs, q)
	case "v":
		if q := d.byID[rec.Q]; q != nil {
			if rec.On {
				q.voters[rec.K] = true
			} else {
				delete(q.voters, rec.K)
			}
			q.Votes = len(q.voters)
		}
	case "r":
		d.ratings[rec.ID] = &Rating{Key: rec.ID, At: rec.At, Stars: rec.Stars, Comment: rec.Text, Name: rec.Name, Who: rec.Who, Test: test}
	case "m":
		if q := d.byID[rec.Q]; q != nil {
			switch rec.Mod {
			case "answered":
				q.Answered = true
			case "unanswered":
				q.Answered = false
			case "hide":
				q.Hidden = true
			case "show":
				q.Hidden = false
			case "important":
				q.Important = true
			case "unimportant":
				q.Important = false
			}
		}
	case "d":
		if q := d.byID[rec.Q]; q != nil {
			delete(d.byID, rec.Q)
			for i, have := range d.qs {
				if have == q {
					d.qs = append(d.qs[:i], d.qs[i+1:]...)
					break
				}
			}
		}
	}
}

func (s *SniffStore) dataFor(code string, test bool) *sniffData {
	m := s.data
	if test {
		m = s.test
	}
	d := m[code]
	if d == nil {
		d = newSniffData()
		m[code] = d
	}
	return d
}

// --- Sessions --------------------------------------------------------------

func (s *SniffStore) Session(code string) (SniffSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.sessions[strings.ToUpper(strings.TrimSpace(code))]
	if !ok {
		return SniffSession{}, false
	}
	return *ss, true
}

func (s *SniffStore) ByToken(token string) (SniffSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	code, ok := s.byToken[token]
	if !ok {
		return SniffSession{}, false
	}
	return *s.sessions[code], true
}

// Older speaker links by name: /sniff/grace (no event secret). New sessions
// no longer get one (anyone could guess them); the ones already given keep
// working, the open session winning, else the latest. Speaker links are
// now /sniff/<event secret>/<first name> (sniff_links.go).

// slugsFor are a speaker's names to try, in order: grace, grace-hopper,
// grace-hopper-2...
func slugsFor(who string) []string {
	person := strings.FieldsFunc(who, func(r rune) bool { return r == ',' || r == '&' || r == '/' })
	if len(person) == 0 {
		return nil
	}
	if i := strings.Index(strings.ToLower(person[0]), " and "); i > 0 {
		person[0] = person[0][:i]
	}
	var words []string
	for _, w := range strings.Fields(foldName(person[0])) {
		w = strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, w)
		if w != "" {
			words = append(words, w)
		}
	}
	if len(words) == 0 {
		return nil
	}
	base := words[0]
	out := []string{base}
	if len(words) > 1 {
		base += "-" + words[len(words)-1]
		out = append(out, base)
	}
	for n := 2; n < 100; n++ {
		out = append(out, fmt.Sprintf("%s-%d", base, n))
	}
	return out
}

// BySlug finds the session a speaker link by name opens.
func (s *SniffStore) BySlug(slug string, now time.Time) (SniffSession, bool) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *SniffSession
	for _, ss := range s.sessions {
		if ss.Slug != slug || slug == "" {
			continue
		}
		if best == nil || ss.Open(now) && !best.Open(now) || ss.Open(now) == best.Open(now) && ss.EventStart.After(best.EventStart) {
			best = ss
		}
	}
	if best == nil {
		return SniffSession{}, false
	}
	return *best, true
}

// Sessions returns an event's sessions in schedule order (by start, then
// room), sessions without a time last.
func (s *SniffStore) Sessions(eventID string) []SniffSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []SniffSession
	for _, ss := range s.sessions {
		if ss.EventID == eventID {
			out = append(out, *ss)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Start.IsZero() != b.Start.IsZero() {
			return !a.Start.IsZero()
		}
		if !a.Start.Equal(b.Start) {
			return a.Start.Before(b.Start)
		}
		if a.Room != b.Room {
			return a.Room < b.Room
		}
		return a.CreatedAt.Before(b.CreatedAt)
	})
	return out
}

// SyncSchedule gives every session on the schedule a code and a speaker
// link, once: a code never changes, because it's printed on slides. A talk
// whose page key changed (an edited title) keeps its code when the speaker's
// name finds it; a session that left the schedule is flagged, not deleted.
func (s *SniffStore) SyncSchedule(ev luma.Event, items []SchedItem) (added int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	start, end := ev.Times()
	byKey := map[string]*SniffSession{}
	for _, ss := range s.sessions {
		if ss.EventID == ev.APIId {
			cp := *ss
			cp.EventName, cp.EventTZ, cp.EventStart, cp.EventEnd = ev.Name, ev.Timezone, start, end
			s.sessions[ss.Code] = &cp
			byKey[cp.Key] = &cp
		}
	}
	onSchedule := map[string]bool{}
	for _, it := range items {
		onSchedule[it.Key] = true
	}
	for _, it := range items {
		ss := byKey[it.Key]
		if ss == nil {
			// Same speaker, key no longer on the schedule: the same talk,
			// renamed. Only when exactly one session fits.
			var cands []*SniffSession
			for _, have := range byKey {
				if !have.Manual && !onSchedule[have.Key] && foldName(have.Who) == foldName(it.Who) && foldName(have.Who) != "" {
					cands = append(cands, have)
				}
			}
			if len(cands) == 1 {
				ss = cands[0]
				delete(byKey, ss.Key)
				ss.Key = it.Key
				byKey[it.Key] = ss
			}
		}
		if ss == nil {
			code, err := s.newCode()
			if err != nil {
				return added, err
			}
			tok, err := randomHex(16)
			if err != nil {
				return added, err
			}
			pin, err := newPIN()
			if err != nil {
				return added, err
			}
			ss = &SniffSession{Code: code, Token: tok, PIN: pin, EventID: ev.APIId, EventName: ev.Name, EventTZ: ev.Timezone,
				EventStart: start, EventEnd: end, Key: it.Key, CreatedAt: time.Now().UTC()}
			s.sessions[code] = ss
			s.byToken[tok] = code
			byKey[it.Key] = ss
			added++
		}
		ss.Title, ss.Who, ss.Org, ss.Room, ss.Kind, ss.Start, ss.End, ss.Gone = it.Title, it.Who, it.Org, it.Room, it.Kind, it.Start, it.End, false
	}
	for _, ss := range byKey {
		if !ss.Manual && !onSchedule[ss.Key] {
			if !ss.Gone && notSpeaker(ss.Who, ss.Title) {
				ss.Hidden = true // a slot without a speaker (the opening, a break): out of the lists
			}
			ss.Gone = true
		}
	}
	return added, s.saveSessions()
}

// AddSession adds a session by hand (a panel, a side workshop).
func (s *SniffStore) AddSession(ev luma.Event, title, who string) (SniffSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	code, err := s.newCode()
	if err != nil {
		return SniffSession{}, err
	}
	tok, err := randomHex(16)
	if err != nil {
		return SniffSession{}, err
	}
	pin, err := newPIN()
	if err != nil {
		return SniffSession{}, err
	}
	start, end := ev.Times()
	ss := &SniffSession{Code: code, Token: tok, PIN: pin, EventID: ev.APIId, EventName: ev.Name, EventTZ: ev.Timezone, EventStart: start, EventEnd: end,
		Key: "manual-" + strings.ToLower(code), Title: strings.TrimSpace(title), Who: strings.TrimSpace(who), Kind: "talk", Manual: true, CreatedAt: time.Now().UTC()}
	s.sessions[code] = ss
	s.byToken[tok] = code
	return *ss, s.saveSessions()
}

// ResetToken gives a session a new speaker link and PIN; the old ones stop
// working.
func (s *SniffStore) ResetToken(code string) (SniffSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.sessions[code]
	if !ok {
		return SniffSession{}, errors.New("no such session")
	}
	tok, err := randomHex(16)
	if err != nil {
		return SniffSession{}, err
	}
	pin, err := newPIN()
	if err != nil {
		return SniffSession{}, err
	}
	cp := *ss
	delete(s.byToken, cp.Token)
	cp.Token, cp.PIN = tok, pin
	s.sessions[code] = &cp
	s.byToken[tok] = code
	return cp, s.saveSessions()
}

// SetHidden takes a session off (or back onto) the admin list.
func (s *SniffStore) SetHidden(code string, hidden bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.sessions[code]
	if !ok {
		return errors.New("no such session")
	}
	cp := *ss
	cp.Hidden = hidden
	s.sessions[code] = &cp
	return s.saveSessions()
}

// codeAlphabet has no look-alikes (0/O, 1/I/L, 2/Z, 5/S, 8/B, 6/G).
const codeAlphabet = "ACDEFHJKMNPRTUVWXY3479"

// newCode picks an unused 4-letter code (5 once 4 gets crowded). The caller
// holds s.mu.
func (s *SniffStore) newCode() (string, error) {
	n := 4
	for tries := 0; ; tries++ {
		if tries == 50 {
			n = 5
		}
		b := make([]byte, n)
		for i := range b {
			k, err := rand.Int(rand.Reader, big.NewInt(int64(len(codeAlphabet))))
			if err != nil {
				return "", err
			}
			b[i] = codeAlphabet[k.Int64()]
		}
		if _, taken := s.sessions[string(b)]; !taken && !s.isEventCode(string(b)) {
			return string(b), nil
		}
	}
}

// newPIN is a speaker's 4-digit PIN.
func newPIN() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(10000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%04d", n.Int64()), nil
}

// --- Event codes ---------------------------------------------------------------

// The event's one QR: /q/<code> lists every speaker. Event codes share the
// sessions' alphabet and never clash with a session code.

func (s *SniffStore) isEventCode(code string) bool {
	for _, c := range s.events {
		if c == code {
			return true
		}
	}
	return false
}

// EventCode is the event's code, made (once, for good) if it has none yet.
func (s *SniffStore) EventCode(eventID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.events[eventID]; ok {
		return c, nil
	}
	code, err := s.newCode()
	if err != nil {
		return "", err
	}
	s.events[eventID] = code
	data, err := json.MarshalIndent(s.events, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return "", err
	}
	tmp := filepath.Join(s.dir, "sniff_events.json.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", err
	}
	return code, os.Rename(tmp, filepath.Join(s.dir, "sniff_events.json"))
}

// HasEventCode is the event's code without making one.
func (s *SniffStore) HasEventCode(eventID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.events[eventID]
	return c, ok
}

// EventByCode finds the event a code belongs to.
func (s *SniffStore) EventByCode(code string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	code = strings.ToUpper(strings.TrimSpace(code))
	for id, c := range s.events {
		if c == code {
			return id, true
		}
	}
	return "", false
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// saveSessions writes sniff_sessions.json atomically. The caller holds s.mu.
func (s *SniffStore) saveSessions() error {
	list := make([]*SniffSession, 0, len(s.sessions))
	for _, ss := range s.sessions {
		list = append(list, ss)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Code < list[j].Code })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(s.dir, "sniff_sessions.json.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.dir, "sniff_sessions.json"))
}

// --- Posts -----------------------------------------------------------------

var errClosed = errors.New("this session is closed")

// post records one thing the audience or the speaker did. Test-mode posts
// stay in memory.
func (s *SniffStore) post(code string, now time.Time, rec sniffRec) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.sessions[code]
	if !ok {
		return errors.New("no such session")
	}
	test := ss.Phase(now) == event.PhaseTest
	s.purgeTest(ss, now)
	rec.S = code
	d := s.dataFor(code, test)
	if test {
		s.apply(d, rec, true)
		return nil
	}
	if err := s.appendRec(ss.EventID, rec); err != nil {
		return err
	}
	s.logs[ss.EventID] = append(s.logs[ss.EventID], rec)
	s.apply(d, rec, false)
	return nil
}

// purgeTest drops a session's test posts once the event is live. The
// caller holds s.mu.
func (s *SniffStore) purgeTest(ss *SniffSession, now time.Time) {
	if _, ok := s.test[ss.Code]; ok && ss.Phase(now) != event.PhaseTest {
		delete(s.test, ss.Code)
		s.clock++
		s.dataFor(ss.Code, false).version = s.clock
	}
}

func (s *SniffStore) appendRec(eventID string, rec sniffRec) error {
	dir := filepath.Join(s.dir, "events", web.SafeName(eventID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "sniff.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// rewrite writes an event's file again from its records. The caller holds
// s.mu.
func (s *SniffStore) rewrite(eventID string) error {
	dir := filepath.Join(s.dir, "events", web.SafeName(eventID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var b strings.Builder
	for _, rec := range s.logs[eventID] {
		line, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	tmp := filepath.Join(dir, "sniff.jsonl.tmp")
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "sniff.jsonl"))
}

func hashSecret(secret string) string {
	h := sha256.Sum256([]byte("sniff-delete:" + secret))
	return hex.EncodeToString(h[:])
}

// Ask adds a question. A resent question (same id) is a no-op.
func (s *SniffStore) Ask(code string, now time.Time, id, text, name string, who *Who, secret string) error {
	if !s.openAt(code, now) {
		return errClosed
	}
	rec := sniffRec{T: "q", ID: id, At: now.UTC(), Text: text, Name: name, Who: who}
	if secret != "" {
		rec.Del = hashSecret(secret)
	}
	return s.post(code, now, rec)
}

// Vote adds (on) or takes back a phone's vote.
func (s *SniffStore) Vote(code string, now time.Time, qid, voter string, on bool) error {
	if !s.openAt(code, now) {
		return errClosed
	}
	s.mu.Lock()
	d := s.current(code, now)
	q := d.byID[qid]
	same := q != nil && q.voters[voter] == on
	s.mu.Unlock()
	if q == nil {
		return errors.New("no such question")
	}
	if same {
		return nil
	}
	return s.post(code, now, sniffRec{T: "v", At: now.UTC(), Q: qid, K: voter, On: on})
}

// Rate keeps a phone's rating of the session (a new one replaces it).
func (s *SniffStore) Rate(code string, now time.Time, key string, stars int, comment, name string, who *Who) error {
	if !s.openAt(code, now) {
		return errClosed
	}
	return s.post(code, now, sniffRec{T: "r", ID: key, At: now.UTC(), Stars: stars, Text: comment, Name: name, Who: who})
}

// Mod is the speaker's answered / unanswered / hide / show / important /
// unimportant.
func (s *SniffStore) Mod(code string, now time.Time, qid, mod string) error {
	switch mod {
	case "answered", "unanswered", "hide", "show", "important", "unimportant":
	default:
		return errors.New("unknown action")
	}
	s.mu.Lock()
	q := s.current(code, now).byID[qid]
	s.mu.Unlock()
	if q == nil {
		return errors.New("no such question")
	}
	return s.post(code, now, sniffRec{T: "m", At: now.UTC(), Q: qid, Mod: mod})
}

// Delete takes a question out for good, with the secret only its author's
// phone has. The file is rewritten without it.
func (s *SniffStore) Delete(code string, now time.Time, qid, secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.sessions[code]
	if !ok {
		return errors.New("no such session")
	}
	s.purgeTest(ss, now)
	test := s.test[code] != nil && s.test[code].byID[qid] != nil
	d := s.dataFor(code, test)
	q := d.byID[qid]
	if q == nil {
		return nil // already gone
	}
	if secret == "" || q.del == "" || q.del != hashSecret(secret) {
		return errors.New("not your question")
	}
	s.apply(d, sniffRec{T: "d", Q: qid}, test)
	if test {
		return nil
	}
	var keep []sniffRec
	for _, rec := range s.logs[ss.EventID] {
		if rec.S == code && (rec.ID == qid && rec.T == "q" || rec.Q == qid) {
			continue
		}
		keep = append(keep, rec)
	}
	s.logs[ss.EventID] = keep
	return s.rewrite(ss.EventID)
}

func (s *SniffStore) openAt(code string, now time.Time) bool {
	ss, ok := s.Session(code)
	return ok && ss.Open(now)
}

// current is the posts the session shows now: test posts in test mode. The
// caller holds s.mu.
func (s *SniffStore) current(code string, now time.Time) *sniffData {
	ss := s.sessions[code]
	if ss != nil {
		s.purgeTest(ss, now)
		if ss.Phase(now) == event.PhaseTest {
			return s.dataFor(code, true)
		}
	}
	return s.dataFor(code, false)
}

// SniffView is a copy of a session's posts at one moment.
type SniffView struct {
	Version   int64
	Questions []Question
	Ratings   []Rating
}

// View returns the session's posts: questions in arrival order, ratings
// newest first.
func (s *SniffStore) View(code string, now time.Time) SniffView {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.current(code, now)
	v := SniffView{Version: d.version}
	for _, q := range d.qs {
		c := *q
		c.voters = nil
		v.Questions = append(v.Questions, c)
	}
	for _, r := range d.ratings {
		v.Ratings = append(v.Ratings, *r)
	}
	sort.Slice(v.Ratings, func(i, j int) bool { return v.Ratings[i].At.After(v.Ratings[j].At) })
	return v
}

// Version is the session's version alone, for "nothing new" answers.
func (s *SniffStore) Version(code string, now time.Time) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current(code, now).version
}

// Counts returns how many questions and ratings a session has, and the
// average rating (real posts, or test posts in test mode).
func (s *SniffStore) Counts(code string, now time.Time) (questions, ratings int, avg float64) {
	v := s.View(code, now)
	sum := 0
	for _, r := range v.Ratings {
		sum += r.Stars
	}
	if len(v.Ratings) > 0 {
		avg = float64(sum) / float64(len(v.Ratings))
	}
	return len(v.Questions), len(v.Ratings), avg
}

// Voted says which questions this voter key voted for.
func (s *SniffStore) Voted(code string, now time.Time, voter string) map[string]bool {
	out := map[string]bool{}
	if voter == "" {
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, q := range s.current(code, now).qs {
		if q.voters[voter] {
			out[q.ID] = true
		}
	}
	return out
}
