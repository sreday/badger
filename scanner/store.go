package main

import (
	"crypto/rand"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

// Sponsor is one sponsor link (magic link + PIN) for one event.
type Sponsor struct {
	Token      string    `json:"token"`
	PIN        string    `json:"pin"`
	Name       string    `json:"name"`
	EventID    string    `json:"event_id"`
	EventName  string    `json:"event_name"`
	EventTZ    string    `json:"event_tz"`
	EventStart time.Time `json:"event_start"`
	EventEnd   time.Time `json:"event_end"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at"`        // last PIN entry or sync; zero if never opened
	DeletedAt  time.Time `json:"deleted_at"`          // leads deleted after the retention period
	FromSite   bool      `json:"from_site,omitempty"` // created from the event page's sponsor list
	MailRoot   string    `json:"mail_root,omitempty"` // Message-ID of the first leads email, so later ones thread under it
	MailLast   string    `json:"mail_last,omitempty"` // Message-ID of the latest one
	// PurchasedLeads: the sponsor bought the event's leads, so their
	// dashboard can download the whole list (admin card checkbox).
	PurchasedLeads bool `json:"purchased_leads,omitempty"`
}

// A sponsor link goes through these phases, all from the Luma event times:
// test until 6h before the start, live until 6h after the end, then closed
// (download only) for 12 months (90 days for events before longKeepFrom),
// after which its leads are deleted.
const (
	PhaseTest    = "test"
	PhaseLive    = "live"
	PhaseClosed  = "closed"
	PhaseDeleted = "deleted"

	liveBefore  = 6 * time.Hour // live this long before the start
	closeAfter  = 6 * time.Hour // closes this long after the end
	keepLeads   = 90 * 24 * time.Hour
	testLeadTTL = 60 * time.Minute
	lateSync    = 24 * time.Hour // offline scans made before closing are accepted this long after it
	pinReuse    = 100 * 24 * time.Hour
)

// longKeepFrom is the first event start that keeps leads for 12 months.
// Attendees of earlier events registered under T&C that promised deletion
// 90 days after the event, so their leads keep that promise.
var longKeepFrom = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)

// keepsLong reports whether the link's leads are kept for 12 months.
func (sp Sponsor) keepsLong() bool {
	return !sp.EventStart.Before(longKeepFrom)
}

// KeptFor is how long the leads stay after closing, for people.
func (sp Sponsor) KeptFor() string {
	if sp.keepsLong() {
		return "12 months"
	}
	return "90 days"
}

// Window returns when the link goes live, closes and deletes its leads.
// Links without event times (made before times were stored) are always live.
func (sp Sponsor) Window() (liveFrom, closesAt, deletesAt time.Time) {
	if sp.EventStart.IsZero() {
		return time.Time{}, time.Time{}, time.Time{}
	}
	end := sp.EventEnd
	if end.IsZero() {
		end = sp.EventStart.Add(12 * time.Hour)
	}
	closesAt = end.Add(closeAfter)
	deletesAt = closesAt.Add(keepLeads)
	if sp.keepsLong() {
		deletesAt = closesAt.AddDate(1, 0, 0)
	}
	return sp.EventStart.Add(-liveBefore), closesAt, deletesAt
}

// PhaseWindowDeletes returns when the link's leads are deleted.
func (sp Sponsor) PhaseWindowDeletes() time.Time {
	_, _, deletesAt := sp.Window()
	return deletesAt
}

// PhaseAt returns the link's phase at t.
func (sp Sponsor) PhaseAt(t time.Time) string {
	if !sp.DeletedAt.IsZero() {
		return PhaseDeleted
	}
	liveFrom, closesAt, deletesAt := sp.Window()
	switch {
	case liveFrom.IsZero():
		return PhaseLive
	case t.Before(liveFrom):
		return PhaseTest
	case t.Before(closesAt):
		return PhaseLive
	case t.Before(deletesAt):
		return PhaseClosed
	default:
		return PhaseDeleted
	}
}

