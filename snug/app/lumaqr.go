package app

import (
	"net/http"
	"regexp"

	qrcode "github.com/skip2/go-qrcode"
)

// handleLumaQR is a guest's own Luma check-in QR (what their ticket shows),
// for the Printed view's Check in on Luma: scanned with the Luma app on a
// phone, it checks them in on Luma. Luma's public API can't.
func (s *Server) handleLumaQR(w http.ResponseWriter, r *http.Request) {
	event, key := r.PathValue("eventID"), r.PathValue("key")
	if !lumaEvent.MatchString(event) || !guestKey.MatchString(key) {
		http.NotFound(w, r)
		return
	}
	png, err := qrcode.Encode("https://luma.com/check-in/"+event+"?pk="+key, qrcode.Medium, 512)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Write(png)
}

var (
	lumaEvent = regexp.MustCompile(`^evt-[A-Za-z0-9]{6,32}$`)
	guestKey  = regexp.MustCompile(`^g-[A-Za-z0-9]{4,40}$`)
)
