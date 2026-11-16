package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAdminLockout(t *testing.T) {
	now := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	adminLock.now = func() time.Time { return now }
	defer func() { adminLock.now = time.Now }()
	req := func(ip string) *http.Request {
		r := httptest.NewRequest("GET", "/snap/", nil)
		r.RemoteAddr = "127.0.0.1:5555"
		r.Header.Set("X-Forwarded-For", "6.6.6.6, "+ip) // a forged first hop is ignored
		return r
	}
	a, b := req("1.1.1.1"), req("2.2.2.2")
	// The same wrong password again and again is one try.
	for i := 0; i < 20; i++ {
		AdminFailed(a, "stale")
	}
	if AdminLocked(a) {
		t.Fatal("a replayed password locked its owner out")
	}
	for _, p := range []string{"a", "b", "c"} {
		AdminFailed(a, p)
	}
	if AdminLocked(a) {
		t.Fatal("locked after four different passwords")
	}
	AdminFailed(a, "d")
	if !AdminLocked(a) || AdminLocked(b) {
		t.Fatal("five wrong passwords lock that address, and only that one")
	}
	AdminPassed(a) // the right password during the wait changes nothing
	if !AdminLocked(a) {
		t.Fatal("a locked address got in")
	}
	now = now.Add(LockFor + time.Second)
	if AdminLocked(a) {
		t.Fatal("still locked after ten minutes")
	}
	AdminFailed(a, "x")
	AdminPassed(a)
	for _, p := range []string{"1", "2", "3", "4"} {
		AdminFailed(a, p)
	}
	if AdminLocked(a) {
		t.Fatal("a good login did not clear the count")
	}
	if ClientIP(a) != "1.1.1.1" {
		t.Fatalf("client ip %q", ClientIP(a))
	}
	direct := httptest.NewRequest("GET", "/", nil)
	direct.RemoteAddr = "9.9.9.9:1234"
	direct.Header.Set("X-Forwarded-For", "1.1.1.1")
	if ClientIP(direct) != "9.9.9.9" {
		t.Fatal("a caller that is not the proxy chose its own address")
	}
}
