package main

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"
	"time"

	"github.com/sreday/badger/pkg"
)

func TestParseScan(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  ScanKey
		ok    bool
	}{
		{"badge QR", "linkedin://in/ab-nour", ScanKey{"linkedin", "ab-nour"}, true},
		{"https profile URL", "https://www.linkedin.com/in/AB-Nour/?trk=public", ScanKey{"linkedin", "ab-nour"}, true},
		{"no www", "linkedin.com/in/ab-nour", ScanKey{"linkedin", "ab-nour"}, true},
		{"mangled by keyboard layout", "linkedin;//in-ab-nour", ScanKey{"linkedin", "ab-nour"}, true},
		{"mangled colon only", "linkedinÖ//in/ab-nour", ScanKey{"linkedin", "ab-nour"}, true},
		{"percent-encoded slug", "https://www.linkedin.com/in/%C5%82ukasz", ScanKey{"linkedin", "łukasz"}, true},
		{"trailing newline from scanner", "linkedin://in/ab-nour\n", ScanKey{"linkedin", "ab-nour"}, true},
		{"luma check-in QR", "https://luma.com/check-in/evt-BtqobjFadNdgEcP?pk=g-6B4A6TxxJ9f4KWu", ScanKey{"luma", "g-6B4A6TxxJ9f4KWu"}, true},
		{"guest id", "gst-U7QXCggu0HCnMcV", ScanKey{"luma", "gst-U7QXCggu0HCnMcV"}, true},
		{"ticket key", "tkt-lCYWUHhPBdTFnVT", ScanKey{"luma", "tkt-lCYWUHhPBdTFnVT"}, true},
		{"email", " Ada@Example.com ", ScanKey{"email", "ada@example.com"}, true},
		{"wifi QR", "WIFI:T:WPA;S:sreday;P:secret;H:false;;", ScanKey{}, false},
		{"random URL", "https://sreday.com/", ScanKey{}, false},
		{"empty", "   ", ScanKey{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseScan(tt.input)
			if ok != tt.ok || got != tt.want {
				t.Errorf("ParseScan(%q) = %v, %v; want %v, %v", tt.input, got, ok, tt.want, tt.ok)
			}
		})
	}
}

// The badge QR is built from pkg.ExtractLinkedIn; whatever a guest typed,
// scanning their badge must land on the same key as the guest index.
func TestBadgeQRRoundTrip(t *testing.T) {
	answers := []string{
		"https://www.linkedin.com/in/Ab-Nour/",
		"linkedin.com/in/ab-nour",
		"/in/ab-nour",
		"ab-nour",
		"https://www.linkedin.com/in/ab-nour?utm_source=share",
	}
	for _, a := range answers {
		url := pkg.ExtractLinkedIn(map[string]string{"linkedin": a})
		badgeQR := strings.Replace(url, "https://www.linkedin.com/", "linkedin://", 1) // as in pkg.GenerateBadge
		key, ok := ParseScan(badgeQR)
		if !ok || key.Value != SlugFromURL(url) || key.Value != "ab-nour" {
			t.Errorf("answer %q: badge %q parsed to %v, index key %q", a, badgeQR, key, SlugFromURL(url))
		}
	}
}

func testGuests() []Guest {
	return []Guest{
		{ID: "gst-1", Key: "g-aaaaaa1", Tickets: []string{"tkt-aaaaaa1"}, FirstName: "Ada", LastName: "Lovelace", Name: "Ada Lovelace",
			Email: "Ada@Example.com", Title: "CTO", Company: "Analytical", LinkedIn: "https://www.linkedin.com/in/ada"},
		{ID: "gst-2", Key: "g-bbbbbb2", Name: "Grace Hopper", Email: "grace@example.com", LinkedIn: "https://www.linkedin.com/in/shared"},
		{ID: "gst-3", Key: "g-cccccc3", Name: "Copy Cat", Email: "cat@example.com", LinkedIn: "https://www.linkedin.com/in/shared"},
	}
}

