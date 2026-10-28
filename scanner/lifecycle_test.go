package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
	if resp, _ := l.c.Post(l.base()+"/email", "application/json", strings.NewReader(`{"to":"bob@acme.com"}`)); resp.StatusCode != http.StatusBadRequest || len(l.mails) != 0 {
		t.Errorf("test mode must not send email: %d, %d sent", resp.StatusCode, len(l.mails))
	}
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
		!bytes.Contains(l.mails[0].CSV, []byte("Ada,Lovelace")) || !strings.Contains(l.mails[0].Body, "2 conversations scanned by your team: 0 hot, 0 not sure, 0 cold, 2 unrated, 0 with a note.") ||
		!strings.HasSuffix(l.mails[0].Body, "Questions? Just reply to this email. The list is attached.\n\nSREday Team\n") ||
		l.mails[0].Subject != "Acme Team Lead Scanning - SREday Test" {
		t.Fatalf("email: %d %+v", resp.StatusCode, l.mails)
	}
	first := l.mails[0]
	if strings.Join(first.Cc, ",") != "ceo@acme.com,sales@acme.com" {
		t.Errorf("cc = %v (the To address is not repeated in CC)", first.Cc)
	}
	if !strings.Contains(first.Body, l.base()) || !strings.Contains(first.Body, "PIN "+l.sp.PIN) || !strings.Contains(first.Body, l.base()+"/dashboard") {
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
	if body := get(t, l.c, l.base()); !strings.Contains(body, "no longer available") {
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
		{ScannedAt: at(12, 41), ScannedBy: "Bob", Rating: "unsure"},
		{ScannedAt: at(16, 55), Note: "Send pricing"},
	}
	want := "Hey Harness Team,\n\n" +
		"Here's how your team is doing at SREday Amsterdam 2026 Q4, as of right now.\n\n" +
		"The event: (400 attendees registered).\n" +
		"The list shows only people scanned by your team at the conference.\n\n" +
		"5 conversations scanned by your team: 2 hot, 1 not sure, 1 cold, 1 unrated, 2 with a note.\n"
	if got := teamReport(sp, leads, at(17, 40), 400); got != want {
		t.Errorf("report =\n%s\nwant\n%s", got, want)
	}
	closed := *sp
	closed.EventStart, closed.EventEnd = at(9, 0), at(11, 0)
	if got := teamReport(&closed, leads, at(17, 40), 400); !strings.Contains(got, "Here's how your team did at SREday Amsterdam 2026 Q4.\n") {
		t.Errorf("closed report should say did:\n%s", got)
	}
	if got := teamReport(sp, nil, at(10, 0), 400); !strings.HasSuffix(got, "No conversations scanned yet. Open your scanner link and scan a badge to get started.\n") {
		t.Errorf("empty report = %s", got)
	}
	for name, want := range map[string]string{
		"SREday Amsterdam 2026 Q4": "SREday Team", "LLMday NYC 2026 Q4": "LLMday Team",
		"PLATFORMday San Francisco 2026 Q4":                       "PLATFORMday Team",
		"LLMday + Prompt Engineering Conference - London 2026 Q4": "LLMday & PEC Team",
	} {
		if got := signature(name); got != want {
			t.Errorf("signature(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestHotLeads(t *testing.T) {
	ix := NewGuestIndex([]Guest{{ID: "gst-1", Name: "John Smith", Email: "john@acme.com", Title: "CTO", Company: "Acme", LinkedIn: "https://www.linkedin.com/in/john-smith"}})
	leads := []Lead{
		{Key: ScanKey{Kind: "linkedin", Value: "john-smith"}, Rating: "hot", Note: "Wants a demo"},
		{Key: ScanKey{Kind: "email", Value: "jane@<x>.com"}, Rating: "hot"},
		{Key: ScanKey{Kind: "linkedin", Value: "cold-one"}, Rating: "cold"},
	}
	text, h := hotLeads(leads, ix)
	if !strings.Contains(text, "🔥 Hot leads registered:\n- John Smith (https://www.linkedin.com/in/john-smith), CTO at Acme - john@acme.com - Notes: (Wants a demo)\n") ||
		!strings.Contains(text, "- jane@<x>.com\n") || strings.Contains(text, "cold-one") {
		t.Errorf("text =\n%s", text)
	}
	if !strings.Contains(h, `<li><a href="https://www.linkedin.com/in/john-smith" style="color:#0a66c2;font-weight:bold;text-decoration:none">John Smith</a>, CTO at Acme - <a href="mailto:john@acme.com" style="color:#141c25">john@acme.com</a> - Notes: (Wants a demo)</li>`) ||
		!strings.Contains(h, "jane@&lt;x&gt;.com") {
		t.Errorf("html =\n%s", h)
	}
	if text, h := hotLeads(leads[2:], ix); text != "" || h != "" {
		t.Error("no hot leads: no section")
	}
}

func TestRetention(t *testing.T) {
	// 2026 events keep the 90 days their attendees were promised; from
	// 2027 leads stay for 12 months.
	short := Sponsor{EventStart: time.Date(2026, 12, 31, 18, 0, 0, 0, time.UTC)}
	_, closesAt, deletesAt := short.Window()
	if !deletesAt.Equal(closesAt.Add(90*24*time.Hour)) || short.KeptFor() != "90 days" {
		t.Errorf("2026 event deletes at %v (%s), want 90 days after %v", deletesAt, short.KeptFor(), closesAt)
	}
	long := Sponsor{EventStart: longKeepFrom, EventEnd: longKeepFrom.Add(8 * time.Hour)}
	_, closesAt, deletesAt = long.Window()
	if !deletesAt.Equal(closesAt.AddDate(1, 0, 0)) || long.KeptFor() != "12 months" {
		t.Errorf("2027 event deletes at %v (%s), want a year after %v", deletesAt, long.KeptFor(), closesAt)
	}
	if got := long.PhaseAt(closesAt.AddDate(0, 11, 0)); got != PhaseClosed {
		t.Errorf("11 months after a 2027 event = %s, want closed", got)
	}
	if got := long.PhaseAt(deletesAt); got != PhaseDeleted {
		t.Errorf("a year after a 2027 event = %s, want deleted", got)
	}

	// A PIN stays taken while its link still has leads, even when idle.
	st, _ := OpenStore(t.TempDir())
	now := closesAt.Add(200 * 24 * time.Hour)
	for i := 0; i < 10000; i++ {
		st.sponsors[fmt.Sprint(i)] = &Sponsor{Token: fmt.Sprint(i), PIN: fmt.Sprintf("%04d", i), EventID: "evt-old", CreatedAt: now}
	}
	st.sponsors["42"] = &Sponsor{Token: "42", PIN: "0042", EventID: "evt-old", EventStart: long.EventStart, EventEnd: long.EventEnd}
	st.mu.Lock()
	pin, _ := st.uniquePIN("evt-old", now)
	st.mu.Unlock()
	if pin == "0042" {
		t.Error("PIN of a link that still has leads was given out again")
	}
}

func TestDashboard(t *testing.T) {
	l := newLifecycleServer(t, false)
	_, closesAt, _ := l.sp.Window()
	l.clock = closesAt.Add(-time.Hour)
	l.scan("a", "linkedin://in/ada-l", l.clock)

	// A computer without the PIN gets the PIN page, which comes back here.
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	resp, err := c.Get(l.base() + "/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("X-Robots-Tag") == "" || resp.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("dashboard headers = %v", resp.Header)
	}
	resp.Body.Close()
	if body := get(t, c, l.base()+"/dashboard"); !strings.Contains(body, `name="next" value="dashboard"`) || !strings.Contains(body, "Open dashboard") {
		t.Fatal("expected the dashboard's PIN page")
	}
	resp, err = c.PostForm(l.base()+"/pin", url.Values{"pin": {l.sp.PIN}, "next": {"dashboard"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Request.URL.Path != "/s/"+l.sp.Token+"/dashboard" {
		t.Errorf("after the PIN landed on %s, want the dashboard", resp.Request.URL.Path)
	}
	body := get(t, c, l.base()+"/dashboard")
	if !strings.Contains(body, "Download CSV") || strings.Contains(body, "deleted") || strings.Contains(body, "90 days") {
		t.Error("expected the dashboard, with nothing about deletion")
	}
	// The same PIN signs this computer in to the scanner's data too.
	if phase, leads := l.leads(); phase != PhaseLive || len(leads) != 1 {
		t.Errorf("leads = %s %+v", phase, leads)
	}

	// After deletion the dashboard says so too.
	_, _, deletesAt := l.sp.Window()
	l.clock = deletesAt.Add(time.Minute)
	l.srv.store.DeleteExpired(l.clock)
	if body := get(t, c, l.base()+"/dashboard"); !strings.Contains(body, "no longer available") || strings.Contains(body, "90 days") {
		t.Error("deleted dashboard should say so")
	}

	if body := get(t, c, l.ts.URL+"/robots.txt"); !strings.Contains(body, "Disallow: /") {
		t.Errorf("robots.txt = %q", body)
	}
}

func TestTestBadge(t *testing.T) {
	// Resolves without any guest list.
	var ix *GuestIndex
	for _, raw := range []string{testBadgeQR, "https://www.linkedin.com/in/marek-pawlikowski/"} {
		k, _ := ParseScan(raw)
		if g, ok := ix.Lookup(k); !ok || g.Name != "Mark Pawlikowski" || g.Email != "mark@sreday.com" {
			t.Errorf("Lookup(%q) = %+v, %v", raw, g, ok)
		}
	}

	l := newLifecycleServer(t, false)
	if r := l.scan("t1", testBadgeQR, l.clock); r.Lead == nil || r.Lead.Name != "Mark Pawlikowski" || r.Lead.Company != "SREday / LLMday / PLATFORMday / PEC" {
		t.Fatalf("test-mode scan = %+v", r)
	}
	resp, err := l.c.Get(l.base() + "/export.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, `filename="TEST - SREday Test - Acme - Leads Scanned.csv"`) {
		t.Errorf("test CSV name: %s", cd)
	}
	var b bytes.Buffer
	b.ReadFrom(resp.Body)
	if !strings.Contains(b.String(), "Mark,Pawlikowski,mark@sreday.com,Organizer,SREday / LLMday / PLATFORMday / PEC,https://www.linkedin.com/in/marek-pawlikowski") {
		t.Errorf("test CSV =\n%s", b.String())
	}

	// It works at the event too.
	l.clock, _, _ = l.sp.Window()
	if r := l.scan("a", testBadgeQR, l.clock); r.Status != "ok" || r.Lead.Name != "Mark Pawlikowski" {
		t.Errorf("live scan = %+v", r)
	}
	if body := get(t, l.c, l.base()+"/dashboard"); !strings.Contains(body, "/assets/test-badge.png") || !strings.Contains(body, l.sp.PIN) {
		t.Error("dashboard should show the setup steps with the test badge and the PIN")
	}
	resp, err = l.c.Get(l.base() + "/qr.png")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" {
		t.Errorf("scanner QR: %d", resp.StatusCode)
	}
	if resp, err := http.Get(l.base() + "/qr.png"); err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("scanner QR without the PIN: %v %v", resp, err)
	} else {
		resp.Body.Close()
	}
	resp, err = http.Get(l.ts.URL + "/assets/test-badge.png")
	if err != nil {
		t.Fatal(err)
	}
	png, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" || !bytes.Equal(png, testBadgePNG) {
		t.Errorf("test badge: %d %s, %d bytes", resp.StatusCode, resp.Header.Get("Content-Type"), len(png))
	}
}

func TestSponsorReadyCSV(t *testing.T) {
	guests := []Guest{
		{FirstName: "Grace", LastName: "Hopper", Email: "grace@navy.mil", Title: "Rear Admiral", Company: "US Navy", LinkedIn: "https://www.linkedin.com/in/grace-h", CheckedIn: true},
		{Name: "Ada Lovelace", Email: "ada@example.com", Title: "=CTO", Company: "Analytical", LinkedIn: "https://www.linkedin.com/in/ada-l"},
		{FirstName: "Élodie", LastName: "Ásgeirsdóttir", Email: "e@example.is", Title: "SRE", Company: "Íslandsbanki"},
	}
	var b bytes.Buffer
	n, err := WriteSponsorReadyCSV(&b, guests, false)
	if err != nil || n != 3 {
		t.Fatalf("WriteSponsorReadyCSV = %d, %v", n, err)
	}
	want := "\ufeffFirst name,Last name,Email,Job title,Company,LinkedIn\n" +
		"Élodie,Ásgeirsdóttir,e@example.is,SRE,Íslandsbanki,\n" +
		"Grace,Hopper,grace@navy.mil,Rear Admiral,US Navy,https://linkedin.com/in/grace-h\n" +
		"Ada,Lovelace,ada@example.com,'=CTO,Analytical,https://linkedin.com/in/ada-l\n"
	if b.String() != want {
		t.Errorf("CSV =\n%s\nwant\n%s", b.String(), want)
	}

	b.Reset()
	if n, _ := WriteSponsorReadyCSV(&b, guests, true); n != 1 || !strings.Contains(b.String(), "Hopper") {
		t.Errorf("checked in only = %d rows:\n%s", n, b.String())
	}
}
