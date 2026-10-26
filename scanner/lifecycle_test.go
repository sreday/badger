package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPhases(t *testing.T) {
	start := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	sp := Sponsor{EventStart: start, EventEnd: start.Add(8 * time.Hour)}
	for _, tc := range []struct {
		at   time.Time
		want string
	}{
		{start.AddDate(0, -2, 0), PhaseTest},
		{start.Add(-6*time.Hour - time.Second), PhaseTest},
		{start.Add(-6 * time.Hour), PhaseLive},
		{start.Add(14*time.Hour - time.Second), PhaseLive}, // end + 6h
		{start.Add(14 * time.Hour), PhaseClosed},
		{start.Add(14*time.Hour + 90*24*time.Hour - time.Second), PhaseClosed},
		{start.Add(14*time.Hour + 90*24*time.Hour), PhaseDeleted},
	} {
		if got := sp.PhaseAt(tc.at); got != tc.want {
			t.Errorf("PhaseAt(%v) = %s, want %s", tc.at, got, tc.want)
		}
	}
	if got := (Sponsor{}).PhaseAt(time.Now()); got != PhaseLive {
		t.Errorf("link without event times = %s, want live", got)
	}
}

func TestUniquePIN(t *testing.T) {
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ev := func(id string) LumaEvent {
		return LumaEvent{APIId: id, StartAt: now.Add(48 * time.Hour).Format(time.RFC3339)}
	}
	seen := map[string]bool{}
	for i := 0; i < 300; i++ {
		sp, err := st.AddSponsor(fmt.Sprint("S", i), ev(fmt.Sprint("evt-", i%7)))
		if err != nil {
			t.Fatal(err)
		}
		if seen[sp.PIN] {
			t.Fatalf("PIN %s given out twice", sp.PIN)
		}
		seen[sp.PIN] = true
	}

	// Every PIN taken: one held by a link idle for 100+ days is the only
	// one that can be given out again.
	st2, _ := OpenStore(t.TempDir())
	old := now.Add(-101 * 24 * time.Hour)
	for i := 0; i < 10000; i++ {
		at := now
		if i == 1234 {
			at = old
		}
		st2.sponsors[fmt.Sprint(i)] = &Sponsor{Token: fmt.Sprint(i), PIN: fmt.Sprintf("%04d", i), EventID: "evt-old", CreatedAt: at}
	}
	st2.mu.Lock()
	pin, err := st2.uniquePIN("evt-new", now)
	st2.mu.Unlock()
	if err != nil || pin != "1234" {
		t.Errorf("uniquePIN = %q, %v; want 1234, the only PIN idle for 100 days", pin, err)
	}

	// A link for an event months away isn't idle while it waits for it.
	st2.sponsors["1234"].EventStart = now.Add(30 * 24 * time.Hour)
	st2.mu.Lock()
	pin, err = st2.uniquePIN("evt-new", now)
	st2.mu.Unlock()
	if err != nil || len(pin) != 4 {
		t.Errorf("fallback to unique-in-event: %q, %v", pin, err)
	}
}

// lifecycleServer runs the server on a fake clock against the fake Luma.
type lifecycleServer struct {
	t     *testing.T
	srv   *Server
	ts    *httptest.Server
	clock time.Time
	sp    *Sponsor
	c     *http.Client
	mails []Mail
}

