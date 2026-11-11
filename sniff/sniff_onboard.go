package sniff

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/sreday/badger/event"
	"github.com/sreday/badger/luma"
)

// --- Readable event links + the onboarding email ------------------------------
//
// An event's Sniff page is /sniff/<brand>-<city>-<year>-<quarter>-<tail>,
// e.g. /sniff/sreday-london-2026-q4-k7mq4x: readable, and the random tail
// keeps it unguessable (the speakers' PIN is shared, so the link is half of
// what guards the attendee details). The speaker onboarding email (the
// "Speaker onboarding" Apps Script) asks GET /api/sniff/link for it, with
// the key from /sniff, so the link never sits in a public site repo.

var quarterName = regexp.MustCompile(`^(.*?)[\s,-]+(\d{4})\s+(q[1-4])$`)

// brandWords are dropped from the city part of a name.
var brandWords = regexp.MustCompile(`\b(sreday|llmday|platformday|devops not dead|devopsnotdead|prompt engineering conference|prompt engineering|pec)\b`)

// eventSlug is the readable part of an event's link: "sreday-nyc-2026-q4"
// for "SREday NYC 2026 Q4", "llmday-london-2026-q4" for "LLMday + Prompt
// Engineering Conference - London 2026 Q4". A name without a year and
// quarter is slugged whole (at most 40 letters).
func eventSlug(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	_, brand := event.Conference(name)
	if m := quarterName.FindStringSubmatch(n); m != nil {
		head := m[1]
		if i := strings.LastIndex(head, " - "); i >= 0 {
			head = head[i+3:]
		}
		city := strings.Join(strings.Fields(foldName(brandWords.ReplaceAllString(head, " "))), "-")
		parts := []string{brand}
		if city != "" {
			parts = append(parts, city)
		}
		return strings.Join(append(parts, m[2], m[3]), "-")
	}
	s := strings.Join(strings.Fields(foldName(n)), "-")
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	if s == "" {
		s = brand
	}
	return s
}

// NoteEventName remembers an event's Luma name (for its readable link).
func (s *SniffStore) NoteEventName(eventID, name string) {
	if eventID == "" || strings.TrimSpace(name) == "" {
		return
	}
	s.mu.Lock()
	s.names[eventID] = name
	s.mu.Unlock()
}

// eventNameLocked is the event's name: as noted, else from its sessions.
// The caller holds s.mu.
func (s *SniffStore) eventNameLocked(eventID string) string {
	if n := s.names[eventID]; n != "" {
		return n
	}
	for _, ss := range s.sessions {
		if ss.EventID == eventID && ss.EventName != "" {
			return ss.EventName
		}
	}
	return ""
}

// OldSecret is the event an older secret belonged to, and its current one
// (old links redirect).
func (s *SniffStore) OldSecret(sec string) (current string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	eventID, ok := s.aliases[strings.ToLower(strings.TrimSpace(sec))]
	if !ok {
		return "", false
	}
	current, ok = s.secrets[eventID]
	return current, ok
}

// redirectOld sends a link with an older event secret to the same page
// under the current one. It says whether it did.
func (s *Server) redirectOld(w http.ResponseWriter, r *http.Request, sec string) bool {
	cur, ok := s.sniff.OldSecret(sec)
	if !ok {
		return false
	}
	to := strings.Replace(r.URL.Path, "/sniff/"+sec, "/sniff/"+cur, 1)
	if r.URL.RawQuery != "" {
		to += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, to, http.StatusPermanentRedirect)
	return true
}

// LinkKey is the key the onboarding script sends (made once, kept in
// sniff_link_key). Shown to the team on /sniff.
func (s *SniffStore) LinkKey() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, "sniff_link_key")
	if raw, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(raw))) >= 32 {
		return strings.TrimSpace(string(raw)), nil
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	k := hex.EncodeToString(b)
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return "", err
	}
	return k, os.WriteFile(path, []byte(k+"\n"), 0o600)
}

var folderSlug = regexp.MustCompile(`^(\d{4})-(.+)-(q[1-4])$`)

// pickEvent finds the Luma event of a site's event folder ("2026-london-q4")
// and brand ("sreday", "llmday", "platformday", "pec", ...): the one whose
// readable link ends in london-2026-q4, the brand's own first when there
// are several (LLMday + PEC London is one Luma event for both sites).
func pickEvent(events []luma.Event, brand, folder string) (luma.Event, bool) {
	m := folderSlug.FindStringSubmatch(strings.ToLower(strings.TrimSpace(folder)))
	if m == nil {
		return luma.Event{}, false
	}
	tail := "-" + m[2] + "-" + m[1] + "-" + m[3]
	brand = strings.ToLower(strings.TrimSpace(brand))
	var best []luma.Event
	bestScore := -1
	for _, ev := range events {
		slug := eventSlug(ev.Name)
		if !strings.HasSuffix(slug, tail) {
			continue
		}
		_, conf := event.Conference(ev.Name)
		score := 0
		if conf == brand || strings.HasPrefix(slug, brand+"-") {
			score = 2
		} else if brand == "pec" && strings.Contains(strings.ToLower(ev.Name), "prompt engineering") {
			score = 2
		}
		switch {
		case score > bestScore:
			best, bestScore = []luma.Event{ev}, score
		case score == bestScore:
			best = append(best, ev)
		}
	}
	if len(best) != 1 {
		return luma.Event{}, false
	}
	return best[0], true
}

// handleSniffLinkAPI is GET /api/sniff/link?brand=sreday&event=2026-london-q4
// with the key (X-Sniff-Key header or key=): the event's Sniff page and the
// speakers' PIN, for the onboarding email.
func (s *Server) handleSniffLinkAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	reply := func(status int, v map[string]any) {
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(v)
	}
	key, err := s.sniff.LinkKey()
	got := r.Header.Get("X-Sniff-Key")
	if got == "" {
		got = r.URL.Query().Get("key")
	}
	if err != nil || got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(key)) != 1 {
		reply(http.StatusUnauthorized, map[string]any{"ok": false, "error": "wrong key"})
		return
	}
	events, err := s.onboardEvents()
	if err != nil {
		reply(http.StatusBadGateway, map[string]any{"ok": false, "error": "Luma: " + err.Error()})
		return
	}
	ev, ok := pickEvent(events, r.URL.Query().Get("brand"), r.URL.Query().Get("event"))
	if !ok {
		reply(http.StatusNotFound, map[string]any{"ok": false, "error": "no single Luma event for that brand and folder"})
		return
	}
	s.sniff.NoteEventName(ev.APIId, ev.Name)
	sec, err := s.sniff.EventSecret(ev.APIId)
	if err != nil {
		reply(http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	reply(http.StatusOK, map[string]any{"ok": true, "url": s.baseURL(r) + "/sniff/" + sec, "pin": sniffPIN, "event": ev.Name})
}

// The onboarding page looks up one event per preview and send; Luma's
// calendar list is read at most every 5 minutes for them (Luma rate-limits
// quick repeats). A failed read falls back to the last good one.
var obCache struct {
	sync.Mutex
	at     time.Time
	events []luma.Event
}

func (s *Server) onboardEvents() ([]luma.Event, error) {
	obCache.Lock()
	defer obCache.Unlock()
	if obCache.events != nil && time.Since(obCache.at) < 5*time.Minute {
		return obCache.events, nil
	}
	events, err := luma.ListEvents(s.cfg.LumaAPIKey)
	if err != nil {
		if obCache.events != nil {
			return obCache.events, nil
		}
		return nil, err
	}
	obCache.at, obCache.events = time.Now(), events
	return events, nil
}
