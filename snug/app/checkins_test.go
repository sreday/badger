package app

import (
	"bytes"
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sreday/badger/pkg"
)

func TestCheckinLog(t *testing.T) {
	s := &Server{
		events:   map[string]pkg.LumaEventEntry{},
		guests:   map[string]map[string]pkg.BadgeData{"evt-abc123": {"jo@example.com": {Name: "Jo Doe", Email: "jo@example.com", Title: "SRE", Company: "Acme"}}},
		checkins: &checkinLog{dir: t.TempDir(), seen: map[string]map[string]bool{}},
	}
	post := func(body string) {
		r := httptest.NewRequest("POST", "/checkins", strings.NewReader(body))
		w := httptest.NewRecorder()
		s.handleCheckins(w, r)
		if w.Code != 200 {
			t.Fatalf("POST = %d %s", w.Code, w.Body)
		}
	}
	entry := `{"id":"a1","at":"2026-11-02 09:14:03","event":"evt-abc123","email":"jo@example.com","name":"Jo Doe","method":"scan","status":"printed","laptop":"Laptop X1"}`
	post("[" + entry + "]")
	post("[" + entry + `,{"id":"a2","at":"2026-11-02 09:15:00","event":"evt-abc123","name":"Walk In","method":"walk-in","status":"failed","error":"cover open","laptop":"Laptop X1"},{"id":"x","event":"../etc"}]`)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /checkins/{eventID}", s.handleCheckinsCSV)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/checkins/evt-abc123", nil))
	rows, err := csv.NewReader(bytes.NewReader(w.Body.Bytes())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 { // header + two entries, the resent one once
		t.Fatalf("rows = %d: %v", len(rows), rows)
	}
	if got := strings.Join(rows[1], "|"); got != "2026-11-02 09:14:03|Jo Doe|jo@example.com|SRE|Acme|scan|printed||Laptop X1|evt-abc123|evt-abc123|a1|" {
		t.Errorf("row = %s", got)
	}
	if rows[2][5] != "walk-in" || rows[2][7] != "cover open" {
		t.Errorf("walk-in row = %v", rows[2])
	}
	if !strings.Contains(w.Header().Get("Content-Disposition"), "check-ins.csv") {
		t.Errorf("no download name: %q", w.Header().Get("Content-Disposition"))
	}
	// A fresh server reading the same file still knows a1.
	s.checkins = &checkinLog{dir: s.checkins.dir, seen: map[string]map[string]bool{}}
	post("[" + entry + "]")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/checkins/evt-abc123", nil))
	if n := strings.Count(w.Body.String(), "\n"); n != 3 {
		t.Errorf("after restart: %d lines", n)
	}
}

func TestCheckinLogJSON(t *testing.T) {
	s := &Server{
		events:   map[string]pkg.LumaEventEntry{},
		guests:   map[string]map[string]pkg.BadgeData{},
		checkins: &checkinLog{dir: t.TempDir(), seen: map[string]map[string]bool{}},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /checkins/{eventID}/log", s.handleCheckinsJSON)
	get := func() string {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/checkins/evt-abc123/log", nil))
		return strings.TrimSpace(w.Body.String())
	}
	if got := get(); got != "[]" {
		t.Fatalf("empty log = %s", got)
	}
	s.handleCheckins(httptest.NewRecorder(), httptest.NewRequest("POST", "/checkins", strings.NewReader(
		`[{"id":"a1","at":"2026-11-02 09:14:03","event":"evt-abc123","email":"jo@example.com","name":"Jo Doe","method":"luma check-in","status":"checked in on Luma","laptop":"Laptop X1"}]`)))
	if got := get(); !strings.Contains(got, `"email":"jo@example.com"`) || !strings.Contains(got, `"method":"luma check-in"`) || !strings.Contains(got, `"at":"2026-11-02 09:14:03"`) {
		t.Errorf("log = %s", got)
	}
}

// A log started before the LinkedIn column (12 columns) still reads, takes
// new rows, and downloads with the full header.
func TestCheckinLogOldFile(t *testing.T) {
	dir := t.TempDir()
	old := "Time,Name,Email,Job title,Company,How,Result,Error,Laptop,Event,Event ID,Entry ID\n" +
		"2026-11-02 08:01:00,Jo Doe,jo@example.com,SRE,Acme,scan,printed,,Laptop X1,Ev,evt-abc123,a1\n"
	if err := os.WriteFile(filepath.Join(dir, "evt-abc123.csv"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{events: map[string]pkg.LumaEventEntry{}, guests: map[string]map[string]pkg.BadgeData{},
		checkins: &checkinLog{dir: dir, seen: map[string]map[string]bool{}}}
	s.handleCheckins(httptest.NewRecorder(), httptest.NewRequest("POST", "/checkins", strings.NewReader(
		`[{"id":"a1","at":"x","event":"evt-abc123"},{"id":"w1","at":"2026-11-02 08:05:00","event":"evt-abc123","name":"Robin Vale","company":"Globex","linkedin":"https://www.linkedin.com/in/robin","method":"walk-in","status":"printed"}]`)))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /checkins/{eventID}", s.handleCheckinsCSV)
	mux.HandleFunc("GET /checkins/{eventID}/log", s.handleCheckinsJSON)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/checkins/evt-abc123", nil))
	rows, err := csv.NewReader(bytes.NewReader(w.Body.Bytes())).ReadAll()
	if err != nil || len(rows) != 3 || rows[0][12] != "LinkedIn" || rows[2][4] != "Globex" || rows[2][12] != "https://www.linkedin.com/in/robin" {
		t.Fatalf("download = %v %v", rows, err)
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/checkins/evt-abc123/log", nil))
	if !strings.Contains(w.Body.String(), `"company":"Globex"`) || strings.Count(w.Body.String(), `"id"`) != 2 {
		t.Errorf("log = %s", w.Body)
	}
}

func TestScanProblems(t *testing.T) {
	// Scans that went wrong go to their own spreadsheet, never the
	// check-ins (which tick people as printed).
	dir := t.TempDir()
	s := &Server{
		events:   map[string]pkg.LumaEventEntry{},
		guests:   map[string]map[string]pkg.BadgeData{},
		checkins: &checkinLog{dir: dir, seen: map[string]map[string]bool{}},
		scans:    &checkinLog{dir: filepath.Join(dir, "scans"), seen: map[string]map[string]bool{}},
	}
	h := http.NewServeMux()
	h.HandleFunc("POST /scans", s.handleScans)
	h.HandleFunc("GET /scans/{eventID}", s.handleScansCSV)
	h.HandleFunc("GET /checkins/{eventID}", s.handleCheckinsCSV)
	post := func(path, body string) int {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("POST", path, strings.NewReader(body)))
		return rr.Code
	}
	if c := post("/scans", `[{"id":"s1","at":"2026-11-06 09:00:00","event":"evt-x1","method":"scan","status":"not on the list","error":"https://luma.com/check-in/evt-x1?pk=g-abc","laptop":"Laptop A"}]`); c != 200 {
		t.Fatalf("POST /scans = %d", c)
	}
	get := func(path string) string {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
		return rr.Body.String()
	}
	if b := get("/scans/evt-x1"); !strings.Contains(b, "not on the list") || !strings.Contains(b, "pk=g-abc") {
		t.Errorf("scan problems = %q", b)
	}
	if b := get("/checkins/evt-x1"); strings.Contains(b, "not on the list") {
		t.Errorf("a scan problem landed in the check-ins: %q", b)
	}
}