func TestGuestIndexLookup(t *testing.T) {
	ix := NewGuestIndex(testGuests())
	tests := []struct {
		key    ScanKey
		wantID string
	}{
		{ScanKey{"linkedin", "ada"}, "gst-1"},
		{ScanKey{"luma", "g-aaaaaa1"}, "gst-1"},
		{ScanKey{"luma", "gst-1"}, "gst-1"},
		{ScanKey{"luma", "tkt-aaaaaa1"}, "gst-1"},
		{ScanKey{"email", "ada@example.com"}, "gst-1"},
		{ScanKey{"linkedin", "shared"}, ""}, // two guests: no guessing
		{ScanKey{"luma", "g-bbbbbb2"}, "gst-2"},
		{ScanKey{"linkedin", "nobody"}, ""},
	}
	for _, tt := range tests {
		g, ok := ix.Lookup(tt.key)
		gotID := ""
		if ok {
			gotID = g.ID
		}
		if gotID != tt.wantID {
			t.Errorf("Lookup(%v) = %q, want %q", tt.key, gotID, tt.wantID)
		}
	}
	var nilIx *GuestIndex
	if _, ok := nilIx.Lookup(ScanKey{"linkedin", "ada"}); ok {
		t.Error("nil index should not match")
	}
}

func TestStoreScansDedupeAndReload(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := s.AddSponsor("Acme", LumaEvent{APIId: "evt-1", Name: "SREday Test", Timezone: "Europe/London"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sp.PIN) != 4 || len(sp.Token) != 32 {
		t.Fatalf("bad token/pin: %q %q", sp.Token, sp.PIN)
	}
	ix := NewGuestIndex(testGuests())
	t0 := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)

	add := func(id, raw string, at time.Time) (Lead, bool) {
		key, _ := ParseScan(raw)
		l, dup, err := s.AddScan(sp, Lead{ScanID: id, Raw: raw, Key: key, Identity: identity(key, ix), ScannedAt: at})
		if err != nil {
			t.Fatal(err)
		}
		return l, dup
	}

	add("s2", "linkedin://in/nobody", t0.Add(2*time.Minute))
	add("s1", "linkedin://in/ada", t0)
	if _, dup := add("s1", "linkedin://in/ada", t0); dup {
		t.Error("retry of the same scan id should not be a duplicate")
	}
	l, dup := add("s3", "https://luma.com/check-in/evt-1?pk=g-aaaaaa1", t0.Add(5*time.Minute))
	if !dup || l.ScanID != "s1" {
		t.Errorf("ticket scan of the same person: dup=%v scan=%s, want dup of s1", dup, l.ScanID)
	}
	if _, err := s.UpdateLead(sp, "s1", "hot", "wants a demo"); err != nil {
		t.Fatal(err)
	}

	// Reopen from disk: same state.
	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	sp2, ok := s2.Sponsor(sp.Token)
	if !ok || sp2.PIN != sp.PIN {
		t.Fatal("sponsor not reloaded")
	}
	leads := s2.Leads(sp.Token)
	if len(leads) != 2 || leads[0].ScanID != "s1" || leads[1].ScanID != "s2" {
		t.Fatalf("leads after reload = %+v", leads)
	}
	if leads[0].Rating != "hot" || leads[0].Note != "wants a demo" {
		t.Errorf("update not reloaded: %+v", leads[0])
	}

	var buf bytes.Buffer
	if err := WriteCSV(&buf, sp2, leads, ix); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(buf.String(), "\ufeff"))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		csvHeader,
		{"2026-10-23 11:00:00", "Ada", "Lovelace", "Ada@Example.com", "CTO", "Analytical", "https://www.linkedin.com/in/ada", "hot", "wants a demo", ""},
		{"2026-10-23 11:02:00", "", "", "", "", "", "https://www.linkedin.com/in/nobody", "", "", ""},
	}
	if len(rows) != len(want) {
		t.Fatalf("csv rows = %v", rows)
	}
	for i := range want {
		if strings.Join(rows[i], "|") != strings.Join(want[i], "|") {
			t.Errorf("row %d = %v, want %v", i, rows[i], want[i])
		}
	}
}

func TestCSVSafe(t *testing.T) {
	for in, want := range map[string]string{"=HYPERLINK(1)": "'=HYPERLINK(1)", "+1 555": "'+1 555", "Acme": "Acme", "": ""} {
		if got := csvSafe(in); got != want {
			t.Errorf("csvSafe(%q) = %q, want %q", in, got, want)
		}
	}
}
