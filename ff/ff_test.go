package ff

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNoUpstreamIsNotFound(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler("").ServeHTTP(rec, httptest.NewRequest("GET", "/x/karta/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", rec.Code)
	}
}

func TestForwardsPathAndKeepsRedirectsHere(t *testing.T) {
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" {
			t.Error("cookie was passed on")
		}
		if r.URL.Path == "/folder/" {
			http.Redirect(w, r, "/folder/karta/", http.StatusFound)
			return
		}
		io.WriteString(w, "path="+r.URL.Path)
	}))
	defer app.Close()
	h := http.StripPrefix("/ff", Handler(app.URL))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/ff/folder/karta/app.js", nil)
	req.Header.Set("Cookie", "admin=1")
	h.ServeHTTP(rec, req)
	if body := rec.Body.String(); body != "path=/folder/karta/app.js" {
		t.Fatalf("body %q", body)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/ff/folder/", nil))
	if loc := rec.Header().Get("Location"); loc != "/ff/folder/karta/" {
		t.Fatalf("redirect to %q", loc)
	}
}
