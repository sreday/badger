package pkg

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A scanned Luma ticket finds its guest by the key in its QR code.
func TestGetGuestsKeepsScanKeys(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "get-guests") {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"entries":[{"guest":{"api_id":"gst-abc123","name":"ada lovelace","email":"ada@example.com",
			"check_in_qr_code":"https://luma.com/check-in/evt-1?pk=g-aaaaaa1",
			"event_tickets":[{"api_id":"tkt-xyz789"}],
			"registration_answers":[{"label":"Job Title","answer":"CTO"},{"label":"Company","answer":"Analytical"}]}}],"next_cursor":""}`))
	}))
	defer ts.Close()
	old := lumaBaseURL
	lumaBaseURL = ts.URL
	defer func() { lumaBaseURL = old }()

	guests, err := GetGuests("evt-1", "key")
	if err != nil {
		t.Fatal(err)
	}
	g := guests["ada@example.com"]
	if g.Key != "g-aaaaaa1" || g.GuestID != "gst-abc123" || len(g.Tickets) != 1 || g.Tickets[0] != "tkt-xyz789" || g.Name != "Ada Lovelace" {
		t.Errorf("guest = %+v", g)
	}
}
