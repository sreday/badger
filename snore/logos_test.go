package snore

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPickLogos(t *testing.T) {
	page := `<html><head><link rel="icon" href="/favicon-32.png"><link rel="apple-touch-icon" href="/touch.png">
	<link rel="icon" type="image/svg+xml" href="/icon.svg"><link rel="stylesheet" href="/a.css"></head><body>
	<a href="/pricing"><img src="/img/pricing.png" alt="Pricing"></a>
	<a class="brand" href="https://www.example.ai/"><img src="/img/mark.svg" alt=""></a>
	<img class="customer-logo" src="https://cdn.example.ai/img/otherco-logo.svg" alt="Otherco logo">
	<img class="hero" src="/img/hero.png" alt="A room">
	</body></html>`
	var got []string
	for _, c := range pickLogos(page, "https://example.ai/", "example.ai") {
		got = append(got, c.url)
	}
	// The picture in the link home, then the icons. A customer's logo never.
	want := []string{"https://example.ai/img/mark.svg", "https://example.ai/touch.png", "https://example.ai/icon.svg", "https://example.ai/favicon-32.png"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("place %d: got %v, want %v", i, got, want)
		}
	}
	// A logo drawn into the page itself.
	drawn := `<a href="/"><svg viewBox="0 0 90 20"><path fill="#123456" d="` + strings.Repeat("M0 0h9v9z", 30) + `"/></svg></a>`
	c := pickLogos(drawn, "https://example.ai/", "example.ai")
	if len(c) == 0 || !bytes.Contains(c[0].inline, []byte(`xmlns="http://www.w3.org/2000/svg"`)) {
		t.Fatalf("a drawn logo: %v", c)
	}
}

func TestPaleLogos(t *testing.T) {
	if !lightSVG(`<svg><path fill="#F1EAE0" d="M0 0"/><path fill="#F1EAE0" d="M1 1"/><rect fill="none"/></svg>`) {
		t.Fatal("an off-white drawing needs the dark plate")
	}
	if lightSVG(`<svg><path fill="white" d="M0 0"/><path fill="#7b3ff2" d="M1 1"/><path fill="#111" d="M1 1"/></svg>`) {
		t.Fatal("a drawing that is mostly dark stays on white")
	}
	img := image.NewNRGBA(image.Rect(0, 0, 40, 10))
	for x := 0; x < 40; x++ {
		for y := 0; y < 10; y++ {
			c := color.NRGBA{255, 255, 255, 255}
			if x < 8 {
				c = color.NRGBA{120, 60, 240, 255}
			}
			if y == 0 {
				c = color.NRGBA{0, 0, 0, 0}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	if !lightPNG(buf.Bytes()) {
		t.Fatal("white letters beside a small coloured mark need the dark plate")
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
			return http.StatusOK, []byte(`<a href="/"><img src="/logo.svg" alt="Example logo"></a>`), nil
		case "https://example.ai/logo.svg":
			return http.StatusOK, []byte(svg), nil
		}
		return http.StatusNotFound, nil, nil
	}
	s.pol.min = 0
	s.wantLogo("example.ai")
	file := s.logoOf("example.ai")
	if file != "example.ai.svg" {
		t.Fatalf("logoOf = %q", file)
	}
	if _, err := os.Stat(filepath.Join(s.dir, logoDir, file)); err != nil {
		t.Fatal(err)
	}
	if f := s.logoOf("../etc"); f != "" {
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

func TestTrimEdges(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 100, 100))
	for x := 40; x < 60; x++ {
		for y := 45; y < 55; y++ {
			img.SetNRGBA(x, y, color.NRGBA{10, 20, 30, 255})
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	out, ok := trimEdges(buf.Bytes())
	if !ok {
		t.Fatal("a small drawing on a big sheet is cut to size")
	}
	got, _, err := image.Decode(bytes.NewReader(out))
	if err != nil || got.Bounds().Dx() != 20 || got.Bounds().Dy() != 10 {
		t.Fatalf("cut to %v, %v", got.Bounds(), err)
	}
	if _, ok := trimEdges(out); ok {
		t.Fatal("nothing left to cut")
	}
}

func TestLogoForWhitePage(t *testing.T) {
	white := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 9 9"><path fill="#fff" d="M0 0h9v9H0z"/><path fill="#fff" d="M1 1h2v2H1z"/><path fill="#7b3ff2" d="M0 0h2v2H0z"/></svg>`
	dark := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 9 9"><path fill="#123456" d="M0 0h9v9H0z"/></svg>`
	site := func(page string) *Server {
		s, _ := sponsorTestServer(t)
		s.raw = func(u string, _ http.Header) (int, []byte, error) {
			switch u {
			case "https://example.ai/":
				return http.StatusOK, []byte(page), nil
			case "https://example.ai/white.svg":
				return http.StatusOK, []byte(white), nil
			case "https://example.ai/dark.svg", "https://example.ai/icon.svg":
				return http.StatusOK, []byte(dark), nil
			}
			return http.StatusNotFound, nil, nil
		}
		s.pol.min = 0
		s.wantLogo("example.ai")
		return s
	}
	read := func(s *Server) string {
		data, err := os.ReadFile(filepath.Join(s.dir, logoDir, s.logoOf("example.ai")))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	// Both versions in the header: the one for a white page is kept.
	if got := read(site(`<a href="/"><img src="/white.svg"></a><a href="/"><img src="/dark.svg"></a>`)); got != dark {
		t.Fatalf("kept %s", got)
	}
	// Only a white one: its white goes dark, its colour stays, and it
	// still beats the icon.
	got := read(site(`<link rel="icon" href="/icon.svg"><a href="/"><img src="/white.svg"></a>`))
	if strings.Contains(got, "#fff") || !strings.Contains(got, logoInk) || !strings.Contains(got, "#7b3ff2") {
		t.Fatalf("repainted to %s", got)
	}
	// A white picture.
	img := image.NewNRGBA(image.Rect(0, 0, 10, 10))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 255, 255, 255, 200
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	out, ext, ok := inkDark(buf.Bytes(), "png")
	if !ok || ext != "png" || lightPNG(out) {
		t.Fatal("a white picture is repainted dark")
	}
}
