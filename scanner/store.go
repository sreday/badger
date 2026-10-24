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
)

// Sponsor is one magic link + PIN for one event.
type Sponsor struct {
	Token     string    `json:"token"`
	PIN       string    `json:"pin"`
	Name      string    `json:"name"`
	EventID   string    `json:"event_id"`
	EventName string    `json:"event_name"`
	EventTZ   string    `json:"event_tz"`
	CreatedAt time.Time `json:"created_at"`
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
	Rating     string    `json:"rating"`
	Note       string    `json:"note"`
	ReceivedAt time.Time `json:"received_at"`
}

// record is one line of leads.jsonl: a new scan, or an update to one.
type record struct {
	Type string `json:"type"` // "scan" or "update"
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
}

func OpenStore(dir string) (*Store, error) {
	s := &Store{
		dir:      dir,
		sponsors: make(map[string]*Sponsor),
		leads:    make(map[string][]*Lead),
		byScanID: make(map[string]*Lead),
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
		s.byScanID[l.ScanID] = &l
		s.leads[l.Sponsor] = append(s.leads[l.Sponsor], &l)
	case "update":
		if l, ok := s.byScanID[rec.ScanID]; ok {
			l.Rating = rec.Rating
			l.Note = rec.Note
		}
	}
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
	tok := make([]byte, 16)
	if _, err := rand.Read(tok); err != nil {
		return nil, err
	}
	n, err := rand.Int(rand.Reader, big.NewInt(10000))
	if err != nil {
		return nil, err
	}
	sp := &Sponsor{
		Token:     hex.EncodeToString(tok),
		PIN:       fmt.Sprintf("%04d", n.Int64()),
		Name:      name,
		EventID:   ev.APIId,
		EventName: ev.Name,
		EventTZ:   ev.Timezone,
		CreatedAt: time.Now().UTC(),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sponsors[sp.Token] = sp
	if err := s.saveSponsors(); err != nil {
		delete(s.sponsors, sp.Token)
		return nil, err
	}
	return sp, nil
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
func (s *Store) AddScan(sp *Sponsor, l Lead) (lead Lead, duplicate bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.byScanID[l.ScanID]; ok {
		return *existing, false, nil
	}
	for _, existing := range s.leads[sp.Token] {
		if existing.Identity == l.Identity {
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

// UpdateLead sets a lead's rating and note.
func (s *Store) UpdateLead(sp *Sponsor, scanID, rating, note string) (Lead, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.byScanID[scanID]
	if !ok || l.Sponsor != sp.Token {
		return Lead{}, fmt.Errorf("unknown scan %q", scanID)
	}
	rec := record{Type: "update", Lead: Lead{ScanID: scanID, Sponsor: sp.Token, Rating: rating, Note: note}}
	if err := s.appendRecord(sp.EventID, rec); err != nil {
		return Lead{}, err
	}
	s.apply(rec)
	return *l, nil
}

// Leads returns a sponsor's leads ordered by when the conversation happened.
func (s *Store) Leads(token string) []Lead {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Lead, 0, len(s.leads[token]))
	for _, l := range s.leads[token] {
		out = append(out, *l)
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

var csvHeader = []string{"Scanned At", "First Name", "Last Name", "Email", "Job Title", "Company", "LinkedIn", "Rating", "Note", "Scanned By"}

// WriteCSV writes a sponsor's leads, oldest conversation first.
func WriteCSV(w io.Writer, sp *Sponsor, leads []Lead, ix *GuestIndex) error {
	loc, err := time.LoadLocation(sp.EventTZ)
	if err != nil || sp.EventTZ == "" {
		loc = time.UTC
	}
	// UTF-8 BOM so Excel opens names with accents correctly.
	if _, err := io.WriteString(w, "\ufeff"); err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	if err := cw.Write(csvHeader); err != nil {
		return err
	}
	for _, l := range leads {
		first, last, email, title, company, linkedin := "", "", "", "", "", ""
		if g, ok := ix.Lookup(l.Key); ok {
			first, last = g.FirstName, g.LastName
			if first == "" && last == "" {
				first, last, _ = strings.Cut(g.Name, " ")
			}
			email, title, company, linkedin = g.Email, g.Title, g.Company, g.LinkedIn
		} else {
			switch l.Key.Kind {
			case "linkedin":
				linkedin = "https://www.linkedin.com/in/" + l.Key.Value
			case "email":
				email = l.Key.Value
			}
		}
		row := []string{
			l.ScannedAt.In(loc).Format("2006-01-02 15:04:05"),
			first, last, email, title, company, linkedin,
			l.Rating, l.Note, l.ScannedBy,
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

// csvSafe stops spreadsheet apps from running registration answers as formulas.
func csvSafe(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}
