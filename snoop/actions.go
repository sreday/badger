package snoop

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Actions on a call (Marek 2026-11-12: "put the same action logic and
// visuals to snoop calls, as we have in snap - just make them distinct
// visually, maybe a flowchart instead of circle").
//
// Claude's read of a call (claude.go) brings 3 options: the recommended
// move and two different plays. A "reply" option, or Other (Marek types the
// brief), fires Snoop's own drafting routine "Snoop: draft a follow-up"
// (Claude Code on Marek's subscription, Gmail connector) with the call's
// details; Claude finds the conversation with these people (or writes a new
// email) and saves ONE Gmail draft. Nothing is sent. Snoop doesn't read
// Gmail, so the card says "Claude is drafting" for a few minutes and then
// points at Gmail's drafts.
//
// The routine's id and API token live in DataDir/snoop/draft.json, set from
// the call page (paste the token once), so no server env edit is needed.

// CallAction is one option Claude proposes after a call.
type CallAction struct {
	Title  string `json:"title"`
	Kind   string `json:"kind"`   // "reply" (a follow-up email Claude drafts), "task" (Mark's own step)
	Prompt string `json:"prompt"` // the brief for the drafter, or what the task is
}

func (a CallAction) Reply() bool { return a.Kind == "reply" || a.Kind == "" }

// draftRoutineID is "Snoop: draft a follow-up" (created 2026-11-12); the
// call page offers it when asking for the API token.
const draftRoutineID = "trig_01BZoqNHJbV6v6xWAhdBg7en"

const (
	snoopFireURL   = "https://api.anthropic.com/v1/claude_code/routines/%s/fire"
	draftingFor    = 4 * time.Minute // how long the card says "Claude is drafting"
	draftRefireGap = 2 * time.Minute // a second click within this just waits
)

type draftCfg struct {
	Routine string `json:"routine"`
	Token   string `json:"token"`
}

func (s *Server) draftPath() string { return filepath.Join(s.cfg.DataDir, "snoop", "draft.json") }

func (s *Server) loadDraftCfg() draftCfg {
	var c draftCfg
	if data, err := os.ReadFile(s.draftPath()); err == nil {
		json.Unmarshal(data, &c)
	}
	return c
}

// CanDraft: the drafting routine is connected.
func (s *Server) CanDraft() bool { c := s.loadDraftCfg(); return c.Routine != "" && c.Token != "" }

// Drafting: Claude was asked for a follow-up a moment ago.
func (c *Call) Drafting(now time.Time) bool {
	return !c.DraftAsked.IsZero() && now.Sub(c.DraftAsked) < draftingFor
}

// draftPrompt is the routine's fire payload for one call.
func (s *Server) draftPrompt(c *Call, a *CallAction, note string) string {
	var b strings.Builder
	b.WriteString("MODE: CALL FOLLOW-UP\n\n")
	fmt.Fprintf(&b, "Mark (%s) had this call and wants a follow-up email drafted. Save ONE Gmail draft; never send.\n\n", s.cfg.Account)
	fmt.Fprintf(&b, "Call: %q, %s (%s).\n", c.Title, c.Start.In(s.tz).Format("Mon 2 Jan 2006, 15:04"), s.tz)
	var who []string
	for _, at := range c.Attendees {
		if strings.EqualFold(at.Email, s.cfg.Account) {
			continue
		}
		w := at.Email
		if at.Name != "" {
			w = at.Name + " <" + at.Email + ">"
		}
		who = append(who, w)
	}
	if len(who) > 0 {
		fmt.Fprintf(&b, "Other people on the invite: %s.\n", strings.Join(who, ", "))
	}
	if c.Calendly {
		fmt.Fprintf(&b, "Booked through Calendly: %s.\n", c.CalendlyEvent)
	}
	if n := c.Notes(); n != nil {
		if n.Summary != "" {
			fmt.Fprintf(&b, "What happened on the call (Claude's notes, from the transcript): %s\n", n.Summary)
		}
		if len(n.NextSteps) > 0 {
			b.WriteString("Agreed next steps:\n")
			for i, st := range n.NextSteps {
				mark := ""
				if c.Ticked[i] {
					mark = " (done)"
				}
				fmt.Fprintf(&b, "- %s%s\n", st, mark)
			}
		}
	}
	if c.CompanyFacts != "" {
		fmt.Fprintf(&b, "About their company: %s\n", c.CompanyFacts)
	}
	switch {
	case note != "":
		b.WriteString("\nMark's own instruction for this email (follow it; it overrides everything else): " + note + "\n")
	case a != nil:
		b.WriteString("\nThe follow-up Mark picked: " + a.Title + ".\n")
		if a.Prompt != "" {
			b.WriteString("Details: " + a.Prompt + "\n")
		}
	}
	b.WriteString("\nFind the email conversation with these people about this matter and reply in it, or write a new email to them if there is none. Use only facts from the call notes, the emails and Mark's instruction.")
	return b.String()
}

func (s *Server) fireDraft(text string) error {
	cfg := s.loadDraftCfg()
	body, _ := json.Marshal(map[string]string{"text": text})
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf(s.fireBase, cfg.Routine), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "experimental-cc-routine-2026-04-01")
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct{ Message string } `json:"error"`
		}
		json.Unmarshal(out, &e)
		switch resp.StatusCode {
		case http.StatusTooManyRequests:
			return fmt.Errorf("Claude's hourly limit for drafts is reached, try again later")
		case http.StatusUnauthorized:
			return fmt.Errorf("the drafting routine's token is wrong or was regenerated: paste the new one on a call page")
		}
		return fmt.Errorf("starting Claude: %d %s", resp.StatusCode, e.Error.Message)
	}
	return nil
}

