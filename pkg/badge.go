package pkg

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"image"
	"image/png"
	"io"
	"log"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

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
		for _, r := range w {
			words[i] = string(unicode.ToUpper(r)) + strings.ToLower(w[utf8.RuneLen(r):])
			break
		}
	}
	return strings.Join(words, " ")
}

func ExtractLinkedIn(data map[string]string) string {
	var linkedin string
	for key, val := range data {
		if strings.Contains(strings.ToLower(key), "linkedin") {
			linkedin = strings.TrimSpace(val)
			break
		}
	}

	if linkedin == "" || strings.EqualFold(linkedin, "none") {
		return ""
	}

	// Normalize to a full LinkedIn URL regardless of input format.
	lower := strings.ToLower(linkedin)
	lower = strings.TrimPrefix(lower, "https://")
	lower = strings.TrimPrefix(lower, "http://")
	lower = strings.TrimPrefix(lower, "www.linkedin.com")
	lower = strings.TrimPrefix(lower, "linkedin.com")
	lower = strings.TrimLeft(lower, "/")
	lower = strings.TrimPrefix(lower, "in/")
	lower = strings.Trim(lower, "/")

	return "https://www.linkedin.com/in/" + lower
}

// ParseCSV reads a Luma CSV export and returns a map of email -> BadgeData.
// Handles UTF-8 BOM, matches column headers case-insensitively for user-supplied
// fields (Job Title, Company, LinkedIn), and exact-matches Luma system fields
// (name, email).
func ParseCSV(r io.Reader) (map[string]BadgeData, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("reading CSV: %w", err)
	}
	// Strip UTF-8 BOM if present
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})

	csvReader := csv.NewReader(bytes.NewReader(raw))

	headers, err := csvReader.Read()
	if err != nil {
		return nil, fmt.Errorf("reading CSV header: %w", err)
	}

	// Build column index: exact for system fields, case-insensitive for user fields
	nameCol, emailCol, titleCol, companyCol, linkedinCol := -1, -1, -1, -1, -1
	for i, h := range headers {
		switch h {
		case "name":
			nameCol = i
		case "email":
			emailCol = i
		}
		lower := strings.ToLower(strings.TrimSpace(h))
		switch lower {
		case "job title":
			titleCol = i
		case "company":
			companyCol = i
		}
		if strings.Contains(lower, "linkedin") {
			linkedinCol = i
		}
	}

	if nameCol < 0 {
		return nil, fmt.Errorf("CSV missing required column: \"name\"")
	}
	if emailCol < 0 {
		return nil, fmt.Errorf("CSV missing required column: \"email\"")
	}

	getCol := func(record []string, idx int) string {
		if idx < 0 || idx >= len(record) {
			return ""
		}
		return strings.TrimSpace(record[idx])
	}

	db := make(map[string]BadgeData)
	for {
		record, err := csvReader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading CSV row: %w", err)
		}

		const placeholder = "403 not found"
		flagged := false

		name := getCol(record, nameCol)
		if name == "" {
			name = placeholder
			flagged = true
		}
		email := getCol(record, emailCol)
		if email == "" {
			email = placeholder
			flagged = true
		}
		title := getCol(record, titleCol)
		if title == "" {
			title = placeholder
			flagged = true
		}
		company := getCol(record, companyCol)
		if company == "" {
			company = placeholder
			flagged = true
		}

		linkedinRaw := getCol(record, linkedinCol)
		answers := map[string]string{"linkedin": linkedinRaw}

		data := BadgeData{
			Name:     Caps(name),
			Email:    email,
			Title:    title,
			Company:  company,
			LinkedIn: ExtractLinkedIn(answers),
			Flagged:  flagged,
		}
		db[data.Email] = data
	}

	return db, nil
}

func GenerateBadge(data BadgeData, wifiID, wifiPassword, wifiAuth string) (image.Image, error) {
	bounds := templateImage.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()

	dc := gg.NewContextForImage(templateImage)

	const (
		qrSize    = 180 // 30% of 600px badge height
		qrPadding = 6   // custom smaller quiet zone (pixels)
		qrOuter   = qrSize + 2*qrPadding
	)

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
			qr.DisableBorder = true
			qrImg := qr.Image(qrSize)
			qrX := w - qrOuter - 5
			qrY := 5
			dc.SetRGB(1, 1, 1)
			dc.DrawRectangle(float64(qrX), float64(qrY), float64(qrOuter), float64(qrOuter))
			dc.Fill()
			dc.DrawImage(qrImg, qrX+qrPadding, qrY+qrPadding)
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
			qr.DisableBorder = true
			qrImg := qr.Image(qrSize)
			qrX := 5
			qrY := h - qrOuter - 5
			dc.SetRGB(1, 1, 1)
			dc.DrawRectangle(float64(qrX), float64(qrY), float64(qrOuter), float64(qrOuter))
			dc.Fill()
			dc.DrawImage(qrImg, qrX+qrPadding, qrY+qrPadding)

			// "wifi:" label above QR
			face := makeFontFace(20)
			dc.SetFontFace(face)
			dc.SetRGB(1, 1, 1)
			dc.DrawStringAnchored("wifi:", float64(qrX), float64(qrY-25), 0, 1)
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
