package main

import (
	"html/template"
	"net/http"
)

// toolFuncs: "tool" builds the header brand (badger head + name + subtitle)
// for templates/brand.html.
var toolFuncs = template.FuncMap{
	"tool": func(name, sub string, ver ...string) map[string]string {
		v := ""
		if len(ver) > 0 {
			v = ver[0]
		}
		return map[string]string{"Name": name, "Sub": sub, "Ver": v}
	},
}

// handleHub is scan.sreday.com/: the Badger logo and a button per tool.
// It shows nothing private; every tool asks for its own login.
func (s *Server) handleHub(w http.ResponseWriter, r *http.Request) {
	s.render(w, "hub.html", map[string]any{})
}

type sniffEventRow struct {
	ID, Name, When string
	Sessions       int
}

// handleSniffIndex lists the events to open Sniff for: upcoming ones from
// Luma, and any older one that already has sessions.
func (s *Server) handleSniffIndex(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{}
	events, err := ListEvents(s.cfg.LumaAPIKey)
	if err != nil {
		data["Error"] = "Luma: " + err.Error()
	}
	var rows []sniffEventRow
	for _, ev := range recentEvents(events) {
		start, _ := ev.Times()
		when := ""
		if !start.IsZero() {
			when = localTime(start, ev.Timezone)
		}
		rows = append(rows, sniffEventRow{ID: ev.APIId, Name: ev.Name, When: when, Sessions: s.visibleSessions(ev.APIId)})
	}
	data["Events"] = rows
	s.render(w, "sniff_index.html", data)
}

func (s *Server) visibleSessions(eventID string) int {
	n := 0
	for _, ss := range s.sniff.Sessions(eventID) {
		if !ss.Hidden {
			n++
		}
	}
	return n
}

func assetHandler(contentType string, data []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(data)
	}
}