func newLifecycleServer(t *testing.T, full bool) *lifecycleServer {
	luma := fakeLuma(t)
	t.Cleanup(luma.Close)
	lumaBaseURL = luma.URL

	l := &lifecycleServer{t: t}
	dir := t.TempDir()
	srv, err := newServer(Config{DataDir: dir, LumaAPIKey: "test-key", AdminPassword: "pw", FullAnswers: full})
	if err != nil {
		t.Fatal(err)
	}
	srv.now = func() time.Time { return l.clock }
	srv.mail = func(m Mail) error { l.mails = append(l.mails, m); return nil }
	l.srv = srv
	l.ts = httptest.NewServer(srv.routes())
	t.Cleanup(l.ts.Close)

	start := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	l.sp, err = srv.store.AddSponsor("Acme", LumaEvent{
		APIId: "evt-1", Name: "SREday Test", Timezone: "Europe/Amsterdam",
		StartAt: start.Format(time.RFC3339), EndAt: start.Add(8 * time.Hour).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	l.clock = start.AddDate(0, -2, 0) // two months early: test mode

	jar, _ := cookiejar.New(nil)
	l.c = &http.Client{Jar: jar}
	l.c.PostForm(l.base()+"/pin", url.Values{"pin": {l.sp.PIN}})
	return l
}

func (l *lifecycleServer) base() string { return l.ts.URL + "/s/" + l.sp.Token }

func (l *lifecycleServer) scan(id, raw string, at time.Time) syncResult {
	l.t.Helper()
	return postSync(l.t, l.c, l.base(), []syncOp{{Op: "scan", ScanID: id, Raw: raw, ScannedAt: at}})[0]
}

func (l *lifecycleServer) leads() (string, []LeadView) {
	l.t.Helper()
	var out struct {
		Phase string
		Leads []LeadView
	}
	if err := json.Unmarshal([]byte(get(l.t, l.c, l.base()+"/leads")), &out); err != nil {
		l.t.Fatal(err)
	}
	return out.Phase, out.Leads
}

func TestLifecycle(t *testing.T) {
	l := newLifecycleServer(t, false)
	leadsFile := filepath.Join(l.srv.cfg.DataDir, "events", "evt-1", "leads.jsonl")

	// Test mode: everything works, nothing reaches the disk, gone in an hour.
	if body := get(t, l.c, l.base()); !strings.Contains(body, "TEST MODE") || !strings.Contains(body, "Tap to start camera") {
		t.Fatal("expected the scanner in test mode")
	}
	if r := l.scan("t1", "linkedin://in/ada-l", l.clock); r.Status != "ok" || r.Lead.ExpiresAt.IsZero() || r.Lead.Name != "Ada Lovelace" {
		t.Fatalf("test scan = %+v", r)
	}
	postSync(t, l.c, l.base(), []syncOp{{Op: "update", ScanID: "t1", Rating: "hot", Note: "tried it"}})
	if phase, leads := l.leads(); phase != PhaseTest || len(leads) != 1 || leads[0].Rating != "hot" {
		t.Fatalf("test leads = %s %+v", phase, leads)
	}
	if csv := get(t, l.c, l.base()+"/export.csv"); !strings.Contains(csv, "Ada,Lovelace") || !strings.Contains(csv, "tried it") {
		t.Errorf("test CSV =\n%s", csv)
	}
	if _, err := os.Stat(leadsFile); !os.IsNotExist(err) {
		t.Error("test scans must never be written to disk")
	}
	// The same badge again is a new test lead (sponsors try it with one badge).
	if r := l.scan("t1b", "linkedin://in/ada-l", l.clock); r.Status != "ok" || r.Lead.ScanID != "t1b" {
		t.Errorf("repeat test scan = %+v", r)
	}
	l.srv.store.mu.Lock()
	l.srv.store.testLeads[l.sp.Token] = l.srv.store.testLeads[l.sp.Token][:1]
	delete(l.srv.store.testByScan, "t1b")
	l.srv.store.mu.Unlock()
	l.clock = l.clock.Add(59 * time.Minute)
	if _, leads := l.leads(); len(leads) != 1 {
		t.Error("test lead gone too early")
	}
	l.clock = l.clock.Add(time.Minute)
	if _, leads := l.leads(); len(leads) != 0 {
		t.Error("test lead should be gone after 60 minutes")
	}

	// A test scan still within its hour doesn't follow into the live list.
	liveFrom, closesAt, deletesAt := l.sp.Window()
	l.clock = liveFrom.Add(-10 * time.Minute)
	l.scan("t2", "linkedin://in/ada-l", l.clock)
	l.clock = liveFrom
	if phase, leads := l.leads(); phase != PhaseLive || len(leads) != 0 {
		t.Fatalf("live list should start empty: %s %+v", phase, leads)
	}
	// An offline test scan synced after going live is still a test scan.
	if r := l.scan("t3", "linkedin://in/ada-l", liveFrom.Add(-time.Minute)); r.Lead == nil || r.Lead.ExpiresAt.IsZero() {
		t.Errorf("late test scan = %+v", r)
	}

	// Live.
	if r := l.scan("a", "linkedin://in/ada-l", l.clock); r.Status != "ok" || !r.Lead.ExpiresAt.IsZero() {
		t.Fatalf("live scan = %+v", r)
	}
	if _, leads := l.leads(); len(leads) != 1 {
		t.Fatalf("live leads = %+v", leads)
	}

	// Closed: new scans refused, an offline scan from before closing is
	// accepted for a day, the CSV stays.
	l.clock = closesAt.Add(time.Hour)
	if body := get(t, l.c, l.base()); !strings.Contains(body, "Scanning has closed") || strings.Contains(body, "Tap to start camera") {
		t.Error("closed page should have no camera")
	}
	if r := l.scan("b", "https://luma.com/check-in/evt-1?pk=g-bbbbbb2", l.clock); r.Status != "closed" {
		t.Errorf("scan after closing = %s", r.Status)
	}
	if r := l.scan("c", "https://luma.com/check-in/evt-1?pk=g-bbbbbb2", closesAt.Add(-time.Hour)); r.Status != "ok" {
		t.Errorf("offline scan from before closing = %s", r.Status)
	}
	l.clock = closesAt.Add(lateSync + time.Minute)
	if r := l.scan("d", "x@example.com", closesAt.Add(-time.Hour)); r.Status != "closed" {
		t.Errorf("offline scan more than a day late = %s", r.Status)
	}
	if r := postSync(t, l.c, l.base(), []syncOp{{Op: "update", ScanID: "a", Rating: "cold"}})[0]; r.Status != "closed" {
		t.Errorf("update after closing = %s", r.Status)
	}
	if csv := get(t, l.c, l.base()+"/export.csv"); strings.Count(csv, "\n") != 3 {
		t.Errorf("closed CSV =\n%s", csv)
	}

	// Email works while closed.
	resp, _ := l.c.Post(l.base()+"/email", "application/json", strings.NewReader(`{"to":"bob@acme.com","cc":"ceo@acme.com, Bob@acme.com; sales@acme.com"}`))
	if resp.StatusCode != http.StatusNoContent || len(l.mails) != 1 || l.mails[0].To != "bob@acme.com" ||
		!bytes.Contains(l.mails[0].CSV, []byte("Ada,Lovelace")) || !strings.Contains(l.mails[0].Body, "2 conversations") ||
		l.mails[0].Subject != "Acme Team Lead Scanning - SREday Test" {
		t.Fatalf("email: %d %+v", resp.StatusCode, l.mails)
	}
	first := l.mails[0]
	if strings.Join(first.Cc, ",") != "ceo@acme.com,sales@acme.com" {
		t.Errorf("cc = %v (the To address is not repeated in CC)", first.Cc)
	}
	if !strings.Contains(first.Body, l.base()) || !strings.Contains(first.Body, "PIN "+l.sp.PIN) || !strings.Contains(first.Body, "Snout by Badger") {
		t.Errorf("body should carry the scanner link, PIN and signature:\n%s", first.Body)
	}
	if first.MessageID == "" || first.InReplyTo != "" {
		t.Errorf("first email starts the thread: %+v", first)
	}
	l.c.Post(l.base()+"/email", "application/json", strings.NewReader(`{"to":"bob@acme.com"}`))
	l.c.Post(l.base()+"/email", "application/json", strings.NewReader(`{"to":"alice@acme.com"}`))
	second, third := l.mails[1], l.mails[2]
	if second.InReplyTo != first.MessageID || second.References != first.MessageID || second.Subject != first.Subject {
		t.Errorf("second email should follow up on the first: %+v", second)
	}
	if third.InReplyTo != second.MessageID || third.References != first.MessageID+" "+second.MessageID {
		t.Errorf("third email should follow up on the second: %+v", third)
	}
	if resp, _ := l.c.Post(l.base()+"/email", "application/json", strings.NewReader(`{"to":"bob@acme.com","cc":"nope"}`)); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad CC: %d", resp.StatusCode)
	}
	l.mails = l.mails[:1]
	l.srv.mailMu.Lock()
	l.srv.mailSent = map[string][]time.Time{l.sp.Token: {l.clock}}
	l.srv.mailMu.Unlock()
	if resp, _ := l.c.Post(l.base()+"/email", "application/json", strings.NewReader(`{"to":"not an email"}`)); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad address: %d", resp.StatusCode)
	}
	for i := 0; i < 12; i++ {
		l.c.Post(l.base()+"/email", "application/json", strings.NewReader(`{"to":"bob@acme.com"}`))
	}
	if len(l.mails) != 10 {
		t.Errorf("rate limit: %d emails sent, want 10 an hour", len(l.mails))
	}

	// 90 days after closing: deleted, file gone, link says so.
	l.clock = deletesAt.Add(time.Minute)
	if n, err := l.srv.store.DeleteExpired(l.clock); n != 1 || err != nil {
		t.Fatalf("DeleteExpired = %d, %v", n, err)
	}
	if _, err := os.Stat(leadsFile); !os.IsNotExist(err) {
		t.Error("leads file should be deleted")
	}
	if body := get(t, l.c, l.base()); !strings.Contains(body, "were deleted") {
		t.Error("deleted link should say so")
	}
	if resp, _ := l.c.Post(l.base()+"/sync", "application/json", strings.NewReader(`{"ops":[]}`)); resp.StatusCode != http.StatusGone {
		t.Errorf("sync after deletion: %d", resp.StatusCode)
	}

	// A restart keeps the deletion.
	st, err := OpenStore(l.srv.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if sp, _ := st.Sponsor(l.sp.Token); sp.DeletedAt.IsZero() || len(st.Leads(sp.Token, false, l.clock)) != 0 {
		t.Error("deletion lost on restart")
	}
}

func TestFullAnswers(t *testing.T) {
	l := newLifecycleServer(t, true)
	l.clock, _, _ = l.sp.Window()
	r := l.scan("a", "linkedin://in/ada-l", l.clock)
	if r.Lead == nil || r.Lead.Email != "ada@example.com" || len(r.Lead.Answers) != 1 || r.Lead.Answers[0].Value != "Yes" {
		t.Fatalf("full lead view = %+v", r.Lead)
	}
	l.scan("b", "https://luma.com/check-in/evt-1?pk=g-bbbbbb2", l.clock.Add(time.Minute))
	csv := strings.TrimPrefix(get(t, l.c, l.base()+"/export.csv"), "\ufeff")
	lines := strings.Split(strings.TrimSpace(csv), "\n")
	if lines[0] != "Scanned At,First Name,Last Name,Email,Phone,Job Title,Company,LinkedIn,Rating,Note,Scanned By,Terms and Conditions" {
		t.Errorf("header = %s", lines[0])
	}
	if !strings.HasSuffix(lines[1], ",Yes") || !strings.HasSuffix(lines[2], ",") {
		t.Errorf("rows =\n%s", csv)
	}
}

func TestTeamReport(t *testing.T) {
	sp := &Sponsor{Name: "Harness", EventName: "SREday Amsterdam 2026 Q4", EventTZ: "Europe/Amsterdam"}
	at := func(h, m int) time.Time { return time.Date(2026, 11, 2, h-1, m, 0, 0, time.UTC) } // CET = UTC+1
	leads := []Lead{
		{ScannedAt: at(9, 12), ScannedBy: "Bob", Rating: "hot", Note: "Wants a demo"},
		{ScannedAt: at(12, 5), ScannedBy: "Alice", Rating: "cold"},
		{ScannedAt: at(12, 20), ScannedBy: "bob", Rating: "hot"},
		{ScannedAt: at(12, 41), ScannedBy: "Bob"},
		{ScannedAt: at(16, 55), Note: "Send pricing"},
	}
	got := teamReport(sp, leads, at(17, 40), 400, 312)
	t.Log("\n" + got)
	for _, want := range []string{
		"Here's how the Harness team did at SREday Amsterdam 2026 Q4, as of 2 Nov 17:40 (Amsterdam time).",
		"The event: 312 attendees checked in (400 registered).", "count only the people your team talked to",
		"5 conversations scanned by your team", "🔥 2 hot · ❄️ 1 cold · 2 not rated", "📝 2 with notes",
		"Bob: 3 (🔥 2)", "Alice: 1\n", "(no name set): 1",
		"Busiest hour: 12:00–13:00, 3 conversations", "First scan 09:12, last 16:55",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q", want)
		}
	}
	if got := teamReport(sp, nil, at(10, 0), 400, 0); !strings.Contains(got, "No conversations scanned yet") || !strings.Contains(got, "The event: 400 people registered.") {
		t.Errorf("empty report = %s", got)
	}
}

func TestBuildMessageHeaders(t *testing.T) {
	msg := string(buildMessage("mark@sreday.com", "hello@sreday.com", Mail{
		To: "bob@acme.com", Cc: []string{"ceo@acme.com", "sales@acme.com"}, Subject: "Acme Team Lead Scanning - SREday Test",
		MessageID: "<b@sreday.com>", InReplyTo: "<a@sreday.com>", References: "<a@sreday.com>",
		Body: "hi", Filename: "x.csv", CSV: []byte("a,b\n"),
	}))
	for _, want := range []string{
		"From: Snout by Badger <mark@sreday.com>\r\n", "Cc: ceo@acme.com, sales@acme.com\r\n",
		"Message-ID: <b@sreday.com>\r\n", "In-Reply-To: <a@sreday.com>\r\n", "References: <a@sreday.com>\r\n",
		"Reply-To: hello@sreday.com\r\n", "Subject: Acme Team Lead Scanning - SREday Test\r\n",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q", want)
		}
	}
	if id := newMessageID("mark@sreday.com"); !strings.HasPrefix(id, "<snout.") || !strings.HasSuffix(id, "@sreday.com>") {
		t.Errorf("message id = %s", id)
	}
}
