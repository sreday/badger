package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A site's list: blank names dropped, a LinkedIn field that isn't a
// LinkedIn link left off the badge; a site that stops answering keeps the
// list it had.
func TestAmbassadors(t *testing.T) {
	up := true
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		w.Write([]byte(`[{"name":" Jo Example ","job":"SRE","company":"Acme","linkedin":"https://www.linkedin.com/in/jo-example/"},
			{"name":"","job":"x"},
			{"name":"Sam Sample","linkedin":"sam at example dot com"}]`))
	}))
	defer ts.Close()
	old := ambassadorSites
	ambassadorSites = []struct{ Brand, URL string }{{"SREday", ts.URL}}
	defer func() { ambassadorSites = old }()

	var c ambassadorCache
	g := c.get(false)
	if len(g) != 1 || len(g[0].People) != 2 || g[0].Error != "" {
		t.Fatalf("got %+v", g)
	}
	if p := g[0].People[0]; p.Name != "Jo Example" || p.LinkedIn == "" || p.Company != "Acme" {
		t.Errorf("first: %+v", p)
	}
	if p := g[0].People[1]; p.LinkedIn != "" {
		t.Errorf("not a LinkedIn link kept: %+v", p)
	}

	up = false
	g = c.get(true)
	if len(g[0].People) != 2 || g[0].Error == "" {
		t.Errorf("site down: got %+v", g)
	}
}
