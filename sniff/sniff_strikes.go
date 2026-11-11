package sniff

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Three filtered questions in a row from one phone, and that phone can't
// ask for an hour (not in test mode: speakers try the filter out). The
// count follows the phone's own random key, never the IP (a venue shares
// one); a question that gets through starts it again. Memory only.
const (
	strikesToBlock = 3
	strikeBlock    = time.Hour
)

type strikes struct {
	mu    sync.Mutex
	count map[string]int
	until map[string]time.Time
}

func newStrikes() *strikes {
	return &strikes{count: map[string]int{}, until: map[string]time.Time{}}
}

// blocked is how long the phone still can't ask (0: it can).
func (s *strikes) blocked(key string, now time.Time) time.Duration {
	if key == "" {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if u, ok := s.until[key]; ok {
		if now.Before(u) {
			return u.Sub(now)
		}
		delete(s.until, key)
	}
	return 0
}

// strike counts a filtered question; the third in a row blocks the phone.
// It says how long (0: not blocked yet).
func (s *strikes) strike(key string, now time.Time) time.Duration {
	if key == "" {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.count) > 20000 { // a ceiling, not a store
		s.count = map[string]int{}
	}
	s.count[key]++
	if s.count[key] >= strikesToBlock {
		delete(s.count, key)
		s.until[key] = now.Add(strikeBlock)
		return strikeBlock
	}
	return 0
}

// clear starts the count again (a question that got through).
func (s *strikes) clear(key string) {
	if key == "" {
		return
	}
	s.mu.Lock()
	delete(s.count, key)
	s.mu.Unlock()
}

func blockedMsg(d time.Duration) string {
	m := int(d.Minutes()) + 1
	return fmt.Sprintf("Too many questions that didn't get through. You can ask again in %d minutes.", m)
}

// blockedJSON answers a phone that is (now) blocked: the message and the
// seconds left, for its countdown.
func blockedJSON(w http.ResponseWriter, status int, filtered bool, d time.Duration) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"ok": status < 300, "filtered": filtered, "error": blockedMsg(d), "blocked": int(d.Seconds())})
}
