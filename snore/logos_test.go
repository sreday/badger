package snore

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
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

func TestCutWhite(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 20, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 20; x++ {
			c := color.NRGBA{255, 255, 255, 255}
			ring := x >= 5 && x < 15 && y >= 5 && y < 15 && !(x >= 8 && x < 12 && y >= 8 && y < 12)
			if ring {
				c = color.NRGBA{20, 40, 90, 255}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	cut, ok := cutWhite(buf.Bytes())
	if !ok {
		t.Fatal("a logo on a white box was left alone")
	}
	got, err := png.Decode(bytes.NewReader(cut))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, a := got.At(0, 0).RGBA(); a != 0 {
		t.Fatal("the box is still there")
	}
	if _, _, _, a := got.At(6, 6).RGBA(); a == 0 {
		t.Fatal("the drawing went with the box")
	}
	if _, _, _, a := got.At(10, 10).RGBA(); a == 0 {
		t.Fatal("white inside the drawing must stay")
	}
	// No white box around it: nothing to cut.
	img.SetNRGBA(0, 0, color.NRGBA{20, 40, 90, 255})
	buf.Reset()
	png.Encode(&buf, img)
	if _, ok := cutWhite(buf.Bytes()); ok {
		t.Fatal("a picture without a white box was cut")
	}
}
