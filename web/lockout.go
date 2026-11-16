package web

import (
	"crypto/sha256"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The admin login is one password for every tool, so its lockout is shared
// too, the same as the PIN pages': five wrong passwords from one address and
// that address waits ten minutes. The same wrong password sent again (a
// browser replaying what it has stored) counts once, so a stale saved
// password doesn't lock its owner out by itself.
const (
	LockTries = 5
	LockFor   = 10 * time.Minute
)

type adminFail struct {
	tried map[[32]byte]bool
	first time.Time
	until time.Time
}

var adminLock = struct {
	sync.Mutex
	fails map[string]*adminFail
	now   func() time.Time
}{fails: map[string]*adminFail{}, now: time.Now}

// ClientIP is the caller's address: what the proxy in front (Caddy, on the
// same machine) reports, else the connection's own.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1]) // the hop the proxy itself added
		}
	}
	return host
}

// AdminLocked: this address sent too many wrong admin passwords and waits.
func AdminLocked(r *http.Request) bool {
	adminLock.Lock()
	defer adminLock.Unlock()
	f := adminLock.fails[ClientIP(r)]
	return f != nil && adminLock.now().Before(f.until)
}

// AdminFailed notes one wrong admin password from this address.
func AdminFailed(r *http.Request, password string) {
	adminLock.Lock()
	defer adminLock.Unlock()
	now, ip := adminLock.now(), ClientIP(r)
	f := adminLock.fails[ip]
	if f == nil || now.Sub(f.first) > LockFor && now.After(f.until) {
		f = &adminFail{tried: map[[32]byte]bool{}, first: now}
		adminLock.fails[ip] = f
	}
	f.tried[sha256.Sum256([]byte(password))] = true
	if len(f.tried) >= LockTries {
		f.tried, f.first, f.until = map[[32]byte]bool{}, now, now.Add(LockFor)
	}
	if len(adminLock.fails) > 10000 { // never grows without bound
		for k, v := range adminLock.fails {
			if now.After(v.until) && now.Sub(v.first) > LockFor {
				delete(adminLock.fails, k)
			}
		}
	}
}

// AdminPassed clears the count: the right password came from this address.
func AdminPassed(r *http.Request) {
	adminLock.Lock()
	defer adminLock.Unlock()
	if f := adminLock.fails[ClientIP(r)]; f != nil && !adminLock.now().Before(f.until) {
		delete(adminLock.fails, ClientIP(r))
	}
}

// TooMany answers a locked-out address.
func TooMany(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "600")
	http.Error(w, "Too many wrong passwords. Try again in ten minutes.", http.StatusTooManyRequests)
}
