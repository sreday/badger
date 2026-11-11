package sniff

import (
	"crypto/rand"
	"encoding/json"
	"log"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// --- Nice links ----------------------------------------------------------------
//
// Every event gets a secret: its page is /sniff/<secret> (organizers only)
// and each speaker's page is /sniff/<secret>/<name>, e.g.
// /sniff/k7mq4x2pw9/grace. The secret keeps the speaker pages
// unguessable; the name makes the link readable. Secrets are kept in
// sniff_secrets.json (event id -> secret) and never change. The older
// /sniff/<token> and /sniff/<first name> links keep working.

// secretAlphabet: lowercase, no look-alikes (no i, l, o, 0, 1).
const secretAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// loadSecrets reads sniff_secrets.json. The caller holds s.mu (or owns s).
func (s *SniffStore) loadSecrets() error {
	raw, err := os.ReadFile(filepath.Join(s.dir, "sniff_secrets.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &s.secrets); err != nil {
		return err
	}
	if raw, err := os.ReadFile(filepath.Join(s.dir, "sniff_secret_aliases.json")); err == nil {
		return json.Unmarshal(raw, &s.aliases)
	}
	return nil
}

// EventSecret is the event's link secret, made (once) if it has none:
// readable, then a random tail, e.g. sreday-london-2026-q4-k7mq4x
// (eventSlug). An event whose name isn't known yet gets a plain random one
// for now; once the name is known (NoteEventName) it gets the readable one,
// and the old secret keeps working as a redirect (sniff_secret_aliases.json).
func (s *SniffStore) EventSecret(eventID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := s.eventNameLocked(eventID)
	if c, ok := s.secrets[eventID]; ok && (strings.Contains(c, "-") || name == "") {
		return c, nil
	}
	base := ""
	if name != "" {
		base = eventSlug(name)
	}
	var sec string
	for {
		n := 10
		if base != "" {
			n = 6
		}
		b := make([]byte, n)
		digits := 0
		for i := range b {
			k, err := rand.Int(rand.Reader, big.NewInt(int64(len(secretAlphabet))))
			if err != nil {
				return "", err
			}
			b[i] = secretAlphabet[k.Int64()]
			if b[i] >= '2' && b[i] <= '9' {
				digits++
			}
		}
		sec = string(b)
		if base != "" {
			sec = base + "-" + sec
		}
		// Digits keep it from ever reading as a first-name link (/sniff/grace).
		if digits >= 2 && !s.secretTaken(sec) {
			break
		}
	}
	if old, ok := s.secrets[eventID]; ok {
		s.aliases[old] = eventID
		if err := s.writeJSON("sniff_secret_aliases.json", s.aliases); err != nil {
			return "", err
		}
	}
	s.secrets[eventID] = sec
	return sec, s.writeJSON("sniff_secrets.json", s.secrets)
}

// writeJSON writes one of the store's small JSON files. The caller holds s.mu.
func (s *SniffStore) writeJSON(name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(s.dir, name+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.dir, name))
}

// secretTaken: the caller holds s.mu.
func (s *SniffStore) secretTaken(sec string) bool {
	for _, c := range s.secrets {
		if c == sec {
			return true
		}
	}
	if _, ok := s.aliases[sec]; ok {
		return true
	}
	for _, ss := range s.sessions {
		if ss.Slug == sec || ss.Token == sec {
			return true
		}
	}
	return false
}

// HasEventSecret is the event's secret without making one.
func (s *SniffStore) HasEventSecret(eventID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.secrets[eventID]
	return c, ok
}

// EventBySecret finds the event a secret belongs to.
func (s *SniffStore) EventBySecret(sec string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sec = strings.ToLower(strings.TrimSpace(sec))
	if sec == "" {
		return "", false
	}
	for id, c := range s.secrets {
		if c == sec {
			return id, true
		}
	}
	return "", false
}

// nameSlug is "grace-hopper" for "Grace Hopper": every word, accents
// folded. Used for sessions without a speaker (their title).
func nameSlug(who string) string {
	return strings.Join(strings.Fields(foldName(who)), "-")
}

// SpeakerNames are the speaker-page names of the event's sessions: code ->
// name, the speaker's first name (/sniff/<secret>/grace; co-speakers: the
// first one's). Names are handed out in the order the sessions were made,
// so they never move. Another Grace at the same event gets grace-hopper
// (then a number); the same Grace Hopper twice (a talk and a workshop) gets
// grace-2. A session without a speaker gets its title.
func (s *SniffStore) SpeakerNames(eventID string) map[string]string {
	s.mu.Lock()
	var list []*SniffSession
	for _, ss := range s.sessions {
		if ss.EventID == eventID {
			list = append(list, ss)
		}
	}
	s.mu.Unlock()
	sort.SliceStable(list, func(i, j int) bool {
		if !list[i].CreatedAt.Equal(list[j].CreatedAt) {
			return list[i].CreatedAt.Before(list[j].CreatedAt)
		}
		return list[i].Code < list[j].Code
	})
	out := make(map[string]string, len(list))
	holder := map[string]string{} // name -> the folded speaker field that has it
	for _, ss := range list {
		who := foldName(ss.Who)
		cands := slugsFor(ss.Who)
		if len(cands) > 0 && holder[cands[0]] != "" && holder[cands[0]] == who {
			// The same person again: grace-2, grace-3.
			first := cands[0]
			cands = nil
			for n := 2; n < 100; n++ {
				cands = append(cands, first+"-"+strconv.Itoa(n))
			}
		}
		if len(cands) == 0 { // no speaker, or no Latin letters in the name
			base := nameSlug(ss.Title)
			if base == "" {
				base = strings.ToLower(ss.Code)
			}
			cands = []string{base}
			for n := 2; n < 100; n++ {
				cands = append(cands, base+"-"+strconv.Itoa(n))
			}
		}
		for _, c := range cands {
			if _, taken := holder[c]; !taken {
				holder[c] = who
				out[ss.Code] = c
				break
			}
		}
		if out[ss.Code] == "" {
			out[ss.Code] = strings.ToLower(ss.Code)
		}
	}
	return out
}

// BySecretName finds the session /sniff/<secret>/<name> opens.
func (s *SniffStore) BySecretName(sec, name string) (SniffSession, bool) {
	eventID, ok := s.EventBySecret(sec)
	if !ok {
		return SniffSession{}, false
	}
	name = strings.ToLower(strings.TrimSpace(name))
	for code, n := range s.SpeakerNames(eventID) {
		if n == name {
			return s.Session(code)
		}
	}
	return SniffSession{}, false
}

// handleSniffAdminOld sends the old /admin/sniff/<event> to the event's
// page, /sniff/<secret>.
func (s *Server) handleSniffAdminOld(w http.ResponseWriter, r *http.Request) {
	to := s.eventPagePath(r.PathValue("event"))
	if r.URL.RawQuery != "" {
		to += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// eventPagePath is the organizers' page for the event: /sniff/<secret>,
// made on first use.
func (s *Server) eventPagePath(eventID string) string {
	sec, err := s.sniff.EventSecret(eventID)
	if err != nil {
		log.Printf("Sniff: no secret for %s: %v", eventID, err)
		return "/sniff"
	}
	return "/sniff/" + sec
}

// byName serves a talk's QR or slide by its name link
// (/sniff/<secret>/<name>/qr.png): no PIN, they carry no data.
func (s *Server) byName(next func(http.ResponseWriter, *http.Request, SniffSession)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ss, ok := s.sniff.BySecretName(r.PathValue("secret"), r.PathValue("name"))
		if !ok {
			if s.redirectOld(w, r, r.PathValue("secret")) {
				return
			}
			http.NotFound(w, r)
			return
		}
		next(w, r, ss)
	}
}

// byToken serves the same by the older token link, once the PIN is in.
func (s *Server) byToken(next func(http.ResponseWriter, *http.Request, SniffSession)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if ss, ok := s.sniffByToken(w, r); ok {
			next(w, r, ss)
		}
	}
}