// lastActive is when the link last mattered, for PIN reuse: its last use,
// creation, or closing, whichever is latest.
func (sp Sponsor) lastActive() time.Time {
	t := sp.CreatedAt
	if sp.LastUsedAt.After(t) {
		t = sp.LastUsedAt
	}
	if _, closesAt, _ := sp.Window(); closesAt.After(t) {
		t = closesAt
	}
	return t
}

// Lead is one conversation: a scan plus the sponsor's rating and note.
type Lead struct {
	ScanID     string    `json:"scan_id"`
	Sponsor    string    `json:"sponsor"` // sponsor token
	ScannedAt  time.Time `json:"scanned_at"`
	ScannedBy  string    `json:"scanned_by"`
	Raw        string    `json:"raw"`
	Key        ScanKey   `json:"key"`
	Identity   string    `json:"identity"`
	Rating     string    `json:"rating"` // the team's: see teamRating
	Note       string    `json:"note"`   // everyone's notes with their names: see teamNote
	Takes      []Take    `json:"takes,omitempty"`
	ReceivedAt time.Time `json:"received_at"`
	Test       bool      `json:"test,omitempty"` // test-mode scan: memory only, gone after an hour
}

// record is one line of leads.jsonl: a new scan ("scan"), someone else on
// the team scanning the same person ("scanner"), or one person's rating and
// note ("take", by Author). "update" is the old single rating and note,
// from before takes; it counts as the first scanner's take.
type record struct {
	Type   string `json:"type"`
	Author string `json:"author,omitempty"`
	Lead
}

// Store keeps sponsors in sponsors.json and leads in an append-only
// <event>/leads.jsonl per event, both under dir.
type Store struct {
	dir string

	mu       sync.Mutex
	sponsors map[string]*Sponsor
	leads    map[string][]*Lead // sponsor token -> leads in arrival order
	byScanID map[string]*Lead

	// Test-mode leads are kept in memory only, never on disk.
	testLeads  map[string][]*Lead
	testByScan map[string]*Lead

	// The scanning history per sponsor token, real and test.
	history     map[string][]Event
	testHistory map[string][]Event
}

func OpenStore(dir string) (*Store, error) {
	s := &Store{
		dir:         dir,
		sponsors:    make(map[string]*Sponsor),
		leads:       make(map[string][]*Lead),
		byScanID:    make(map[string]*Lead),
		testLeads:   make(map[string][]*Lead),
		testByScan:  make(map[string]*Lead),
		history:     make(map[string][]Event),
		testHistory: make(map[string][]Event),
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	data, err := os.ReadFile(filepath.Join(dir, "sponsors.json"))
	if err == nil {
		var list []*Sponsor
		if err := json.Unmarshal(data, &list); err != nil {
			return nil, fmt.Errorf("parsing sponsors.json: %w", err)
		}
		for _, sp := range list {
			s.sponsors[sp.Token] = sp
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	files, _ := filepath.Glob(filepath.Join(dir, "events", "*", "leads.jsonl"))
	for _, f := range files {
		if err := s.load(f); err != nil {
			return nil, fmt.Errorf("loading %s: %w", f, err)
		}
	}
	return s, nil
}

func (s *Store) load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for i, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			// A torn last line after a crash is skipped, not fatal.
			fmt.Fprintf(os.Stderr, "%s:%d: skipping bad line: %v\n", path, i+1, err)
			continue
		}
		s.apply(rec)
	}
	return nil
}

