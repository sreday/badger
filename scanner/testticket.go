package main

import (
	"encoding/base64"
	"fmt"
	"html"
	"net/http"
	"regexp"

	qrcode "github.com/skip2/go-qrcode"
)

// The test ticket: a fake Luma ticket QR on a phone, to try the USB ticket
// scanner at the desk without anyone's real ticket. Badger's event page has
// a Test ticket button showing a QR that opens this page on a phone; held
// to the scanner, it prints the test badge (Mark's, as on the sponsor
// dashboards). It's in Luma's own check-in format, so it goes through the
// same path as a real ticket, and its key matches no guest.
const testTicketKey = "g-badgertest"

var lumaEventID = regexp.MustCompile(`^evt-[A-Za-z0-9]{6,32}$`)

func testTicketURL(event string) string {
	return "https://luma.com/check-in/" + event + "?pk=" + testTicketKey
}

// handleTestTicket is the phone page: public (the phone isn't signed in)
// and harmless, it only shows a QR that prints the test badge. Everything
// is in the page, so it stays up with no signal.
func (s *Server) handleTestTicket(w http.ResponseWriter, r *http.Request) {
	event := r.PathValue("event")
	if !lumaEventID.MatchString(event) {
		http.NotFound(w, r)
		return
	}
	png, err := qrcode.Encode(testTicketURL(event), qrcode.Medium, 640)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex"><title>Test ticket</title>
<style>
* { box-sizing: border-box; margin: 0; }
body { min-height: 100vh; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 18px;
       padding: 24px 16px; background: #fff; color: #141c25; text-align: center;
       font: 600 16px/1.4 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif; }
.tag { padding: 5px 14px; border-radius: 999px; background: #5a3f75; color: #fff; font-size: 13px; font-weight: 800; letter-spacing: .08em; text-transform: uppercase; }
img { width: min(80vw, 360px); height: auto; image-rendering: pixelated; }
h1 { font-size: 22px; }
p { color: #4b5a6b; font-weight: 500; font-size: 14px; max-width: 340px; }
</style></head><body>
<span class="tag">Test ticket</span>
<img src="data:image/png;base64,%s" alt="Test ticket QR code">
<h1>Mark Pawlikowski</h1>
<p>Hold this to the QR scanner at the badge desk: Badger prints the test badge. Turn the screen brightness up if it doesn't read.</p>
<p style="font-size:12px">%s</p>
</body></html>`, base64.StdEncoding.EncodeToString(png), html.EscapeString(event))
}

// handleTestTicketLink is the QR Badger's Test ticket button shows: the
// phone page's address, for the phone camera.
func (s *Server) handleTestTicketLink(w http.ResponseWriter, r *http.Request) {
	event := r.PathValue("event")
	if !lumaEventID.MatchString(event) {
		http.NotFound(w, r)
		return
	}
	png, err := qrcode.Encode(s.baseURL(r)+"/t/"+event, qrcode.Medium, 512)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(png)
}
