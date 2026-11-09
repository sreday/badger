// Package event holds what Snout and Sniff work out from a Luma event's
// name and times: its conference and brand colour, and its phase (test,
// live, closed).
package event

import (
	"strings"
	"time"
)

// An event goes through these phases, all from the Luma event times: test
// until 6h before the start, live until 6h after the end, then closed.
const (
	PhaseTest   = "test"
	PhaseLive   = "live"
	PhaseClosed = "closed"

	LiveBefore = 6 * time.Hour  // live this long before the start
	CloseAfter = 6 * time.Hour  // closes this long after the end
	LateSync   = 24 * time.Hour // offline scans made before closing are accepted this long after it
)

// Window is an event's start and end, as Luma has them.
type Window struct {
	Start, End time.Time
}

// Bounds returns when the event goes live and closes. Without a start
// (links made before times were stored) both are zero: always live.
func (w Window) Bounds() (liveFrom, closesAt time.Time) {
	if w.Start.IsZero() {
		return time.Time{}, time.Time{}
	}
	end := w.End
	if end.IsZero() {
		end = w.Start.Add(12 * time.Hour)
	}
	return w.Start.Add(-LiveBefore), end.Add(CloseAfter)
}

// PhaseAt returns the phase at t.
func (w Window) PhaseAt(t time.Time) string {
	liveFrom, closesAt := w.Bounds()
	switch {
	case liveFrom.IsZero():
		return PhaseLive
	case t.Before(liveFrom):
		return PhaseTest
	case t.Before(closesAt):
		return PhaseLive
	default:
		return PhaseClosed
	}
}

// Label returns the phase at now and when it ends, formatted for people in
// the event's time zone.
func Label(w Window, tz string, now time.Time) (phase, until string) {
	phase = w.PhaseAt(now)
	liveFrom, closesAt := w.Bounds()
	var end time.Time
	switch phase {
	case PhaseTest:
		end = liveFrom
	case PhaseLive:
		end = closesAt
	}
	if end.IsZero() {
		return phase, ""
	}
	return phase, LocalTime(end, tz)
}

// LocalTime formats t in the event's time zone, e.g. "2 Nov 22:00".
func LocalTime(t time.Time, tz string) string {
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		loc = time.UTC
	}
	return t.In(loc).Format("2 Jan 15:04")
}

// Brand picks the scanner's tint from the event name: purple SREday
// (the default), green LLMday (and LLMday + PEC London), orange PLATFORMday.
func Brand(eventName string) string {
	n := strings.ToLower(eventName)
	switch {
	case strings.Contains(n, "platformday"):
		return "platformday"
	case strings.Contains(n, "llmday") || strings.Contains(n, "prompt engineering"):
		return "llmday"
	default:
		return "sreday"
	}
}

// Conference is the conference an event belongs to, by its Luma name, and
// the slug of its test badge picture (snout/assets/badges/test-badge-<slug>.jpg).
func Conference(eventName string) (name, slug string) {
	n := strings.ToLower(eventName)
	switch {
	case strings.Contains(n, "platformday"):
		return "PLATFORMday", "platformday"
	case strings.Contains(n, "llmday"):
		return "LLMday", "llmday"
	case strings.Contains(n, "prompt engineering") || strings.Contains(" "+n+" ", " pec "):
		return "PEC", "pec"
	case strings.Contains(n, "devops not dead") || strings.Contains(n, "devopsnotdead"):
		return "DevOps Not Dead", "devopsnotdead"
	default:
		return "SREday", "sreday"
	}
}
