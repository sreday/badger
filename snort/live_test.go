package snort

import (
	"os"
	"testing"
	"time"
)

// TestLiveEvents reads one brand's events from the real GitHub when
// SNORT_LIVE is set (a brand name). Skipped otherwise.
func TestLiveEvents(t *testing.T) {
	b := os.Getenv("SNORT_LIVE")
	if b == "" {
		t.Skip("set SNORT_LIVE")
	}
	s, err := New(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if b == "conf42" {
		list, err := s.readConf42Events(time.Now())
		t.Logf("err=%v", err)
		for _, ev := range list {
			t.Logf("%s %s %s %s", ev.ID, ev.Day, ev.Tab, ev.Suffix)
		}
		return
	}
	list, err := s.readSiteEvents(b, time.Now())
	t.Logf("err=%v", err)
	for _, ev := range list {
		t.Logf("%s %s %s", ev.ID, ev.Day, ev.File)
	}
}
