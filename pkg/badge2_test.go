package pkg

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"
)

func loadTestTemplate(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "assets", "template.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := InitBadgeAssets(data, ""); err != nil {
		t.Fatal(err)
	}
}

var previewBadges = map[string]BadgeData{
	"short": {Name: "Mark Pawlikowski", Company: "SREday", Title: "Organizer", LinkedIn: "https://www.linkedin.com/in/marek-pawlikowski"},
	"long": {Name: "Maximiliana Konstantinopoulou-Abernathy", Company: "International Business Machines Corporation",
		Title: "Principal Site Reliability Engineer, Observability Platform", LinkedIn: "https://www.linkedin.com/in/someone-with-a-long-handle-123"},
	"flagged": {Name: "Grace Hopper", Company: "403 not found", Title: "403 not found", LinkedIn: "https://www.linkedin.com/in/grace", Flagged: true},
}

func isWhite(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	return r > 0xf000 && g > 0xf000 && b > 0xf000
}

func TestGenerateBadgeV2(t *testing.T) {
	loadTestTemplate(t)
	for name, d := range previewBadges {
		img, err := GenerateBadgeV2(d)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if b := img.Bounds(); b.Dx() != 1000 || b.Dy() != 600 {
			t.Errorf("%s: size %v", name, b)
		}
		// Rounded corners: the very corner is blank paper, the edge
		// midpoints are the background.
		for _, p := range []image.Point{{0, 0}, {999, 0}, {0, 599}, {999, 599}, {3, 3}} {
			if !isWhite(img.At(p.X, p.Y)) {
				t.Errorf("%s: corner %v not white", name, p)
			}
		}
		// The thin white margin, then the purple.
		if !isWhite(img.At(2, 300)) || !isWhite(img.At(500, 2)) {
			t.Errorf("%s: no white margin", name)
		}
		if isWhite(img.At(12, 300)) || isWhite(img.At(500, 12)) {
			t.Errorf("%s: purple should start inside the margin", name)
		}
		// The QR tile: white, with black modules inside.
		if d.LinkedIn != "" {
			black := 0
			for y := 222; y < 540; y += 4 {
				for x := 620; x < 940; x += 4 {
					if r, _, _, _ := img.At(x, y).RGBA(); r < 0x2000 {
						black++
					}
				}
			}
			if black < 1000 {
				t.Errorf("%s: QR looks empty (%d black samples)", name, black)
			}
		}
	}
	if dir := os.Getenv("BADGE_PREVIEW_DIR"); dir != "" {
		for name, d := range previewBadges {
			img, _ := GenerateBadgeV2(d)
			png, _ := EncodeBadgePNG(img)
			os.WriteFile(filepath.Join(dir, "badge-2.0-"+name+".png"), png, 0644)
		}
	}
}

func TestBadgeV2TextStaysLeftOfQR(t *testing.T) {
	loadTestTemplate(t)
	img, err := GenerateBadgeV2(previewBadges["long"])
	if err != nil {
		t.Fatal(err)
	}
	// Beside the QR (bottom right) the background shows through between the
	// text column and the tile: no white text pixels in the gutter. Above
	// the QR the name may run the full width.
	for y := 205; y < 560; y++ {
		for x := 566; x < 590; x++ {
			if isWhite(img.At(x, y)) {
				t.Fatalf("text runs into the QR gutter at %d,%d", x, y)
			}
		}
	}
}