func (s *Store) apply(rec record) {
	switch rec.Type {
	case "scan":
		if _, ok := s.byScanID[rec.ScanID]; ok {
			return
		}
		l := rec.Lead
		rating, note := l.Rating, l.Note
		l.Rating, l.Note, l.Takes = "", "", nil
		s.byScanID[l.ScanID] = &l
		s.leads[l.Sponsor] = append(s.leads[l.Sponsor], &l)
		s.history[l.Sponsor] = append(s.history[l.Sponsor], Event{At: l.ScannedAt, By: l.ScannedBy, Action: "scan", ScanID: l.ScanID})
		if rating != "" || note != "" {
			s.history[l.Sponsor] = append(s.history[l.Sponsor], applyTake(&l, firstScanner(&l), rating, note, l.ScannedAt)...)
		}
	case "update":
		if l, ok := s.byScanID[rec.ScanID]; ok {
			s.history[l.Sponsor] = append(s.history[l.Sponsor], applyTake(l, firstScanner(l), rec.Rating, rec.Note, rec.ReceivedAt)...)
		}
	case "take":
		if l, ok := s.byScanID[rec.ScanID]; ok {
			s.history[l.Sponsor] = append(s.history[l.Sponsor], applyTake(l, rec.Author, rec.Rating, rec.Note, rec.ReceivedAt)...)
		}
	case "scanner":
		// Someone else on the team scanned the same person.
		if l, ok := s.byScanID[rec.ScanID]; ok {
			for _, n := range strings.Split(rec.ScannedBy, ",") {
				if n = strings.TrimSpace(n); n != "" && addScanner(l.ScannedBy, n) != l.ScannedBy {
					s.history[l.Sponsor] = append(s.history[l.Sponsor], Event{At: rec.ReceivedAt, By: n, Action: "rescan", ScanID: l.ScanID})
				}
			}
			l.ScannedBy = rec.ScannedBy
		}
	}
}

// addScanner adds a name to "Scanned By", keeping the first scanner first.
func addScanner(list, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return list
	}
	for _, n := range strings.Split(list, ",") {
		if strings.EqualFold(strings.TrimSpace(n), name) {
			return list
		}
	}
	if strings.TrimSpace(list) == "" {
		return name
	}
	return list + ", " + name
}

