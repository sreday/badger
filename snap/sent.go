package snap

import (
	"strings"
	"time"
)

// Sent mail parks the card (Marek 2026-11-13: "park the Active to Snoozed
// if I just sent an answer in the thread, with claude or not"). When a sync
// sees Mark's own new message at the end of a flagged thread, the card is
// done for now: it moves to Snoozed for a week with the ball in their park,
// and so do the other emails of its topic. Their reply brings it straight
// back; if none comes, it returns to Active when the snooze ends and Claude
// reads it again for the nudge.

const sentSnooze = 7 * 24 * time.Hour

// parkSent snoozes an open loose end Mark has just answered.
func parkSent(t *Thread, at time.Time) {
	if !t.Flagged() {
		return
	}
	if until := at.Add(sentSnooze); until.After(t.SnoozeUntil) {
		t.SnoozeUntil = until
	}
	t.Waiting, t.Between, t.SentAt = "them", "", at
	if !strings.HasPrefix(t.Why, "You replied on ") {
		t.Why = "You replied on " + at.Format("2 Jan") + "; waiting on them. Before that: " + t.Why
	}
}
