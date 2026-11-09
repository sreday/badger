package main

import (
	"html/template"
	"net/http"

	"github.com/sreday/badger/ui"
)

// toolFuncs: "tool" builds the header brand (badger head + name + subtitle)
// for the shared ui/brand.html.
var toolFuncs = template.FuncMap{
	"stablePIN": stablePIN, // a sponsor whose PIN never changes (no Change PIN button)
	"tool":      ui.Tool,
}

// handleHub is scan.sreday.com/: the Badger logo and a button per tool.
// It shows nothing private; every tool asks for its own login.
func (s *Server) handleHub(w http.ResponseWriter, r *http.Request) {
	s.render(w, "hub.html", map[string]any{})
}

func assetHandler(contentType string, data []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(data)
	}
}
