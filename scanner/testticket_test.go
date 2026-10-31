package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTestTicket(t *testing.T) {
	s := &Server{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /t/{event}", s.handleTestTicket)
	mux.HandleFunc("GET /t/{event}/link.png", s.handleTestTicketLink)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	for path, want := range map[string]int{
		"/t/evt-qQfoSkUnT6B5o11":          200,
		"/t/evt-qQfoSkUnT6B5o11/link.png": 200,
		"/t/not-an-event":                 404,
		"/t/evt-x%22%3E%3Cscript%3E":      404,
	} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s = %d, want %d", path, resp.StatusCode, want)
		}
		if want == 200 && strings.HasSuffix(path, "11") && !strings.Contains(string(body), "data:image/png;base64,") {
			t.Errorf("%s: no QR in the page", path)
		}
	}
	if got := testTicketURL("evt-abc123"); got != "https://luma.com/check-in/evt-abc123?pk=g-badgertest" {
		t.Errorf("test ticket = %q", got)
	}
}
