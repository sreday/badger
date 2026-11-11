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
// It says which strike this is (1, 2, 3) and how long the block is (0: no
// block yet).
func (s *strikes) strike(key string, now time.Time) (int, time.Duration) {
	if key == "" {
		return 0, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.count) > 20000 { // a ceiling, not a store
		s.count = map[string]int{}
	}
	s.count[key]++
	n := s.count[key]
	if n >= strikesToBlock {
		delete(s.count, key)
		s.until[key] = now.Add(strikeBlock)
		return n, strikeBlock
	}
	return n, 0
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
	json.NewEncoder(w).Encode(map[string]any{"ok": status < 300, "filtered": filtered, "error": blockedMsg(d), "blocked": int(d.Seconds()), "strike": strikesToBlock, "of": strikesToBlock})
}

// Three questions per person per talk: counted per phone (its random key)
// and session, memory only. A filtered question doesn't count (it never
// reaches the speaker); deleting one gives the slot back.
const maxQuestionsPerTalk = 3

type quota struct {
	mu sync.Mutex
	n  map[string]int
}

func newQuota() *quota { return &quota{n: map[string]int{}} }

func (q *quota) used(code, phone string) int {
	if phone == "" {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.n[code+"|"+phone]
}

func (q *quota) add(code, phone string, d int) {
	if phone == "" {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.n) > 50000 { // a ceiling, not a store
		q.n = map[string]int{}
	}
	k := code + "|" + phone
	if q.n[k] += d; q.n[k] <= 0 {
		delete(q.n, k)
	}
}
