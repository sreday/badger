package snore

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestPickLogos(t *testing.T) {
	page := `<html><head><link rel="icon" href="/favicon-32.png"><link rel="apple-touch-icon" href="/touch.png">
	<link rel="icon" type="image/svg+xml" href="/icon.svg"><link rel="stylesheet" href="/a.css"></head><body>
	<img class="customer-logo" src="/img/otherco.svg" alt="Otherco">
	<img class="hero" src="/img/hero.png" alt="A room">
	<img src="/img/example-logo.svg" alt="Example">
	</body></html>`
	got := pickLogos(page, "https://example.ai/", "example.ai")
	want := []string{"https://example.ai/img/example-logo.svg", "https://example.ai/img/otherco.svg", "https://example.ai/icon.svg", "https://example.ai/touch.png"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("place %d: got %v, want %v", i, got, want)
		}
	}
}

func TestLogoKindAndKeep(t *testing.T) {
	if ext, light := logoKind([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><path fill="#fff" d="M0 0h9v9z"/></svg>`)); ext != "svg" || !light {
		t.Fatalf("a white drawing: %q %v", ext, light)
	}
	if ext, light := logoKind([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><path fill="#123456" d="M0 0h9v9z"/></svg>`)); ext != "svg" || light {
		t.Fatalf("a dark drawing: %q %v", ext, light)
	}
	if ext, _ := logoKind([]byte("<html><body>Not found, sorry. Nothing to see on this page at all.</body></html>")); ext != "" {
		t.Fatalf("a web page is no logo: %q", ext)
	}

	s, _ := sponsorTestServer(t)
	svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 9 9"><path fill="#123456" d="M0 0h9v9H0z"/></svg>`
	s.raw = func(u string, _ http.Header) (int, []byte, error) {
		switch u {
		case "https://example.ai/":
			return http.StatusOK, []byte(`<img src="/logo.svg" alt="Example logo">`), nil
		case "https://example.ai/logo.svg":
			return http.StatusOK, []byte(svg), nil
		}
		return http.StatusNotFound, nil, nil
	}
	s.pol.min = 0
	s.wantLogo("example.ai")
	file, dark := s.logoOf("example.ai")
	if file != "example.ai.svg" || dark {
		t.Fatalf("logoOf = %q %v", file, dark)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "logos", file)); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.logoOf("../etc"); f != "" {
		t.Fatal("a path is no domain")
	}
}
