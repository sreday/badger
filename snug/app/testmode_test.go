package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Once an event is live, its badges printed in test mode are wiped; later
// ones, other events' and everything before the event goes live stay.
func TestWipeTestBadges(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, at time.Time) {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte("png"), 0644)
		os.Chtimes(p, at, at)
	}
	start := time.Now().Add(2 * time.Hour) // live since 4 hours ago
	write("evt-1-test.png", start.Add(-30*time.Hour))
	write("evt-1-real.png", start.Add(-time.Hour))
	write("evt-2-other.png", start.Add(-30*time.Hour))
	wipeTestBadges(start.Format(time.RFC3339), dir, "evt-1-")
	for name, want := range map[string]bool{"evt-1-test.png": false, "evt-1-real.png": true, "evt-2-other.png": true} {
		if _, err := os.Stat(filepath.Join(dir, name)); (err == nil) != want {
			t.Errorf("%s: kept %v, want %v", name, err == nil, want)
		}
	}

	// Still test mode: nothing goes.
	write("evt-3-test.png", time.Now().Add(-time.Hour))
	wipeTestBadges(time.Now().Add(48*time.Hour).Format(time.RFC3339), dir, "evt-3-")
	if _, err := os.Stat(filepath.Join(dir, "evt-3-test.png")); err != nil {
		t.Error("test mode wiped a badge")
	}
}
