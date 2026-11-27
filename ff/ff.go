// Package ff gives the Filmforum practice app its addresses on scan.sreday.com
// while its tools are being built: everything under /ff/ is passed, as it is,
// to the app, which runs on its own elsewhere and keeps its own data.
//
// Where the app runs is set on the server only (FF_UPSTREAM), never in this
// repo. Without it /ff/ answers "not found". The app itself answers "not found"
// to anything outside its own secret folder, so this package needs no login.
package ff

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// Handler forwards requests (with the /ff prefix already removed) to upstream.
func Handler(upstream string) http.Handler {
	u, err := url.Parse(strings.TrimRight(upstream, "/"))
	if upstream == "" || err != nil || u.Scheme == "" || u.Host == "" {
		return http.NotFoundHandler()
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(u)
			r.Out.Host = u.Host
			r.Out.Header.Del("Cookie")
			r.Out.Header.Del("Authorization")
		},
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Set("X-Robots-Tag", "noindex, nofollow, noarchive")
			resp.Header.Set("Referrer-Policy", "no-referrer")
			// A redirect from the app points at its own address: keep people on ours.
			if loc := resp.Header.Get("Location"); strings.HasPrefix(loc, "/") {
				resp.Header.Set("Location", "/ff"+loc)
			} else if strings.HasPrefix(loc, u.String()) {
				resp.Header.Set("Location", "/ff"+strings.TrimPrefix(loc, u.String()))
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "The Filmforum app is not answering right now. Try again in a minute.", http.StatusBadGateway)
		},
	}
	return proxy
}
