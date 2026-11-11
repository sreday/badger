package sniff

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sreday/badger/event"
	"github.com/sreday/badger/web"
)

// Test posts (before an event goes live) are kept in their own file per
// event, events/<event>/sniff_test.jsonl, so a restart or a deploy doesn't
// lose what speakers try out; the file goes as soon as the event is live
// (purgeTest), and posts from it are never mixed with the real ones.

// appendTo adds a record to one of an event's files. The caller holds s.mu.
func (s *SniffStore) appendTo(eventID, name string, rec sniffRec) error {
	dir := filepath.Join(s.dir, "events", web.SafeName(eventID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// writeTest writes an event's test file again (after a delete). The
// caller holds s.mu.
func (s *SniffStore) writeTest(eventID string) error {
	path := filepath.Join(s.dir, "events", web.SafeName(eventID), "sniff_test.jsonl")
	recs := s.testLogs[eventID]
	if len(recs) == 0 {
		err := os.Remove(path)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var b strings.Builder
	for _, rec := range recs {
		line, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// loadTest replays the test files of events still in test mode; a file
// whose event has gone live is removed.
func (s *SniffStore) loadTest() error {
	files, _ := filepath.Glob(filepath.Join(s.dir, "events", "*", "sniff_test.jsonl"))
	now := time.Now()
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		stale := false
		for i, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var rec sniffRec
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				fmt.Fprintf(os.Stderr, "%s:%d: skipping bad line: %v\n", f, i+1, err)
				continue
			}
			ss := s.sessions[rec.S]
			if ss == nil {
				continue
			}
			if ss.Phase(now) != event.PhaseTest {
				stale = true
				continue
			}
			s.testLogs[ss.EventID] = append(s.testLogs[ss.EventID], rec)
			s.apply(s.dataFor(rec.S, true), rec, true)
		}
		if stale {
			os.Remove(f)
		}
	}
	return nil
}
