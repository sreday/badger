package main

import (
	"crypto/sha256"
	"encoding/hex"
	"html/template"
	"net/http"
	"path"
	"strings"

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

// handleHeadAsset serves the turning head's files. They're fingerprinted
// (ETag from the content) and always revalidated, so a re-render can never
// pair new views with old eye spots; unchanged files answer 304.
func handleHeadAsset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	data, err := headFS.ReadFile("assets/head/" + name)
	if err != nil || strings.Contains(name, "/") {
		http.NotFound(w, r)
		return
	}
	types := map[string]string{".webp": "image/webp", ".png": "image/png", ".json": "application/json", ".js": "text/javascript; charset=utf-8"}
	ct, ok := types[path.Ext(name)]
	if !ok {
		http.NotFound(w, r)
		return
	}
	sum := sha256.Sum256(data)
	tag := `"` + hex.EncodeToString(sum[:8]) + `"`
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", tag)
	if r.Header.Get("If-None-Match") == tag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Write(data)
}