func (s *Store) appendRecord(eventID string, rec record) error {
	dir := filepath.Join(s.dir, "events", safeName(eventID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "leads.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
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

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, s)
}

// Sponsor returns the sponsor for a token.
func (s *Store) Sponsor(token string) (*Sponsor, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sp, ok := s.sponsors[token]
	return sp, ok
}

// Sponsors returns all sponsors, newest first.
func (s *Store) Sponsors() []Sponsor {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Sponsor, 0, len(s.sponsors))
	for _, sp := range s.sponsors {
		out = append(out, *sp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// AddSponsor creates a sponsor with a fresh token and 4-digit PIN.
func (s *Store) AddSponsor(name string, ev LumaEvent) (*Sponsor, error) {
	sp, err := newSponsor(name, ev)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if sp.PIN, err = s.uniquePIN(sp.EventID, time.Now()); err != nil {
		return nil, err
	}
	s.sponsors[sp.Token] = sp
	if err := s.saveSponsors(); err != nil {
		delete(s.sponsors, sp.Token)
		return nil, err
	}
	return sp, nil
}

// SyncSiteSponsors makes the event's sponsor links match the names listed
// on its page (compared case-insensitively), each new one with its own PIN.
// It also brings every link on the event up to date with Luma's name and
// times. Links from the page that are no longer listed are removed while
// they're untouched (never opened, no leads) and prune is set; anything in
// use stays. It returns how many it added and removed.
func (s *Store) SyncSiteSponsors(ev LumaEvent, names []string, prune bool) (added, removed int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	listed := make(map[string]bool, len(names))
	for _, name := range names {
		listed[strings.ToLower(name)] = true
	}
	var dropped []*Sponsor
	for tok, sp := range s.sponsors {
		if prune && sp.FromSite && sp.EventID == ev.APIId && sp.LastUsedAt.IsZero() && len(s.leads[tok]) == 0 && !listed[strings.ToLower(sp.Name)] {
			dropped = append(dropped, sp)
			delete(s.sponsors, tok)
		}
	}
	changed := s.refreshEvent(ev)

	var newTokens []string
	for _, name := range names {
		if s.hasSponsor(ev.APIId, name) {
			continue
		}
		sp, err := newSponsor(name, ev)
		if err != nil {
			return 0, 0, err
		}
		sp.FromSite = true
		if sp.PIN, err = s.uniquePIN(sp.EventID, time.Now()); err != nil {
			return 0, 0, err
		}
		s.sponsors[sp.Token] = sp
		newTokens = append(newTokens, sp.Token)
	}
	if len(newTokens) == 0 && len(dropped) == 0 && !changed {
		return 0, 0, nil
	}
	if err := s.saveSponsors(); err != nil {
		for _, tok := range newTokens {
			delete(s.sponsors, tok)
		}
		for _, sp := range dropped {
			s.sponsors[sp.Token] = sp
		}
		return 0, 0, err
	}
	return len(newTokens), len(dropped), nil
}

func (s *Store) hasSponsor(eventID, name string) bool {
	for _, sp := range s.sponsors {
		if sp.EventID == eventID && strings.EqualFold(sp.Name, name) {
			return true
		}
	}
	return false
}

// SetMailThread records the Message-IDs that the next leads email replies to.
func (s *Store) SetMailThread(token, root, last string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sp, ok := s.sponsors[token]
	if !ok {
		return fmt.Errorf("unknown sponsor")
	}
	cp := *sp
	cp.MailRoot, cp.MailLast = root, last
	s.sponsors[token] = &cp
	return s.saveSponsors()
}

// SetPurchasedLeads turns the full leads list on or off for a sponsor.
func (s *Store) SetPurchasedLeads(token string, on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sp, ok := s.sponsors[token]
	if !ok {
		return fmt.Errorf("unknown sponsor")
	}
	cp := *sp
	cp.PurchasedLeads = on
	s.sponsors[token] = &cp
	return s.saveSponsors()
}

// NewPIN gives a sponsor a fresh PIN. Devices signed in with the old PIN
// are signed out, because the session cookie is derived from it.
func (s *Store) NewPIN(token string) (*Sponsor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.sponsors[token]
	if !ok {
		return nil, fmt.Errorf("unknown sponsor")
	}
	pin, err := s.uniquePIN(old.EventID, time.Now())
	if err != nil {
		return nil, err
	}
	// Swap in a copy so readers holding the old pointer never see a torn PIN.
	sp := *old
	sp.PIN = pin
	s.sponsors[token] = &sp
	if err := s.saveSponsors(); err != nil {
		s.sponsors[token] = old
		return nil, err
	}
	return &sp, nil
}

func newSponsor(name string, ev LumaEvent) (*Sponsor, error) {
	tok := make([]byte, 16)
	if _, err := rand.Read(tok); err != nil {
		return nil, err
	}
	start, end := ev.Times()
	return &Sponsor{
		Token:      hex.EncodeToString(tok),
		Name:       name,
		EventID:    ev.APIId,
		EventName:  ev.Name,
		EventTZ:    ev.Timezone,
		EventStart: start,
		EventEnd:   end,
		CreatedAt:  time.Now().UTC(),
	}, nil
}

// refreshEvent copies Luma's current name, time zone and times onto every
// link of the event, so a rescheduled event moves its phases too. The caller
// holds s.mu and saves.
func (s *Store) refreshEvent(ev LumaEvent) bool {
	start, end := ev.Times()
	changed := false
	for tok, sp := range s.sponsors {
		if sp.EventID != ev.APIId {
			continue
		}
		if sp.EventName == ev.Name && sp.EventTZ == ev.Timezone && sp.EventStart.Equal(start) && sp.EventEnd.Equal(end) {
			continue
		}
		cp := *sp
		cp.EventName, cp.EventTZ, cp.EventStart, cp.EventEnd = ev.Name, ev.Timezone, start, end
		s.sponsors[tok] = &cp
		changed = true
	}
	return changed
}

// uniquePIN picks a 4-digit PIN no other link uses. A PIN frees up once its
// link has been inactive for 100 days and its leads are deleted. The caller
// holds s.mu.
func (s *Store) uniquePIN(eventID string, now time.Time) (string, error) {
	taken := make(map[string]bool)
	inEvent := make(map[string]bool)
	for _, sp := range s.sponsors {
		if sp.PIN == "" {
			continue
		}
		if now.Sub(sp.lastActive()) < pinReuse || (!sp.EventStart.IsZero() && sp.PhaseAt(now) != PhaseDeleted) {
			taken[sp.PIN] = true
		}
		if sp.EventID == eventID {
			inEvent[sp.PIN] = true
		}
	}
	for _, avoid := range []map[string]bool{taken, inEvent} {
		if len(avoid) >= 10000 {
			continue
		}
		for {
			n, err := rand.Int(rand.Reader, big.NewInt(10000))
			if err != nil {
				return "", err
			}
			if pin := fmt.Sprintf("%04d", n.Int64()); !avoid[pin] {
				return pin, nil
			}
		}
	}
	return "", fmt.Errorf("no free PIN for event %s", eventID)
}

// EnsurePINs gives a PIN to every link without one (links made before PINs
// were automatic).
func (s *Store) EnsurePINs() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for tok, sp := range s.sponsors {
		if sp.PIN != "" {
			continue
		}
		pin, err := s.uniquePIN(sp.EventID, time.Now())
		if err != nil {
			return err
		}
		cp := *sp
		cp.PIN = pin
		s.sponsors[tok] = &cp
		changed = true
	}
	if !changed {
		return nil
	}
	return s.saveSponsors()
}

// Touch records that a link was used. It saves at most hourly per link, so
// a busy scanner doesn't rewrite sponsors.json on every sync.
func (s *Store) Touch(token string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sp, ok := s.sponsors[token]
	if !ok || now.Sub(sp.LastUsedAt) < time.Hour {
		return
	}
	cp := *sp
	cp.LastUsedAt = now.UTC()
	s.sponsors[token] = &cp
	if err := s.saveSponsors(); err != nil {
		fmt.Fprintf(os.Stderr, "saving sponsors: %v\n", err)
	}
}

// DeleteExpired deletes the leads of every link past its retention period,
// and removes an event's leads file once all of its links are deleted.
// Expired test leads go too. It returns how many links it deleted.
func (s *Store) DeleteExpired(now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeTest(now)

	deleted := 0
	for tok, sp := range s.sponsors {
		if !sp.DeletedAt.IsZero() || sp.PhaseAt(now) != PhaseDeleted {
			continue
		}
		for _, l := range s.leads[tok] {
			delete(s.byScanID, l.ScanID)
		}
		delete(s.leads, tok)
		delete(s.history, tok)
		cp := *sp
		cp.DeletedAt = now.UTC()
		s.sponsors[tok] = &cp
		deleted++
	}
	if deleted == 0 {
		return 0, nil
	}
	if err := s.saveSponsors(); err != nil {
		return deleted, err
	}
	// The leads file is per event: remove it once nobody on the event has
	// leads to keep.
	keep := make(map[string]bool)
	for _, sp := range s.sponsors {
		if sp.DeletedAt.IsZero() {
			keep[sp.EventID] = true
		}
	}
	for _, sp := range s.sponsors {
		if keep[sp.EventID] {
			continue
		}
		f := filepath.Join(s.dir, "events", safeName(sp.EventID), "leads.jsonl")
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			return deleted, err
		}
	}
	return deleted, nil
}

// purgeTest drops test leads older than an hour. The caller holds s.mu.
func (s *Store) purgeTest(now time.Time) {
	for tok, ls := range s.testLeads {
		kept := ls[:0]
		for _, l := range ls {
			if now.Sub(l.ReceivedAt) < testLeadTTL {
				kept = append(kept, l)
			} else {
				delete(s.testByScan, l.ScanID)
			}
		}
		if len(kept) == 0 {
			delete(s.testLeads, tok)
		} else {
			s.testLeads[tok] = kept
		}
	}
	// The history of a test scan goes with it.
	for tok, evs := range s.testHistory {
		kept := evs[:0]
		for _, e := range evs {
			if _, ok := s.testByScan[e.ScanID]; ok {
				kept = append(kept, e)
			}
		}
		if len(kept) == 0 {
			delete(s.testHistory, tok)
		} else {
			s.testHistory[tok] = kept
		}
	}
}

func (s *Store) saveSponsors() error {
	list := make([]*Sponsor, 0, len(s.sponsors))
	for _, sp := range s.sponsors {
		list = append(list, sp)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.dir, "sponsors.json.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.dir, "sponsors.json"))
}

// AddScan stores a scan. If the scan id is already known (a retry) or the
// sponsor already scanned this person, the existing lead is returned and
// duplicate reports whether it was another person-level duplicate.
//
// Test leads (l.Test) are kept in memory only, expire after an hour, and
// may repeat the same person.
func (s *Store) AddScan(sp *Sponsor, l Lead) (lead Lead, duplicate bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l.Test {
		s.purgeTest(l.ReceivedAt)
		if existing, ok := s.testByScan[l.ScanID]; ok {
			return *existing, false, nil
		}
		// No duplicate check in test mode: one badge scanned again and
		// again is how sponsors try it out.
		l.Sponsor = sp.Token
		s.testByScan[l.ScanID] = &l
		s.testLeads[sp.Token] = append(s.testLeads[sp.Token], &l)
		s.testHistory[sp.Token] = append(s.testHistory[sp.Token], Event{At: l.ScannedAt, By: l.ScannedBy, Action: "scan", ScanID: l.ScanID})
		return l, false, nil
	}
	if existing, ok := s.byScanID[l.ScanID]; ok {
		return *existing, false, nil
	}
	for _, existing := range s.leads[sp.Token] {
		if existing.Identity == l.Identity {
			// Same person again: keep one lead, but remember everyone on
			// the team who talked to them.
			if by := addScanner(existing.ScannedBy, l.ScannedBy); by != existing.ScannedBy {
				rec := record{Type: "scanner", Lead: Lead{ScanID: existing.ScanID, Sponsor: sp.Token, ScannedBy: by, ReceivedAt: l.ReceivedAt}}
				if err := s.appendRecord(sp.EventID, rec); err != nil {
					return Lead{}, false, err
				}
				s.apply(rec)
			}
			return *existing, true, nil
		}
	}
	l.Sponsor = sp.Token
	if err := s.appendRecord(sp.EventID, record{Type: "scan", Lead: l}); err != nil {
		return Lead{}, false, err
	}
	s.apply(record{Type: "scan", Lead: l})
	return l, false, nil
}

// UpdateLead sets one person's rating and note on a lead (their take);
// everyone else's stay as they are.
func (s *Store) UpdateLead(sp *Sponsor, scanID, by, rating, note string, now time.Time) (Lead, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l, ok := s.testByScan[scanID]; ok && l.Sponsor == sp.Token {
		s.testHistory[sp.Token] = append(s.testHistory[sp.Token], applyTake(l, by, rating, note, now)...)
		return copyLead(l), nil
	}
	l, ok := s.byScanID[scanID]
	if !ok || l.Sponsor != sp.Token {
		return Lead{}, fmt.Errorf("unknown scan %q", scanID)
	}
	rec := record{Type: "take", Author: by, Lead: Lead{ScanID: scanID, Sponsor: sp.Token, Rating: rating, Note: note, ReceivedAt: now}}
	if err := s.appendRecord(sp.EventID, rec); err != nil {
		return Lead{}, err
	}
	s.apply(rec)
	return copyLead(l), nil
}

// copyLead copies a lead with its own Takes, so callers can't race the store.
func copyLead(l *Lead) Lead {
	c := *l
	c.Takes = append([]Take(nil), l.Takes...)
	return c
}

// History returns a sponsor's scanning history: the test one in test mode.
func (s *Store) History(token string, test bool, now time.Time) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.history[token]
	if test {
		s.purgeTest(now)
		src = s.testHistory[token]
	}
	return append([]Event(nil), src...)
}

// Leads returns a sponsor's leads ordered by when the conversation happened:
// the test leads still within their hour when test is set, the real ones
// otherwise.
func (s *Store) Leads(token string, test bool, now time.Time) []Lead {
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.leads[token]
	if test {
		s.purgeTest(now)
		src = s.testLeads[token]
	}
	out := make([]Lead, 0, len(src))
	for _, l := range src {
		out = append(out, copyLead(l))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ScannedAt.Before(out[j].ScannedAt) })
	return out
}

