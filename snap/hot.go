package snap

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Hot (Marek 2026-11-13): "the main purpose (snappy replies to hot threads)
// is not there yet... we need to be able to see the HOT messages within
// minutes of receiving them". Old loose ends and snoozes stay on the relaxed
// cadence; fresh mail gets a fast lane:
//
//   - Snout looks at the last two days of the Primary tab every minute
//     (syncRecent), on top of the full read every 15 minutes.
//   - Fresh mail from a person is ripe for Claude after two minutes, not a
//     day (Thread.Ripe), and goes first in the queue.
//   - When such a thread waits in the queue, Snout starts the reading
//     routine itself through its API trigger (fireIfHot), at most once every
//     five minutes. The hourly run stays as the safety net.
//   - The Hot tab shows at most five threads: someone wrote in the last 7
//     days, the ball is with Mark, and Claude says it needs him. The rest
//     wait in their own tab (marked hot) until there's room on the plate.

const (
	hotWindow    = 7 * 24 * time.Hour
	hotSettle    = 2 * time.Minute
	hotPlateSize = 5
	topBlockSize = 5
	topBlockAge  = 30 * 24 * time.Hour
	fireGap      = 5 * time.Minute // between two starts of the reading routine
	fetchGrace   = 3 * time.Minute // the routine fetched the queue this recently: it's at work

	recentQuery = "newer_than:2d category:primary -in:chats -in:spam -in:trash -in:drafts"
	hotMarker   = "SNAP HOT MAIL"
)

// FreshInbound: someone other than Mark wrote last, within the hot window.
func (t *Thread) FreshInbound(now time.Time) bool {
	if t.Form == "speaker" {
		return false // a Fast Track or Waitlist form: relaxed, it goes to the Speakers tab (forms.go)
	}
	return t.Skip == "" && t.LastFrom != "" && t.LastFrom != "me" && !t.LastAt.IsZero() && now.Sub(t.LastAt) <= hotWindow
}

// Hot: a fresh loose end with the ball in Mark's court.
func (t *Thread) Hot(now time.Time) bool {
	return t.ByClaude() && t.Open(now) && t.Waiting == "you" && t.FreshInbound(now)
}

func priorityRank(r Row) int {
	if r.StillListed != "" || r.State == "due" {
		return 0 // a cancelled speaker still on the site, a follow-up that's due
	}
	switch r.Priority {
	case "high":
		return 0
	case "low":
		return 2
	}
	return 1
}

func freshOf(r Row) time.Time {
	if r.Fresh.IsZero() {
		return r.LastAt
	}
	return r.Fresh
}

// byStakes puts the ball in Mark's park first (Marek 2026-11-13: "always
// show ball in your park as prioritary"), then sorts high, medium, low; the
// freshest first within each.
func byStakes(rows []Row) {
	mine := func(r Row) int {
		if r.Waiting == "you" {
			return 0
		}
		return 1
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if a, b := mine(rows[i]), mine(rows[j]); a != b {
			return a < b
		}
		if a, b := priorityRank(rows[i]), priorityRank(rows[j]); a != b {
			return a < b
		}
		return freshOf(rows[i]).After(freshOf(rows[j]))
	})
}

// hotPlate splits the hot rows into the five on the plate and the ones
// waiting behind them.
func hotPlate(hot []Row) (plate, waiting []Row) {
	byStakes(hot)
	if len(hot) > hotPlateSize {
		return hot[:hotPlateSize], hot[hotPlateSize:]
	}
	return hot, nil
}

// orderTab orders a tab's active cards (Marek 2026-11-13). The ball in
// Mark's park always comes first: all the threads waiting on him, then the
// rest. Within each of the two: the top five of the last 30 days (highest
// stakes, freshest), then everything else high, medium, low, freshest to
// oldest. One divider: between his and the rest, or, when the tab holds
// only one kind, after the top block.
func orderTab(rows []Row, now time.Time) []Row {
	var mine, others []Row
	for _, r := range rows {
		r.Sep = false
		if r.Waiting == "you" {
			mine = append(mine, r)
		} else {
			others = append(others, r)
		}
	}
	mine, mineCut := topThenRest(mine, now)
	others, othersCut := topThenRest(others, now)
	switch {
	case len(mine) > 0 && len(others) > 0:
		others[0].Sep = true
	case mineCut > 0 && mineCut < len(mine):
		mine[mineCut].Sep = true
	case othersCut > 0 && othersCut < len(others):
		others[othersCut].Sep = true
	}
	return append(mine, others...)
}

