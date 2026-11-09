// Package web holds the small HTTP and CSV helpers Snout and Sniff share.
package web

import (
	"fmt"
	"net/http"
	"strings"
)

// Private marks a page as unlisted: no search engines, the secret link
// never leaks to other sites as a referrer, and no framing.
func Private(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		next(w, r)
	}
}

// BaseURL is the site's address for links: publicURL when set, otherwise
// the request's own scheme and host.
func BaseURL(publicURL string, r *http.Request) string {
	if publicURL != "" {
		return publicURL
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// SendCSV sends a CSV download named after its title.
func SendCSV(w http.ResponseWriter, title string, data []byte) {
	filename := strings.NewReplacer(`"`, "", "/", "-", "\\", "-").Replace(title) + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Cache-Control", "no-store")
	w.Write(data)
}

// CSVSafe stops spreadsheet apps from running registration answers as formulas.
func CSVSafe(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}

// SafeName turns an id into a file or folder name: letters, digits, - and
// _ kept, everything else _.
func SafeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, s)
}

// Plural picks the word for n.
func Plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
