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
	return json.Unmarshal(raw, &s.secrets)
}

// EventSecret is the event's secret, made (once, for good) if it has none.
func (s *SniffStore) EventSecret(eventID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.secrets[eventID]; ok {
		return c, nil
	}
	var sec string
	for {
		b := make([]byte, 10)
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
		// Digits keep it from ever reading as a first-name link (/sniff/grace).
		if digits >= 2 && !s.secretTaken(sec) {
			break
		}
	}
	s.secrets[eventID] = sec
	data, err := json.MarshalIndent(s.secrets, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return "", err
	}
	tmp := filepath.Join(s.dir, "sniff_secrets.json.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", err
	}
	return sec, os.Rename(tmp, filepath.Join(s.dir, "sniff_secrets.json"))
}

// secretTaken: the caller holds s.mu.
func (s *SniffStore) secretTaken(sec string) bool {
	for _, c := range s.secrets {
		if c == sec {
			return true
		}
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

// bySecret runs an organizers' action for the event whose secret is in the
// link (/sniff/<secret>/...): anyone with the link may, like the page.
func (s *Server) bySecret(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		eventID, ok := s.sniff.EventBySecret(r.PathValue("secret"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		r.SetPathValue("event", eventID)
		next(w, r)
	}
}

// eventOrSpeaker serves /sniff/<x>/qr.png and slide.png: the event's when
// x is an event secret, else the speaker's.
func (s *Server) eventOrSpeaker(event, speaker http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if eventID, ok := s.sniff.EventBySecret(r.PathValue("token")); ok {
			r.SetPathValue("event", eventID)
			event(w, r)
			return
		}
		speaker(w, r)
	}
}