// topThenRest: the top block first, then the rest by stakes; cut is where
// the rest begins.
func topThenRest(rows []Row, now time.Time) (out []Row, cut int) {
	byStakes(rows)
	var top, rest []Row
	for _, r := range rows {
		recent := now.Sub(freshOf(r)) <= topBlockAge || r.StillListed != "" || r.State == "due"
		if recent && len(top) < topBlockSize {
			top = append(top, r)
		} else {
			rest = append(rest, r)
		}
	}
	return append(top, rest...), len(top)
}

// --- The fast lane -----------------------------------------------------------

// syncRecent is the minute's look at the newest mail: only threads that
// moved are read, nothing is pruned, and no run is logged.
func (s *Server) syncRecent(ctx context.Context) {
	if !s.syncMu.TryLock() {
		return // the full read is at it
	}
	defer s.syncMu.Unlock()
	if email, _ := s.google.Connected(); email == "" {
		return
	}
	now := s.now()
	refs, err := s.gmail.List(recentQuery)
	if err != nil {
		log.Printf("Snap: fast lane: %v", err)
		return
	}
	doms := s.sponsorDomains(now)
	changed, replied, err := s.readChanged(ctx, refs, now, doms)
	if err != nil {
		log.Printf("Snap: fast lane: %v", err)
	}
	if changed > 0 {
		for _, id := range replied {
			s.updateTopic(id, reopen)
		}
		s.markRelated()
		if err := s.fillQueue(now, doms); err != nil {
			log.Printf("Snap: fast lane: %v", err)
		}
	}
	s.fireIfHot(now)
}

// hotQueued counts the fresh threads waiting for Claude right now.
func (s *Server) hotQueued(now time.Time) int {
	all := s.store.All()
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, t := range all {
		if c, ok := s.excerpts[t.ID]; ok && c.history == t.HistoryID && t.Queued(now) && t.FreshInbound(now) {
			n++
		}
	}
	return n
}

// readConfig is the reading routine's API trigger: from the env, or pasted
// once into the Claude menu (data/snap/read.json).
type readConfig struct {
	Routine string `json:"routine"`
	Token   string `json:"token"`
}

const readRoutineDefault = "trig_013ao9x811n225gTGP8KSw5M" // "Snap", the reading routine

func (s *Server) readCfg() readConfig {
	c := readConfig{Routine: os.Getenv("SNAP_READ_ROUTINE"), Token: os.Getenv("SNAP_READ_TOKEN")}
	var f readConfig
	if data, err := os.ReadFile(filepath.Join(s.dir, "read.json")); err == nil {
		json.Unmarshal(data, &f)
	}
	if f.Routine != "" {
		c.Routine = f.Routine
	}
	if f.Token != "" {
		c.Token = f.Token
	}
	if c.Routine == "" {
		c.Routine = readRoutineDefault
	}
	return c
}

// CanFire: Snout can start the reading routine itself.
func (s *Server) CanFire() bool { return s.readCfg().Token != "" }

// handleReadToken: POST /admin/read-token stores the reading routine's API token.
func (s *Server) handleReadToken(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimSpace(r.FormValue("token"))
	if len(tok) < 20 || strings.ContainsAny(tok, " \n\t") {
		http.Error(w, "That doesn't look like a routine token", http.StatusBadRequest)
		return
	}
	c := s.readCfg()
	c.Token = tok
	if rt := strings.TrimSpace(r.FormValue("routine")); strings.HasPrefix(rt, "trig_") {
		c.Routine = rt
	}
	writeJSON(s.dir, "read.json", c)
	s.mu.Lock()
	s.lastFire = time.Time{}
	s.mu.Unlock()
	back(w, r)
}

// fireIfHot starts the reading routine when fresh mail waits for it.
func (s *Server) fireIfHot(now time.Time) {
	n := s.hotQueued(now)
	if n == 0 {
		return
	}
	c := s.readCfg()
	if c.Token == "" {
		return // the hourly run reads it
	}
	s.mu.Lock()
	busy := now.Sub(s.lastFire) < fireGap || now.Sub(s.claude.LastFetch) < fetchGrace
	if !busy {
		s.lastFire = now
	}
	s.mu.Unlock()
	if busy {
		return
	}
	text := fmt.Sprintf("%s\n\n%d fresh thread(s) just arrived in Mark's inbox and wait in Snap's queue. Read the queue now and answer it, exactly as your prompt says for a scheduled run.", hotMarker, n)
	err := s.fireTrigger(c.Routine, c.Token, text)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		log.Printf("Snap: fast lane: starting Claude: %v", err)
		s.claude.LastProblem = "Couldn't start Claude for fresh mail: " + err.Error()
		if err == errBadToken {
			s.claude.LastProblem = "The reading routine's token is wrong or was regenerated: paste the new one below."
		}
	} else {
		s.claude.Fires++
		s.claude.LastFire = now
	}
	s.saveClaudeLocked()
}
