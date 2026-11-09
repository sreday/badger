package main

import (
	_ "embed"
	"net/http"
)

// One app for the desk laptop: Chrome's "Install" puts the Snout dashboard
// and Snug in their own window with a dock icon, no tabs or address
// bar. It's the same site, so offline mode, USB printing and the ticket
// scanner work as in the browser. Sponsors' scanner pages don't link the
// manifest: this is the organisers' app.

//go:embed assets/icon-192.png
var icon192 []byte

//go:embed assets/icon-512.png
var icon512 []byte

const appManifest = `{
  "name": "Snout by Badger",
  "short_name": "Badger",
  "description": "Sponsor leads and badge printing for SREday, LLMday and PLATFORMday",
  "id": "/admin",
  "start_url": "/snout",
  "scope": "/",
  "display": "standalone",
  "background_color": "#f0f6fa",
  "theme_color": "#5a3f75",
  "icons": [
    {"src": "/assets/icon-192.png", "sizes": "192x192", "type": "image/png", "purpose": "any"},
    {"src": "/assets/icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any"}
  ],
  "shortcuts": [
    {"name": "Print badges", "url": "/snug/", "icons": [{"src": "/assets/icon-192.png", "sizes": "192x192"}]},
    {"name": "Sponsor dashboard", "url": "/snout", "icons": [{"src": "/assets/icon-192.png", "sizes": "192x192"}]}
  ]
}`

func handleManifest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write([]byte(appManifest))
}

func pngHandler(data []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(data)
	}
}
