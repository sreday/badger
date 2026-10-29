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
// right), with what it takes to get online on the left and, where a phone
// can join from one, a QR code with a WiFi sign in its middle.

// WifiKind is the kind of network a card is for.
type WifiKind string

const (
	WifiPassword   WifiKind = "password"   // a password (WPA/WPA2/WPA3)
	WifiOpen       WifiKind = "open"       // no password
	WifiSignIn     WifiKind = "signin"     // a browser sign-in page after joining (open, or with a password)
	WifiEnterprise WifiKind = "enterprise" // a username and password (WPA2-Enterprise, eduroam): no QR
)

// WifiCard is what goes on a WiFi card.
type WifiCard struct {
	Kind     WifiKind
	SSID     string
	Password string
	Username string // WifiEnterprise
	SignIn   string // WifiSignIn: what the sign-in page asks for, e.g. "Access code SREDAY26"
}

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

// wifiLabelSize is the labels' size ("Network", "Password").
const wifiLabelSize = 34

// wifiRow is one label and its value on the card.
type wifiRow struct {
	label, value string
	plain        bool // a sentence, not a name or password: regular weight, smaller
	anywhere     bool // may break inside a word (passwords and usernames)
}

// rows is what the card says, top down.
func (c WifiCard) rows() []wifiRow {
	rows := []wifiRow{{label: "Network", value: c.SSID}}
	switch c.Kind {
	case WifiOpen:
		rows = append(rows, wifiRow{label: "Password", value: "No password", plain: true})
	case WifiSignIn:
		if c.Password != "" {
			rows = append(rows, wifiRow{label: "Password", value: c.Password, anywhere: true})
		}
		signIn := strings.TrimSpace(c.SignIn)
		if signIn == "" {
			signIn = "A sign-in page opens in your browser after you join."
		}
		rows = append(rows, wifiRow{label: "Sign-in", value: signIn, plain: true})
	case WifiEnterprise:
		rows = append(rows, wifiRow{label: "Username", value: c.Username, anywhere: true},
			wifiRow{label: "Password", value: c.Password, anywhere: true})
	default:
		rows = append(rows, wifiRow{label: "Password", value: c.Password, anywhere: true})
	}
	return rows
}

// qr is the card's join-the-network QR text, or "" when phones can't join
// this kind of network from a QR code.
func (c WifiCard) qr() string {
	switch c.Kind {
	case WifiEnterprise:
		return ""
	case WifiOpen:
		return WifiQR(c.SSID, "", "")
	default:
		return WifiQR(c.SSID, c.Password, "WPA")
	}
}

// GenerateWifiBadge draws a WiFi card.
func GenerateWifiBadge(c WifiCard) (image.Image, error) {
	c.SSID = strings.TrimSpace(c.SSID)
	if c.SSID == "" {
		return nil, fmt.Errorf("no network name")
	}
	if c.Kind == "" {
		c.Kind = WifiPassword
	}
	switch c.Kind {
	case WifiPassword:
		if c.Password == "" {
			return nil, fmt.Errorf("no password (choose an open network if it has none)")
		}
	case WifiEnterprise:
		if strings.TrimSpace(c.Username) == "" || c.Password == "" {
			return nil, fmt.Errorf("a username and a password are needed")
		}
	case WifiOpen, WifiSignIn:
	default:
		return nil, fmt.Errorf("unknown network type %q", c.Kind)
	}
	if err := loadV2Fonts(); err != nil {
		return nil, err
	}
	v2FaceMu.Lock()
	defer v2FaceMu.Unlock()

	dc, tileX, tileY, tile := badgeCanvas()
	qrText := c.qr()
	width := tileX - badgeMargin - 40
	if qrText == "" {
		width = float64(badgeWidth) - 2*badgeMargin
	}
	maxH := float64(badgeHeight) - tileY - badgeMargin*0.6

	// The values as big as they fit together. Names break between words
	// first; passwords and usernames may break anywhere (no spaces to
	// break at); a sentence is regular and smaller.
	type line struct {
		text  string
		bold  bool
		size  int
		alpha float64
		label bool
	}
	var lines []line
	fits := false
	for _, anywhere := range []bool{false, true} {
		for size := 84; size >= 20 && !fits; size -= 4 {
			lines = lines[:0]
			ok, height := true, 0.0
			for _, r := range c.rows() {
				vSize, maxLines := size, 2
				var wrapped []string
				switch {
				case r.plain:
					vSize, maxLines = max(wifiLabelSize, size*3/4), 3
					wrapped = wrapText(dc, r.value, false, vSize, width, maxLines)
				case r.anywhere:
					maxLines = 3
					wrapped = wrapTokens(dc, charTokens(r.value), true, vSize, width, maxLines)
				default:
					wrapped = wrapText(dc, r.value, true, vSize, width, maxLines)
					if wrapped == nil && anywhere {
						wrapped = wrapTokens(dc, charTokens(r.value), true, vSize, width, maxLines)
					}
				}
				if wrapped == nil {
					ok = false
					break
				}
				lines = append(lines, line{r.label, false, wifiLabelSize, .75, true})
				for _, t := range wrapped {
					lines = append(lines, line{t, !r.plain, vSize, 1, false})
				}
				height += wifiLabelSize*1.5 + float64(len(wrapped)*vSize)*1.15 + float64(vSize)*0.2
			}
			fits = ok && height <= maxH
		}
	}

	// From the QR's top edge down, each label close above its value.
	y := tileY
	for i, l := range lines {
		if i > 0 {
			prev := lines[i-1]
			switch {
			case l.label: // a label after a value
				y += float64(prev.size)*0.1 + float64(l.size)*0.9
			case prev.label: // a value after its label
				y += float64(prev.size) * 0.55
			default: // the next line of the same value: baselines 1.15 apart
				y += float64(prev.size)*1.15 - capHeight(prev.bold, prev.size)
			}
		}
		dc.SetFontFace(v2Face(l.bold, l.size))
		dc.SetRGBA(1, 1, 1, l.alpha)
		dc.DrawString(l.text, badgeMargin, y+capHeight(l.bold, l.size))
		y += capHeight(l.bold, l.size)
	}

	if qrText != "" {
		if err := drawQRTile(dc, qrText, tileX, tileY, tile, drawWifiLogo); err != nil {
			return nil, fmt.Errorf("WiFi QR: %w", err)
		}
	}
	return finishBadge(dc), nil
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