// LeadCount returns how many leads a sponsor has.
func (s *Store) LeadCount(token string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.leads[token])
}

var csvHeader = []string{"Conference name", "Time", "Lead Full Name", "Lead Job Title", "Lead Company", "Lead LinkedIn", "Rating", "Note", "Scanned by - Name", "Scanned by - Email"}

// scannerNamer turns a lead's "Scanned by" ("Peter, Sam") into full names
// and emails from the sponsor's registrants, one email per name in the same
// order ("no email" keeps them lined up).
func scannerNamer(sp *Sponsor, domain string, guests []Guest) func(by string) (names, emails string) {
	raters := map[string]Rater{}
	return func(by string) (string, string) {
		var ns, es []string
		for _, n := range strings.Split(by, ",") {
			if n = strings.TrimSpace(n); n == "" {
				continue
			}
			r, ok := raters[n]
			if !ok {
				r = FindRater(n, sp.Name, domain, guests)
				raters[n] = r
			}
			if r.Name != "" {
				n = r.Name
			}
			e := r.Email
			if e == "" {
				e = "no email"
			}
			ns, es = append(ns, n), append(es, e)
		}
		return strings.Join(ns, ", "), strings.Join(es, ", ")
	}
}

// LeadNotes is what a sponsor's team said about one registrant, for the
// full leads list.
type LeadNotes struct{ Rating, Note, By string }

