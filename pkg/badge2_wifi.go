package pkg

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"

	"github.com/fogleman/gg"
)

// The WiFi card: the 2.0 badge's look (purple, rounded, QR in the middle
// right), with the network name and password on the left and a QR code
// that joins the network, a WiFi sign in its middle.

// WifiQR is the text of a join-the-network QR code (the format phone
// cameras read). With no password the network is open.
func WifiQR(ssid, password, auth string) string {
	esc := strings.NewReplacer(`\`, `\\`, `;`, `\;`, `,`, `\,`, `"`, `\"`, `:`, `\:`)
	if password == "" {
		auth = "nopass"
	} else if auth == "" {
		auth = "WPA"
	}
	return fmt.Sprintf("WIFI:T:%s;S:%s;P:%s;;", auth, esc.Replace(ssid), esc.Replace(password))
}

// wifiLabelSize is the "Network" / "Password" labels' size.
const wifiLabelSize = 34

// GenerateWifiBadge draws the WiFi card. auth is WPA (the default), WEP or
// nopass; with no password the card says so and the QR joins an open
// network.
func GenerateWifiBadge(ssid, password, auth string) (image.Image, error) {
	ssid = strings.TrimSpace(ssid)
	if ssid == "" {
		return nil, fmt.Errorf("no network name")
	}
	if err := loadV2Fonts(); err != nil {
		return nil, err
	}
	v2FaceMu.Lock()
	defer v2FaceMu.Unlock()

	dc, tileX, tileY, tile := badgeCanvas()
	width := tileX - badgeMargin - 40
	maxH := float64(badgeHeight) - tileY - badgeMargin*0.6

	// Network and password as big as they fit together: the name on up to
	// two lines, the password on up to three (it may break anywhere: a
	// password has no spaces to break at).
	type row struct {
		text  string
		bold  bool
		size  int
		alpha float64
	}
	var rows []row
	var height float64
	fits := false
	// The name breaks between words; only a name that can't fit that way
	// at any size breaks inside a word.
	for _, anywhere := range []bool{false, true} {
		for size := 84; size >= 20 && !fits; size -= 4 {
			name := wrapText(dc, ssid, true, size, width, 2)
			if anywhere {
				name = wrapTokens(dc, charTokens(ssid), true, size, width, 2)
			}
			pass := []string{"No password"}
			passBold := false
			if password != "" {
				pass, passBold = wrapTokens(dc, charTokens(password), true, size, width, 3), true
			}
			if name == nil || pass == nil {
				continue
			}
			rows = rows[:0]
			rows = append(rows, row{"Network", false, wifiLabelSize, .75})
			for _, l := range name {
				rows = append(rows, row{l, true, size, 1})
			}
			rows = append(rows, row{"Password", false, wifiLabelSize, .75})
			passSize := size
			if !passBold {
				passSize = size * 3 / 4
			}
			for _, l := range pass {
				rows = append(rows, row{l, passBold, passSize, 1})
			}
			height = wifiHeight(len(name), size, len(pass), passSize)
			fits = height <= maxH
		}
	}

	// From the QR's top edge down, label close above its value.
	y := tileY
	for i, r := range rows {
		if i > 0 {
			prev := rows[i-1]
			switch {
			case r.size == wifiLabelSize && !r.bold: // a label after a value
				y += float64(prev.size)*0.1 + float64(r.size)*0.9
			case prev.size == wifiLabelSize && !prev.bold: // a value after its label
				y += float64(prev.size) * 0.55
			default: // the next line of the same value
				y += float64(prev.size) * 0.1
			}
		}
		dc.SetFontFace(v2Face(r.bold, r.size))
		dc.SetRGBA(1, 1, 1, r.alpha)
		dc.DrawString(r.text, badgeMargin, y+capHeight(r.bold, r.size))
		y += capHeight(r.bold, r.size)
	}

	if err := drawQRTile(dc, WifiQR(ssid, password, auth), tileX, tileY, tile, drawWifiLogo); err != nil {
		return nil, fmt.Errorf("WiFi QR: %w", err)
	}
	return finishBadge(dc), nil
}

// wifiHeight is roughly how tall the card's text is: two labels, the name
// and the password lines.
func wifiHeight(nameLines, size, passLines, passSize int) float64 {
	return 2*wifiLabelSize*1.5 + float64(nameLines*size)*1.1 + float64(passLines*passSize)*1.1 + float64(size)*0.4
}

// drawWifiLogo draws a WiFi sign (a dot and three arcs, white on a black
// rounded square) centred on cx, cy, on a thin white ring: like the
// LinkedIn mark on a person's badge.
func drawWifiLogo(dc *gg.Context, cx, cy, size float64, c color.Color) {
	pad := size * badgeLogoPad
	dc.SetColor(white)
	dc.DrawRoundedRectangle(cx-size/2-pad, cy-size/2-pad, size+2*pad, size+2*pad, size*0.18)
	dc.Fill()
	dc.SetColor(c)
	dc.DrawRoundedRectangle(cx-size/2, cy-size/2, size, size, size*0.07)
	dc.Fill()

	u := size / 24 // drawn on a 24x24 grid, like the LinkedIn mark
	ox, oy := cx-size/2, cy-size/2
	dc.SetColor(white)
	dc.DrawCircle(ox+12*u, oy+18.2*u, 1.9*u)
	dc.Fill()
	dc.SetLineWidth(2.3 * u)
	dc.SetLineCap(gg.LineCapRound)
	for _, r := range []float64{4.6, 8.6, 12.6} {
		dc.NewSubPath()
		dc.DrawArc(ox+12*u, oy+18.2*u, r*u, math.Pi*1.25, math.Pi*1.75)
		dc.Stroke()
	}
}
