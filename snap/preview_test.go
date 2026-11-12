package snap

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// SNAP_PREVIEW=<file> writes the page with sample threads, for a screenshot.
func TestPreview(t *testing.T) {
	out := os.Getenv("SNAP_PREVIEW")
	if out == "" {
		t.Skip("SNAP_PREVIEW not set")
	}
	now := time.Now()
	s, err := New(Config{Base: "/snap", DataDir: t.TempDir(), AdminPassword: "pw", Account: "mark@sreday.com", Every: time.Hour, TZ: "Europe/London", DraftRoutine: "trig_x", DraftToken: "x"})
	if err != nil {
		t.Fatal(err)
	}
	add := func(th Thread) { s.store.Update(th.ID, func(t *Thread) { *t = th }) }
	add(Thread{ID: "a1", Subject: "SREday NYC 2027 sponsorship", Company: "Acme", Event: "SREday NYC 2027", Category: "sponsors", Loose: true, Priority: "high",
		Label: "Acme Gold package undecided", Why: "Jane said on Oct 28 she'd discuss the Gold package with her team, and nothing since. NYC is in 3 weeks.", Summary: "Jane Doe (Head of DevRel, Acme) asked for the prospectus, liked the booth + talk slot package, and said she'd think about it with her team.",
		Waiting: "them", People: []Person{{"Jane Doe", "jane@acme.com"}}, Messages: 5, FirstAt: now.AddDate(0, 0, -30), LastAt: now.AddDate(0, 0, -14), LastFrom: "Jane Doe", HistoryID: "1", AnalyzedHistory: "1", AnalyzedBy: "claude-opus-5-5", AnalyzedAt: now,
		Actions: []Action{{Title: "Nudge Jane about the Gold package", Prompt: "Draft a short nudge to Jane Doe (Acme)...", Kind: "reply"}, {Title: "Propose SREday London instead", Prompt: "Draft...", Kind: "reply"}, {Title: "Snooze until 6 Jan", Prompt: "Their 2027 budget opens in January.", Kind: "snooze", Until: "2027-01-06"}}})
	add(Thread{ID: "a2", Subject: "Hosting SREday Toronto", Company: "Shopify", Event: "SREday Toronto", Category: "sponsors", Loose: true, Priority: "medium",
		Label: "Shopify to host Toronto 2027", FollowUp: now.AddDate(0, 0, -1).Format("2006-01-02"), Why: "They asked to come back after their Q1 planning.", Summary: "Shopify could host but not before spring.",
		Waiting: "you", People: []Person{{"Sam Lee", "sam@shopify.com"}}, Messages: 3, FirstAt: now.AddDate(0, -6, 0), LastAt: now.AddDate(0, -6, 0), LastFrom: "Sam Lee", HistoryID: "1", AnalyzedHistory: "1", AnalyzedBy: "claude-opus-5-5",
		Actions: []Action{{Title: "Check in with Sam about hosting", Prompt: "Draft...", Kind: "reply"}}})
	add(Thread{ID: "a3", Subject: "Contract for LLMday London", Company: "Pinecone", Event: "LLMday London 2026", Category: "sponsors", Loose: true, Priority: "high",
		Label: "Pinecone contract not sent", Why: "They agreed to the Silver package and are waiting for the contract.", Summary: "Agreed, waiting on the contract.",
		Waiting: "you", People: []Person{{"Ola K", "ola@pinecone.io"}}, Messages: 7, FirstAt: now.AddDate(0, 0, -20), LastAt: now.AddDate(0, 0, -2), LastFrom: "Ola K", HistoryID: "1", AnalyzedHistory: "1", AnalyzedBy: "claude-opus-5-5"})
	add(Thread{ID: "b1", Subject: "My talk at SREday NYC", Category: "speakers", Loose: true, Priority: "high", Label: "Speaker cancelled", CancelName: "Ann Lee",
		StillListed: "Still on the SREday NYC 2026 lineup (sreday/sreday/2026-nyc-q4 talks.csv, status talk): Pagers at 3am", ListedAt: now,
		People: []Person{{"Ann Lee", "ann@corp.io"}}, Messages: 1, FirstAt: now.AddDate(0, 0, -3), LastAt: now.AddDate(0, 0, -3), LastFrom: "Ann Lee", Waiting: "you", HistoryID: "1", AnalyzedHistory: "1"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/?view="+os.Getenv("SNAP_VIEW"), nil)
	req.SetBasicAuth("", "pw")
	s.Handler().ServeHTTP(rec, req)
	page := strings.ReplaceAll(rec.Body.String(), `src="/assets/badger-head.png"`, `src="`+os.Getenv("SNAP_HEAD")+`"`)
	os.WriteFile(out, []byte(page), 0o644)
}