// TeamNotes is a sponsor's ratings and notes by Luma guest ID.
func TeamNotes(sp *Sponsor, leads []Lead, ix *GuestIndex, domain string) map[string]LeadNotes {
	scanners := scannerNamer(sp, domain, ix.Guests())
	out := map[string]LeadNotes{}
	for _, l := range leads {
		g, ok := lookupFor(ix, l.Key, sp.EventName)
		if !ok {
			continue
		}
		rating, note := teamRatings(l.Takes), notesLines(l.Takes)
		if len(l.Takes) == 0 {
			rating, note = ratingLabel(l.Rating), l.Note
		}
		by, _ := scanners(l.ScannedBy)
		out[g.ID] = LeadNotes{rating, note, by}
	}
	return out
}

// WriteCSV writes a sponsor's leads, oldest conversation first. The
// scanners' names and emails come from the sponsor's registrants (domain
// is the sponsor's website domain, or ""): "Peter" is Peter Marshall when
// he's the only registered Peter from the sponsor. With full set it adds
// the lead's email and phone and one column per other registration
// question.
func WriteCSV(w io.Writer, sp *Sponsor, leads []Lead, ix *GuestIndex, domain string, full bool) error {
	loc, err := time.LoadLocation(sp.EventTZ)
	if err != nil || sp.EventTZ == "" {
		loc = time.UTC
	}
	// UTF-8 BOM so Excel opens names with accents correctly.
	if _, err := io.WriteString(w, "\ufeff"); err != nil {
		return err
	}
	// Extra question columns, in the order they first appear.
	var labels []string
	if full {
		seen := make(map[string]bool)
		for _, l := range leads {
			if g, ok := lookupFor(ix, l.Key, sp.EventName); ok {
				for _, a := range g.ExtraAnswers() {
					if !seen[a.Label] {
						seen[a.Label] = true
						labels = append(labels, a.Label)
					}
				}
			}
		}
	}
	header := csvHeader
	if full {
		header = append(append(append([]string{}, csvHeader...), "Lead Email", "Lead Phone"), labels...)
	}
	scanners := scannerNamer(sp, domain, ix.Guests())

	cw := csv.NewWriter(w)
	if err := cw.Write(header); err != nil {
		return err
	}
	for _, l := range leads {
		name, email, title, company, linkedin, phone := "", "", "", "", "", ""
		extra := make(map[string]string)
		if g, ok := lookupFor(ix, l.Key, sp.EventName); ok {
			name = fullName(*g)
			email, title, company, linkedin, phone = g.Email, g.Title, g.Company, g.LinkedIn, g.Phone
			for _, a := range g.ExtraAnswers() {
				extra[a.Label] = a.Value
			}
		} else {
			switch l.Key.Kind {
			case "linkedin":
				linkedin = "https://www.linkedin.com/in/" + l.Key.Value
			case "email":
				email = l.Key.Value
			}
		}
		// Rating and Note say who: "Mark: hot; David: cold", one note per line.
		rating, note := teamRatings(l.Takes), notesLines(l.Takes)
		if len(l.Takes) == 0 {
			rating, note = ratingLabel(l.Rating), l.Note
		}
		byName, byEmail := scanners(l.ScannedBy)
		row := []string{sp.EventName, l.ScannedAt.In(loc).Format("2006-01-02 15:04:05"), name, title, company, linkedin, rating, note, byName, byEmail}
		if full {
			row = append(row, email, phone)
		}
		for _, label := range labels {
			row = append(row, extra[label])
		}
		for i := range row {
			row[i] = csvSafe(row[i])
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// WriteSponsorReadyCSV writes the registration list sponsors get: one row
// per approved guest, sorted by last name, LinkedIn as
// https://linkedin.com/in/<slug>. With att (after the event) it adds the
// Check-in column. It returns how many rows it wrote.
//
// With notes (a sponsor's own download) it adds their team's Rating, Note
// and Scanned by for the people they scanned.
func WriteSponsorReadyCSV(w io.Writer, guests []Guest, att *Attendance, notes map[string]LeadNotes) (int, error) {
	type row struct {
		first, last, email, title, company, linkedin, checkIn string
		notes                                                 LeadNotes
	}
	rows := make([]row, 0, len(guests))
	for _, g := range guests {
		checkIn := ""
		if att != nil {
			checkIn = att.CheckIn(g)
		}
		first, last := g.FirstName, g.LastName
		if first == "" && last == "" {
			first, last, _ = strings.Cut(g.Name, " ")
		}
		rows = append(rows, row{
			first:    strings.TrimSpace(first),
			last:     strings.TrimSpace(last),
			email:    g.Email,
			title:    g.Title,
			company:  g.Company,
			linkedin: strings.Replace(g.LinkedIn, "https://www.linkedin.com/", "https://linkedin.com/", 1),
			checkIn:  checkIn,
			notes:    notes[g.ID],
		})
	}
	// Sorted the way people (and Excel) file names: Ásgeirsdóttir with the As.
	col := collate.New(language.Und, collate.Loose)
	sort.SliceStable(rows, func(i, j int) bool {
		if c := col.CompareString(rows[i].last, rows[j].last); c != 0 {
			return c < 0
		}
		return col.CompareString(rows[i].first, rows[j].first) < 0
	})
	// UTF-8 BOM so Excel opens names with accents correctly.
	if _, err := io.WriteString(w, "\ufeff"); err != nil {
		return 0, err
	}
	cw := csv.NewWriter(w)
	header := []string{"First name", "Last name", "Email", "Job title", "Company", "LinkedIn"}
	if att != nil {
		header = append(header, "Check-in")
	}
	if notes != nil {
		header = append(header, "Rating", "Note", "Scanned by")
	}
	if err := cw.Write(header); err != nil {
		return 0, err
	}
	for _, r := range rows {
		rec := []string{r.first, r.last, r.email, r.title, r.company, r.linkedin}
		if att != nil {
			rec = append(rec, r.checkIn)
		}
		if notes != nil {
			rec = append(rec, r.notes.Rating, r.notes.Note, r.notes.By)
		}
		for i := range rec {
			rec[i] = csvSafe(rec[i])
		}
		if err := cw.Write(rec); err != nil {
			return 0, err
		}
	}
	cw.Flush()
	return len(rows), cw.Error()
}

// ratingLabel is how a rating reads in the spreadsheet: hot, not sure, cold.
func ratingLabel(r string) string {
	if r == "unsure" {
		return "not sure"
	}
	return r
}

// csvSafe stops spreadsheet apps from running registration answers as formulas.
func csvSafe(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}
