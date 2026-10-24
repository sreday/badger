package main

import (
	"net/url"
	"regexp"
	"strings"
)

// ScanKey identifies a person from a scanned payload.
type ScanKey struct {
	Kind  string `json:"kind"` // "linkedin", "luma" or "email"
	Value string `json:"value"`
}

// String returns a stable identity for deduplication.
func (k ScanKey) String() string {
	return k.Kind + ":" + k.Value
}

var (
	// Matches the badge QR (linkedin://in/<slug>) and any LinkedIn profile URL.
	// The separators are deliberately loose: HID scanners on a non-US keyboard
	// layout mangle ":" and "/" into other characters.
	linkedinRe = regexp.MustCompile(`linkedin(?:\.com)?[^a-z0-9]+in[^a-z0-9]([^?#/\s]+)`)
	// Luma guest id (gst-), guest key (g-, as in the check-in QR's pk=) or ticket key (tkt-).
	lumaKeyRe = regexp.MustCompile(`\b((?:gst|g|tkt)-[A-Za-z0-9]{6,})\b`)
)

// ParseScan turns a raw QR / hardware scanner payload into a ScanKey.
func ParseScan(raw string) (ScanKey, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ScanKey{}, false
	}
	lower := strings.ToLower(raw)

	if m := linkedinRe.FindStringSubmatch(lower); m != nil {
		if slug := NormalizeSlug(m[1]); slug != "" {
			return ScanKey{Kind: "linkedin", Value: slug}, true
		}
	}
	if m := lumaKeyRe.FindStringSubmatch(raw); m != nil {
		return ScanKey{Kind: "luma", Value: m[1]}, true
	}
	if strings.Contains(raw, "@") && !strings.ContainsAny(raw, " \t/") {
		return ScanKey{Kind: "email", Value: lower}, true
	}
	return ScanKey{}, false
}

// NormalizeSlug reduces a LinkedIn slug (or the tail of a profile URL) to the
// form used as the match key on both sides: lowercased, unescaped, no query.
func NormalizeSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.IndexAny(s, "?#/"); i >= 0 {
		s = s[:i]
	}
	if u, err := url.PathUnescape(s); err == nil {
		s = u
	}
	return strings.TrimSpace(s)
}

// SlugFromURL extracts the match key from a normalized LinkedIn URL as
// produced by pkg.ExtractLinkedIn.
func SlugFromURL(linkedinURL string) string {
	const prefix = "https://www.linkedin.com/in/"
	if !strings.HasPrefix(linkedinURL, prefix) {
		return ""
	}
	return NormalizeSlug(strings.TrimPrefix(linkedinURL, prefix))
}
