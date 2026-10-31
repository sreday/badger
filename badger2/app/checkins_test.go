package app

import (
	"bytes"
	"encoding/csv"
	"net/http"
	"net/http/httptest"
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
	if got := strings.Join(rows[1], "|"); got != "2026-11-02 09:14:03|Jo Doe|jo@example.com|SRE|Acme|scan|printed||Laptop X1|evt-abc123|evt-abc123|a1" {
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
