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
	if body := get(t, l.c, l.base()); !strings.Contains(body, "Test mode") || !strings.Contains(body, "Start camera") {
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
	if body := get(t, l.c, l.base()); !strings.Contains(body, "Scanning has closed") || strings.Contains(body, "Start camera") {
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
	resp, _ := l.c.Post(l.base()+"/email", "application/json", strings.NewReader(`{"to":"bob@acme.com"}`))
	if resp.StatusCode != http.StatusNoContent || len(l.mails) != 1 || l.mails[0].To != "bob@acme.com" ||
		!bytes.Contains(l.mails[0].CSV, []byte("Ada,Lovelace")) || !strings.Contains(l.mails[0].Body, "2 people") {
		t.Fatalf("email: %d %+v", resp.StatusCode, l.mails)
	}
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
