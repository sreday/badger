package snore

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"testing"
)

func testPNG(w, h int) []byte {
	var b bytes.Buffer
	png.Encode(&b, image.NewGray(image.Rect(0, 0, w, h)))
	return b.Bytes()
}

func TestOnlySquarePhotosAreKept(t *testing.T) {
	s, _ := testServer(t)
	if s.keepPhoto("https://example.org/banner.png") != "" {
		t.Fatal("a banner should not be kept")
	}
	if s.keepPhoto("https://example.org/face.png") == "" {
		t.Fatal("a square headshot should be kept")
	}
	s.mu.Lock()
	s.st.Cards["sre"] = []*Card{{Key: "wide one", Name: "Wide One", PhotoBox: "0123456789abcdef.png"}}
	s.mu.Unlock()
	writeFile := s.dir + "/photos/0123456789abcdef.png"
	if err := writeBytes(writeFile, testPNG(300, 120)); err != nil {
		t.Fatal(err)
	}
	s.dropNonSquare()
	if s.st.Cards["sre"][0].PhotoBox != "" {
		t.Fatal("a kept banner should be dropped")
	}
}

func TestOnlyRealConferences(t *testing.T) {
	for name, want := range map[string]bool{
		"PlatformCon London 2026":               true,
		"CNCF-hosted co-located events NA 2025": true,
		"Some-listed conference 2025":           false,
		"Conference 2025":                       false,
		"Unknown event 2024":                    false,
		"KubeCon + CloudNativeCon Europe 2025":  true,
	} {
		if realConference(name) != want {
			t.Errorf("realConference(%q) = %v", name, !want)
		}
	}
}

func writeBytes(path string, b []byte) error { return os.WriteFile(path, b, 0o600) }