// handleDraft: POST /admin/call/{id}/draft (a = option index, or note = Other).
func (s *Server) handleDraft(w http.ResponseWriter, r *http.Request) {
	c, ok := s.store.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !s.CanDraft() {
		http.Error(w, "Drafting isn't connected yet: paste the routine's token on the call page", http.StatusServiceUnavailable)
		return
	}
	now := s.now()
	if now.Sub(c.DraftAsked) < draftRefireGap {
		s.back(w, r, c.ID) // Claude is already on it
		return
	}
	note := strings.TrimSpace(r.FormValue("note"))
	if len(note) > 2000 {
		note = note[:2000]
	}
	var act *CallAction
	if i, err := strconv.Atoi(r.FormValue("a")); err == nil && i >= 0 && i < len(c.Actions) {
		act = &c.Actions[i]
	}
	if note == "" && act == nil {
		http.Error(w, "Pick an option or write what the email should say", http.StatusBadRequest)
		return
	}
	if err := s.fireDraft(s.draftPrompt(&c, act, note)); err != nil {
		log.Printf("Snoop: draft for %s: %v", c.ID, err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	what := note
	if act != nil {
		what = act.Title
	}
	s.store.Update(c.ID, func(c *Call) { c.DraftAsked, c.DraftWhat = now, what })
	s.back(w, r, c.ID)
}

var routineID = regexp.MustCompile(`^trig_[A-Za-z0-9]+$`)

// handleDraftSetup: POST /admin/draft-setup saves the drafting routine's id
// and API token (made in the routine's Edit screen: Trigger, +, API).
func (s *Server) handleDraftSetup(w http.ResponseWriter, r *http.Request) {
	cfg := draftCfg{Routine: strings.TrimSpace(r.FormValue("routine")), Token: strings.TrimSpace(r.FormValue("token"))}
	if !routineID.MatchString(cfg.Routine) || !strings.HasPrefix(cfg.Token, "sk-ant-") {
		http.Error(w, "That doesn't look like a routine id (trig_...) and its API token (sk-ant-...)", http.StatusBadRequest)
		return
	}
	data, _ := json.Marshal(cfg)
	if err := writeFileAtomic(s.draftPath(), data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, r.Header.Get("Referer"), http.StatusSeeOther)
}

func (s *Server) back(w http.ResponseWriter, r *http.Request, id string) {
	if r.Header.Get("X-Requested-With") == "fetch" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, s.cfg.Base+"/admin/call/"+id, http.StatusSeeOther)
}
