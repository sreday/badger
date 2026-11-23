package snore

import (
	"testing"
	"time"

	"github.com/sreday/badger/bridge"
)

func TestReviveRows(t *testing.T) {
	s, _ := testServer(t)
	now := s.now()
	ago := func(days int) time.Time { return now.Add(-time.Duration(days) * 24 * time.Hour) }
	// Our history: one sponsor from a year and a half ago, one from this year.
	old := s.fp.F["x/sreday/past/2025-amsterdam-q2"]
	old.Key = "x/sreday/past/2025-amsterdam-q2"
	old.Sponsors = []Sponsored{{Name: "Lapsed", Domain: "lapsed.example"}, {Name: "Both", Domain: "both.example"}}
	cur := s.fp.F["x/sreday/2026-london-q4"]
	cur.Key = "x/sreday/2026-london-q4"
	cur.Sponsors = []Sponsored{{Name: "Current", Domain: "current.example"}}

	s.cfg.Talks = func() []bridge.Talk {
		return []bridge.Talk{
			{Domain: "mail.quiet.example", Company: "Quiet Co", Label: "Asked for prices", Threads: 2, LastAt: ago(45), LastFromUs: true, Bucket: "active", URL: "https://mail.example/1"},
			{Domain: "fresh.example", Company: "Fresh Co", LastAt: ago(5), Bucket: "active"},
			{Domain: "snoozed.example", Company: "Snoozed Co", LastAt: ago(60), Bucket: "snoozed"},
			{Domain: "nobudget.example", Company: "No Budget Co", LastAt: ago(90), NoBudget: true, Bucket: "closed"},
			{Domain: "oldno.example", Company: "Old No Co", LastAt: ago(200), DeadEnd: true, Bucket: "closed"},
			{Domain: "current.example", Company: "Current", LastAt: ago(50), Bucket: "active"},
			{Domain: "both.example", Company: "Both", LastAt: ago(40), Bucket: "active"},
			{Domain: "gmail.com", Company: "Nobody", LastAt: ago(40), Bucket: "active"},
		}
	}
	rows := s.reviveRows(now)
	got := map[string]reviveRow{}
	var order []string
	for _, r := range rows {
		got[r.Domain] = r
		order = append(order, r.Domain)
	}
	for _, d := range []string{"fresh.example", "snoozed.example", "nobudget.example", "current.example"} {
		if _, ok := got[d]; ok {
			t.Errorf("%s does not belong on the list", d)
		}
	}
	if r := got["quiet.example"]; r.Why != "Talk went quiet" || r.Quiet != 45 || r.Talk == nil {
		t.Errorf("quiet talk = %+v", r)
	}
	if r := got["oldno.example"]; r.Why != "Dead end" {
		t.Errorf("an old dead end comes back after half a year: %+v", r)
	}
	if r := got["lapsed.example"]; r.Why != "Lapsed sponsor" || len(r.Events) != 1 || r.Talk != nil {
		t.Errorf("lapsed sponsor = %+v", r)
	}
	if r := got["both.example"]; r.Why != "Talk went quiet" || len(r.Events) != 1 || r.Talk == nil {
		t.Errorf("a past sponsor with a quiet talk keeps both: %+v", r)
	}
	if len(rows) != 4 {
		t.Fatalf("rows = %v", order)
	}
	// Past sponsors lead.
	if len(rows[0].Events) == 0 || len(rows[1].Events) == 0 {
		t.Errorf("past sponsors first: %v", order)
	}

}

func TestReviveWithoutMail(t *testing.T) {
	s, _ := testServer(t)
	if rows := s.reviveRows(s.now()); len(rows) != 0 {
		t.Fatalf("rows = %+v", rows)
	}
}
