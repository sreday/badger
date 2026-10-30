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
		// The thin white margin, then the black.
		if !isWhite(img.At(0, 300)) || !isWhite(img.At(500, 0)) {
			t.Errorf("%s: no white margin", name)
		}
		if isWhite(img.At(12, 300)) || isWhite(img.At(500, 12)) {
			t.Errorf("%s: black should start inside the margin", name)
		}
		// The QR tile: white, with black modules inside.
		if d.LinkedIn != "" {
			black := 0
			for y := 140; y < 460; y += 4 {
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
	// Between the text column and the QR tile the background shows through:
	// no white text pixels in the gutter.
	for y := 20; y < 580; y++ {
		for x := 566; x < 590; x++ {
			if isWhite(img.At(x, y)) {
				t.Fatalf("text runs into the QR gutter at %d,%d", x, y)
			}
		}
	}
}

func TestBadgeV2WithoutLinkedInHasNoQR(t *testing.T) {
	loadTestTemplate(t)
	img, err := GenerateBadgeV2(BadgeData{Name: "Jo Bloggs", Company: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	// Where the QR would be: only the black background.
	for y := 140; y < 460; y += 6 {
		for x := 620; x < 940; x += 6 {
			r, g, b, _ := img.At(x, y).RGBA()
			if r>>8 != 0 || g>>8 != 0 || b>>8 != 0 {
				t.Fatalf("something at %d,%d: %v", x, y, img.At(x, y))
			}
		}
	}
}

func TestExtractLinkedInTyped(t *testing.T) {
	for in, want := range map[string]string{"in/jo": "https://www.linkedin.com/in/jo", "linkedin.com/in/jo/": "https://www.linkedin.com/in/jo", "": "", " none ": ""} {
		if got := ExtractLinkedIn(map[string]string{"linkedin": in}); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestWifiQR(t *testing.T) {
	bs := string(rune(92)) // a backslash
	tricky := "p:a,s" + `"` + "s" + bs
	want := "WIFI:T:WPA;S:Cafe" + bs + "; Guest;P:p" + bs + ":a" + bs + ",s" + bs + `"` + "s" + bs + bs + ";;"
	for _, c := range []struct{ ssid, pass, auth, want string }{
		{"SREday", "secret123", "", "WIFI:T:WPA;S:SREday;P:secret123;;"},
		{"Cafe; Guest", tricky, "WPA", want},
		{"Open", "", "WPA", "WIFI:T:nopass;S:Open;P:;;"},
	} {
		if got := WifiQR(c.ssid, c.pass, c.auth); got != c.want {
			t.Errorf("WifiQR(%q, %q) = %q, want %q", c.ssid, c.pass, got, c.want)
		}
	}
}

func TestWifiBadge(t *testing.T) {
	loadTestTemplate(t)
	for _, bad := range []WifiCard{
		{SSID: " ", Password: "x"},
		{SSID: "Net"}, // password type without a password
		{Kind: WifiEnterprise, SSID: "Corp", Password: "x"},
		{Kind: "carrier-pigeon", SSID: "Net"},
	} {
		if _, err := GenerateWifiBadge(bad); err == nil {
			t.Errorf("%+v should fail", bad)
		}
	}
	cases := map[string]WifiCard{
		"short":      {SSID: "SREday", Password: "badger2026"},
		"long":       {SSID: "Microsoft Reactor Guest Network 5GHz", Password: "Th1sIsAVeryLongPasswordWithNoSpaces!2026#Reactor"},
		"open":       {Kind: WifiOpen, SSID: "Venue-Free-WiFi"},
		"tricky":     {SSID: "IlO0 l1", Password: "Il1O0oQ-lI1"},
		"signin":     {Kind: WifiSignIn, SSID: "Hotel Guest", SignIn: "Access code SREDAY26"},
		"signin-pw":  {Kind: WifiSignIn, SSID: "Conference", Password: "welcome2026", SignIn: "Enter your email and accept the terms"},
		"signin-nil": {Kind: WifiSignIn, SSID: "Airport Free WiFi"},
		"enterprise": {Kind: WifiEnterprise, SSID: "eduroam", Username: "guest-sreday@university.ac.uk", Password: "Badger!2026"},
	}
	for name, c := range cases {
		img, err := GenerateWifiBadge(c)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if b := img.Bounds(); b.Dx() != 1000 || b.Dy() != 600 || !isWhite(img.At(0, 0)) {
			t.Errorf("%s: bad card", name)
		}
		// A QR tile in the middle right, except for username & password.
		hasQR := c.Kind != WifiEnterprise
		tileThere := isWhite(img.At(610, 127)) && isWhite(img.At(956, 127)) && isWhite(img.At(610, 473)) && isWhite(img.At(956, 473))
		if tileThere != hasQR {
			t.Errorf("%s: QR tile there = %v, want %v", name, tileThere, hasQR)
		}
		if dir := os.Getenv("BADGE_PREVIEW_DIR"); dir != "" {
			png, _ := EncodeBadgePNG(img)
			os.WriteFile(filepath.Join(dir, "wifi-"+name+".png"), png, 0644)
		}
	}
}
