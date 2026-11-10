package snoop

import (
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

const (
	lookBack  = 7 * 24 * time.Hour // calendar + conference records this far back
	lookAhead = 48 * time.Hour     // arm Meets starting within this
	pollEvery = 5 * time.Minute

	// A Workspace upgrade can take hours to reach the Meet API, so a call
	// armed without Gemini notes tries again this often until it starts.
	geminiRetry = 30 * time.Minute
)

// loop syncs every pollEvery, and straight away when "Sync now" is pressed.
func (s *Server) loop() {
	t := time.NewTicker(pollEvery)
	defer t.Stop()
	for {
		if err := s.syncOnce(); err != nil && !errors.Is(err, ErrNotConnected) {
			log.Printf("Snoop sync: %v", err)
		}
		select {
		case <-t.C:
		case <-s.kick:
		}
	}
}

// syncOnce is one pass: calendar → traces, arm Marek's upcoming Meets,
// collect transcripts + Gemini notes, pick up ad-hoc Meets, write docs.
func (s *Server) syncOnce() error {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	now := s.now()
	err := s.sync(now)
	s.statMu.Lock()
	s.lastSync, s.lastErr = now, err
	s.statMu.Unlock()
	return err
}

func (s *Server) sync(now time.Time) error {
	if s.google.Connected() == "" {
		return ErrNotConnected
	}
	events, err := s.google.Events(s.cfg.CalendarID, now.Add(-lookBack), now.Add(lookAhead))
	if err != nil {
		return fmt.Errorf("reading the calendar: %w", err)
	}
	seen := make(map[string]bool, len(events))
	for i := range events {
		seen[events[i].ID] = true
		if _, err := s.traceEvent(&events[i]); err != nil {
			return err
		}
	}
	if err := s.refreshMoved(seen, now); err != nil {
		return err
	}
	for _, c := range s.store.All() {
		if c.Cancelled || c.Declined && !c.Owned {
			continue
		}
		retryGemini := c.Armed && c.NoGemini && c.Start.After(now) && now.Sub(c.GeminiTried) >= geminiRetry
		if c.Owned && (!c.Armed && c.ArmError == "" || retryGemini) && c.End.After(now) && c.Start.Before(now.Add(lookAhead)) {
			s.arm(c, now)
		}
		if c.Start.Before(now) && c.Start.After(now.Add(-lookBack)) && s.wantsCollect(&c, now) {
			s.collect(c, now)
		}
	}
	if err := s.adHoc(now); err != nil {
		log.Printf("Snoop ad-hoc Meets: %v", err)
	}
	for _, c := range s.store.All() {
		if c.Start.After(now.Add(-lookBack)) {
			s.writeDoc(c, now)
		}
	}
	return nil
}

// traceEvent creates or refreshes the trace for one calendar event with a Meet.
func (s *Server) traceEvent(ev *CalEvent) (Call, error) {
	code := ev.MeetingCode()
	if code == "" || ev.Start.DateTime.IsZero() { // no Meet, or an all-day event
		return Call{}, nil
	}
	return s.store.Update(CallID(ev.ID), func(c *Call) {
		if c.MeetingCode != "" && c.MeetingCode != code {
			// Rebooked with a new Meet link: arm the new space afresh.
			c.Armed, c.NoGemini, c.ArmError, c.GeminiTried = false, false, "", time.Time{}
		}
		c.EventID = ev.ID
		c.Title = ev.Summary
		c.MeetingCode = code
		c.Start, c.End = ev.Start.DateTime, ev.End.DateTime
		c.Organizer = ev.Organizer.Email
		c.Owned = ev.Organizer.Self
		c.Cancelled = ev.Status == "cancelled"
		c.Calendly, c.CalendlyEvent = ParseCalendly(ev.Description)
		c.CalendlyAnswers = nil
		if c.Calendly {
			c.CalendlyAnswers = ParseCalendlyAnswers(ev.Description)
		}
		c.Declined = false
		c.Attendees = c.Attendees[:0]
		for _, a := range ev.Attendees {
			if a.Resource {
				continue
			}
			if a.Self && a.ResponseStatus == "declined" {
				c.Declined = true
			}
			c.Attendees = append(c.Attendees, Attendee{Name: a.DisplayName, Email: a.Email, Response: a.ResponseStatus})
		}
		// Gemini attaches its notes to the event: the fallback when the
		// Meet API's smartNotes isn't available.
		for _, at := range ev.Attachments {
			if c.NotesDoc == "" && strings.HasPrefix(strings.ToLower(at.Title), "notes by gemini") {
				c.NotesDoc = at.FileID
				if c.NotesDoc == "" {
					c.NotesDoc = docID(at.FileURL)
				}
			}
		}
	})
}

// refreshMoved re-reads calendar traces the listing no longer returns. A call
// rebooked to another week (Calendly reschedules move the same event) or
// deleted outright drops out of the window, and its trace would otherwise
// keep the old slot and turn into "no transcript".
func (s *Server) refreshMoved(seen map[string]bool, now time.Time) error {
	for _, c := range s.store.All() {
		if c.EventID == "" || seen[c.EventID] || c.Cancelled {
			continue
		}
		if c.Start.Before(now.Add(-lookBack)) || c.Start.After(now.Add(lookAhead)) {
			continue // outside the window anyway: nothing to compare against
		}
		ev, err := s.google.Event(s.cfg.CalendarID, c.EventID)
		switch {
		case isStatus(err, 404, 410):
			s.store.Update(c.ID, func(c *Call) { c.Cancelled = true })
		case err != nil:
			return fmt.Errorf("re-reading %q: %w", c.Title, err)
		case ev.MeetingCode() == "":
			// The Meet link was removed: nothing left to trace.
			s.store.Update(c.ID, func(c *Call) { c.Cancelled = true })
		default:
			if _, err := s.traceEvent(&ev); err != nil {
				return err
			}
		}
	}
	return nil
}

// arm switches on transcripts (+ Gemini notes) for one of Marek's Meets.
func (s *Server) arm(c Call, now time.Time) {
	sp, err := s.google.Space(c.MeetingCode)
	notes := false
	if err == nil {
		ac := sp.Config.ArtifactConfig
		if ac.TranscriptionConfig.AutoTranscriptionGeneration == "ON" && ac.SmartNotesConfig.AutoSmartNotesGeneration == "ON" {
			s.store.Update(c.ID, func(c *Call) { c.Armed, c.NoGemini, c.ArmError = true, false, "" })
			return
		}
		notes, err = s.google.Arm(sp.Name)
	}
	s.store.Update(c.ID, func(c *Call) {
		switch {
		case err == nil:
			c.Armed, c.NoGemini, c.ArmError = true, !notes, ""
			if !notes {
				c.GeminiTried = now
			}
		case featureUnavailable(err):
			c.ArmError = "Meet transcripts aren't available on this account (Workspace plan or admin setting)"
		case isStatus(err, 403, 404):
			c.ArmError = "Marek doesn't own this Meet space (made by someone else, or in another calendar)"
		default:
			c.ArmError = truncate(err.Error(), 200)
		}
	})
}

// wantsCollect says whether the Meet API may still have something new for the call.
func (s *Server) wantsCollect(c *Call, now time.Time) bool {
	if c.Start.After(now) {
		return false
	}
	if !c.TranscriptDone {
		// A conference found but no transcript yet: Meet can be slow on long calls.
		return now.Before(c.End.Add(transcriptWait)) || c.ConferenceID != "" && now.Before(c.End.Add(24*time.Hour))
	}
	return c.APINotes == nil && now.Before(c.End.Add(notesWait))
}

// collect fetches the Meet transcript and Gemini notes for a call.
func (s *Server) collect(c Call, now time.Time) {
	err := s.collectErr(&c, now)
	s.store.Update(c.ID, func(stored *Call) {
		stored.ConferenceID = c.ConferenceID
		stored.TranscriptDoc = c.TranscriptDoc
		stored.TranscriptDone = c.TranscriptDone
		stored.APILines = c.APILines
		stored.NotesDoc = c.NotesDoc
		stored.APINotes = c.APINotes
		stored.Error = ""
		if err != nil {
			stored.Error = truncate(err.Error(), 300)
		}
	})
}

func (s *Server) collectErr(c *Call, now time.Time) error {
	if c.ConferenceID == "" {
		confs, err := s.google.Conferences(fmt.Sprintf("space.meeting_code = %q", c.MeetingCode))
		if err != nil {
			if !c.Owned && isStatus(err, 403, 404) {
				return nil // someone else's Meet: the extension is the source
			}
			return fmt.Errorf("finding the conference: %w", err)
		}
		conf, ok := pickConference(confs, c)
		if !ok {
			return nil
		}
		c.ConferenceID = conf.Name
	}

	if !c.TranscriptDone {
		ts, err := s.google.Transcripts(c.ConferenceID)
		if err != nil {
			return fmt.Errorf("listing transcripts: %w", err)
		}
		for _, t := range ts {
			if t.State != "FILE_GENERATED" {
				continue
			}
			lines, err := s.google.Entries(c.ConferenceID, t.Name)
			if err != nil {
				return fmt.Errorf("reading the transcript: %w", err)
			}
			c.APILines = append(c.APILines, lines...)
			c.TranscriptDoc = docID(t.DocsDestination.Document)
			c.TranscriptDone = true
		}
		// A call can be transcribed in more than one stretch.
		sort.SliceStable(c.APILines, func(i, j int) bool { return c.APILines[i].At.Before(c.APILines[j].At) })
	}

	if c.APINotes == nil {
		notes, err := s.google.SmartNotes(c.ConferenceID)
		switch {
		case err == nil:
			for _, n := range notes {
				if n.State == "FILE_GENERATED" && n.DocsDestination.Document != "" {
					c.NotesDoc = docID(n.DocsDestination.Document)
				}
			}
		case isStatus(err, 403, 404, 501):
			// smartNotes not available on this account: the calendar attachment
			// (traceEvent) is the fallback.
		default:
			return fmt.Errorf("listing Gemini notes: %w", err)
		}
		if c.NotesDoc != "" {
			doc, err := s.google.ExportHTML(c.NotesDoc)
			if err != nil {
				return fmt.Errorf("reading Gemini's notes: %w", err)
			}
			n := ParseGeminiNotes(doc)
			c.APINotes = &n
		}
	}
	return nil
}

// pickConference returns the finished conference record that matches the
// call's calendar slot best.
func pickConference(confs []Conference, c *Call) (Conference, bool) {
	var best Conference
	var bestGap time.Duration = -1
	for _, cf := range confs {
		if cf.EndTime.IsZero() || cf.StartTime.Before(c.Start.Add(-time.Hour)) || cf.StartTime.After(c.End.Add(time.Hour)) {
			continue
		}
		gap := cf.StartTime.Sub(c.Start)
		if gap < 0 {
			gap = -gap
		}
		if bestGap < 0 || gap < bestGap {
			best, bestGap = cf, gap
		}
	}
	return best, bestGap >= 0
}

// adHoc traces Meets with no calendar event (a meet.new link, a Meet opened
// from Chat) that the Meet API knows about.
func (s *Server) adHoc(now time.Time) error {
	confs, err := s.google.Conferences(fmt.Sprintf("start_time >= %q", now.Add(-lookBack).UTC().Format(time.RFC3339)))
	if err != nil {
		return err
	}
	known := make(map[string]bool)
	for _, c := range s.store.All() {
		if c.ConferenceID != "" {
			known[c.ConferenceID] = true
		}
	}
	for _, cf := range confs {
		if known[cf.Name] || cf.EndTime.IsZero() {
			continue
		}
		sp, err := s.google.Space(strings.TrimPrefix(cf.Space, "spaces/"))
		if err != nil {
			return fmt.Errorf("looking up %s: %w", cf.Space, err)
		}
		if c, ok := s.store.FindByCode(sp.MeetingCode, cf.StartTime); ok && c.ConferenceID == "" {
			continue // a calendar trace; collect() links it next pass
		}
		id := CallID("meet_" + strings.TrimPrefix(cf.Name, "conferenceRecords/"))
		c, err := s.store.Update(id, func(c *Call) {
			c.Title = "Meet " + sp.MeetingCode
			c.MeetingCode = sp.MeetingCode
			c.Start, c.End = cf.StartTime, cf.EndTime
			c.Owned = true // the API only shows conference records of Marek's spaces
			c.ConferenceID = cf.Name
		})
		if err != nil {
			return err
		}
		if s.wantsCollect(&c, now) {
			s.collect(c, now)
		}
	}
	return nil
}

// writeDoc writes the call's Drive doc once there's a transcript, and
// again whenever it changes (new notes, ticked steps).
func (s *Server) writeDoc(c Call, now time.Time) {
	if len(c.Lines()) == 0 {
		return
	}
	if st, _ := c.Status(now); st != StatusDone && st != StatusTranscript {
		return
	}
	body := DocHTML(&c, s.tz, s.cfg.PublicURL+s.cfg.Base+"/admin/call/"+c.ID)
	h := hashOf(body)
	if h == c.DocHash && c.DocID != "" {
		return
	}
	id, err := s.google.WriteDoc(s.cfg.DriveFolder, c.DocID, DocTitle(&c, s.tz), body)
	s.store.Update(c.ID, func(stored *Call) {
		if err != nil {
			stored.Error = "writing the Drive doc: " + truncate(err.Error(), 250)
			return
		}
		stored.DocID, stored.DocHash = id, h
		if strings.HasPrefix(stored.Error, "writing the Drive doc") {
			stored.Error = ""
		}
	})
}
