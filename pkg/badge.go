package pkg

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"log"
	"os"
	"strings"

	"github.com/fogleman/gg"
	qrcode "github.com/skip2/go-qrcode"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/opentype"
)

var (
	templateImage image.Image
	fontData      []byte
	parsedFont    *opentype.Font
)

func InitBadgeAssets(templatePNG []byte, fontPath string) error {
	// Load template image
	var err error
	templateImage, err = png.Decode(bytes.NewReader(templatePNG))
	if err != nil {
		return fmt.Errorf("decoding template: %w", err)
	}

	// Load font
	if fontPath != "" {
		fontData, err = os.ReadFile(fontPath)
		if err != nil {
			return fmt.Errorf("reading font %s: %w", fontPath, err)
		}
		log.Printf("Using font from %s", fontPath)
	} else {
		fontData = gomono.TTF
		log.Printf("Using embedded Go Mono font")
	}

	parsedFont, err = opentype.Parse(fontData)
	if err != nil {
		return fmt.Errorf("parsing font: %w", err)
	}

	return nil
}

func makeFontFace(size float64) font.Face {
	face, err := opentype.NewFace(parsedFont, &opentype.FaceOptions{
		Size:    size,
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		log.Printf("Error creating font face at size %.0f: %v", size, err)
		face, _ = opentype.NewFace(parsedFont, &opentype.FaceOptions{
			Size: 20,
			DPI:  72,
		})
	}
	return face
}

func findFontSize(text string, imgWidth int, targetWidthRatio float64) float64 {
	if text == "" {
		return 20
	}
	testSize := 100.0
	face := makeFontFace(testSize)
	defer face.Close()

	dc := gg.NewContext(imgWidth, 100)
	dc.SetFontFace(face)
	w, _ := dc.MeasureString(text)
	if w == 0 {
		return 20
	}

	estimated := testSize / (w / float64(imgWidth)) * targetWidthRatio
	return estimated
}

func Caps(text string) string {
	words := strings.Fields(text)
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
		}
	}
	return strings.Join(words, " ")
}

func ExtractLinkedIn(data map[string]string) string {
	var linkedin string
	for key, val := range data {
		if strings.Contains(strings.ToLower(key), "linkedin") {
			linkedin = strings.ToLower(val)
			break
		}
	}

	if linkedin == "" || linkedin == "none" {
		return ""
	}

	if !strings.HasPrefix(linkedin, "https://www.linkedin.com/") && !strings.HasPrefix(linkedin, "https://") {
		if !strings.Contains(linkedin, "/") {
			linkedin = "https://www.linkedin.com/in/" + linkedin
		} else {
			linkedin = "https://" + linkedin
		}
	} else if !strings.HasPrefix(linkedin, "https://") {
		linkedin = "https://" + linkedin
	}

	return linkedin
}

func GenerateBadge(data BadgeData, wifiID, wifiPassword, wifiAuth string) (image.Image, error) {
	bounds := templateImage.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()

	dc := gg.NewContextForImage(templateImage)

	// Draw text fields
	fields := []struct {
		x, y    float64
		text    string
		maxSize float64
		ratio   float64
	}{
		{100, 100, data.Name, 60, 0.5},
		{100, 200, data.Company, 40, 0.6},
		{100, 300, data.Title, 40, 0.8},
	}

	for _, f := range fields {
		if f.text == "" {
			continue
		}
		size := findFontSize(f.text, w, f.ratio)
		if size > f.maxSize {
			size = f.maxSize
		}
		face := makeFontFace(size)
		dc.SetFontFace(face)
		dc.SetRGB(1, 1, 1) // white
		dc.DrawStringAnchored(f.text, f.x, f.y, 0, 1)
		face.Close()
	}

	// LinkedIn QR code (top-right)
	if data.LinkedIn != "" {
		url := strings.Replace(data.LinkedIn, "https://www.linkedin.com/", "linkedin://", 1)
		qr, err := qrcode.New(url, qrcode.Low)
		if err != nil {
			log.Printf("Error generating LinkedIn QR: %v", err)
		} else {
			qr.DisableBorder = false
			qrImg := qr.Image(150)
			qrBounds := qrImg.Bounds()
			dc.DrawImage(qrImg, w-qrBounds.Dx()-5, 5)
		}
	}

	// WiFi QR code (bottom-left)
	if wifiID != "" {
		hidden := "false"
		wifiStr := fmt.Sprintf("WIFI:T:%s;S:%s;P:%s;H:%s;;", wifiAuth, wifiID, wifiPassword, hidden)
		qr, err := qrcode.New(wifiStr, qrcode.Low)
		if err != nil {
			log.Printf("Error generating WiFi QR: %v", err)
		} else {
			qr.DisableBorder = false
			qrImg := qr.Image(175)
			qrBounds := qrImg.Bounds()
			dc.DrawImage(qrImg, 5, h-qrBounds.Dy()-5)

			// "wifi:" label above QR
			face := makeFontFace(20)
			dc.SetFontFace(face)
			dc.SetRGB(1, 1, 1)
			dc.DrawStringAnchored("wifi:", 5, float64(h-qrBounds.Dy()-30), 0, 1)
			face.Close()
		}
	}

	return dc.Image(), nil
}

func EncodeBadgePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
