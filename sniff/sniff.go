package sniff

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/skip2/go-qrcode"
	"github.com/sreday/badger/event"
	"github.com/sreday/badger/luma"
	"github.com/sreday/badger/pkg"
	"github.com/sreday/badger/sites"
	"github.com/sreday/badger/web"
)

// Sniff by Badger: one QR per session for questions and feedback (see
// sniff_store.go). Pages:
//
//	/q/<event code>       everyone: the event's one QR, every speaker's face
//	/q/<code>             the audience: ask, vote, rate. Tiny, works on bad wifi;
//	                      the speakers' PIN (1111, the same for every talk)
//	                      opens their page from here
//	/sniff/<token>        the speaker: questions and feedback live, the QR, the slide
//	/admin/sniff/<event>  the organizers: every session's links
//
// Handlers never log who posted, what, or from where.

// sniffPIN opens every speaker's page, at every event: easy to remember
// and to share. Someone in the room guessing it only gets to read the
// questions, which they can mostly read anyway.
const sniffPIN = "1111"

const (
	maxQuestion = 300
	maxName     = 60
	maxComment  = 500
)

// --- Rate limits (memory only) --------------------------------------------

// limiter counts recent hits per key. Keys are the phone's random key (a
// whole venue shares one IP, so per-IP limits are only a very high ceiling).
type limiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newLimiter() *limiter { return &limiter{hits: map[string][]time.Time{}} }

// allow records a hit for key unless it already had n within per.
func (l *limiter) allow(key string, n int, per time.Duration, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.hits) > 20000 {
		for k, ts := range l.hits {
			if len(ts) == 0 || now.Sub(ts[len(ts)-1]) > time.Hour {
				delete(l.hits, k)
			}
		}
	}
	ts := l.hits[key]
	keep := ts[:0]
	for _, t := range ts {
		if now.Sub(t) < per {
			keep = append(keep, t)
		}
	}
	if len(keep) >= n {
		l.hits[key] = keep
		return false
	}
	l.hits[key] = append(keep, now)
	return true
}

// clientIP is the phone's address as Caddy saw it, for the rate-limit
// ceiling only: never stored or logged.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if f := r.Header.Get("X-Forwarded-For"); f != "" {
			return strings.TrimSpace(strings.Split(f, ",")[0])
		}
	}
	return host
}

// --- Audience ---------------------------------------------------------------

// sniffInput is what the audience page posts (JSON), or the same fields from
// a plain form when the phone has no JavaScript.
type sniffInput struct {
	ID      string `json:"id"`     // the question's own random id (resends are no-ops)
	Text    string `json:"text"`   // question or comment
	Name    string `json:"name"`   // as typed
	Anon    bool   `json:"anon"`   // post without the name
	Secret  string `json:"secret"` // lets this phone delete its own question
	Key     string `json:"k"`      // per-session random key: the phone's vote and rating
	Limit   string `json:"rl"`     // per-event random key, for rate limits only (never stored)
	Q       string `json:"q"`      // the question voted on or deleted
	On      bool   `json:"on"`
	Stars   int    `json:"stars"`
	Form    bool   `json:"-"`
	Session string `json:"-"`
}

func readSniffInput(r *http.Request) (sniffInput, error) {
	var in sniffInput
	r.Body = http.MaxBytesReader(nil, r.Body, 16<<10)
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			return in, err
		}
	} else {
		if err := r.ParseForm(); err != nil {
			return in, err
		}
		in.Form = true
		in.Text, in.Name, in.Anon = r.PostForm.Get("text"), r.PostForm.Get("name"), r.PostForm.Get("anon") != ""
		in.Stars, _ = strconv.Atoi(r.PostForm.Get("stars"))
		in.Q, in.On = r.PostForm.Get("q"), r.PostForm.Get("on") != ""
		in.ID, in.Key = r.PostForm.Get("id"), r.PostForm.Get("k")
	}
	in.Text = strings.TrimSpace(in.Text)
	in.Name = strings.Join(strings.Fields(in.Name), " ")
	if utf8.RuneCountInString(in.Name) > maxName {
		in.Name = string([]rune(in.Name)[:maxName])
	}
	if in.Anon {
		in.Name = ""
	}
	return in, nil
}

// validKey: the phone's random ids are 8-64 letters, digits or dashes.
func validKey(s string) bool {
	if len(s) < 8 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

func randomID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// sniffReply answers a post: JSON for the page's script, or straight back
// to the page for a plain form.
func sniffReply(w http.ResponseWriter, r *http.Request, in sniffInput, status int, msg string) {
	if in.Form {
		q := url.Values{}
		if status < 300 {
			q.Set("sent", "1")
		} else {
			q.Set("err", msg)
		}
		http.Redirect(w, r, "/q/"+in.Session+"?"+q.Encode(), http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if status < 300 {
		w.Write([]byte(`{"ok":true}`))
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": msg})
}

// sniffGate checks the session, that it's open, and the rate limits. It
// returns false after answering.
func (s *Server) sniffGate(w http.ResponseWriter, r *http.Request, kind string) (SniffSession, sniffInput, bool) {
	code := strings.ToUpper(r.PathValue("code"))
	in, err := readSniffInput(r)
	in.Session = code
	if err != nil {
		sniffReply(w, r, in, http.StatusBadRequest, "That didn't come through, please try again.")
		return SniffSession{}, in, false
	}
	ss, ok := s.sniff.Session(code)
	if !ok {
		sniffReply(w, r, in, http.StatusNotFound, "This session doesn't exist.")
		return ss, in, false
	}
	now := s.now()
	if !ss.Open(now) {
		sniffReply(w, r, in, http.StatusGone, "This session is closed.")
		return ss, in, false
	}
	limits := map[string]struct {
		n   int
		per time.Duration
	}{"ask": {4, time.Minute}, "vote": {60, time.Minute}, "rate": {10, time.Minute}, "del": {20, time.Minute}}
	lim := limits[kind]
	who := in.Limit
	if !validKey(who) {
		who = "ip:" + clientIP(r) // no script: share a generous limit
		lim.n *= 10
	}
	if !s.sniffLimit.allow("ip:"+clientIP(r), 600, time.Minute, now) ||
		!s.sniffLimit.allow(kind+":"+who, lim.n, lim.per, now) ||
		(kind == "ask" && !s.sniffLimit.allow("session:"+code, 120, time.Minute, now)) {
		sniffReply(w, r, in, http.StatusTooManyRequests, "Easy there! Try again in a minute.")
		return ss, in, false
	}
	return ss, in, true
}

// sniffWho matches a typed name to the one guest or speaker it can only be.
func (s *Server) sniffWho(ss SniffSession, name string) *Who {
	if name == "" {
		return nil
	}
	return matchName(name, s.host.EventGuests(ss.EventID), ss.Speakers())
}

func (s *Server) handleSniffAsk(w http.ResponseWriter, r *http.Request) {
	ss, in, ok := s.sniffGate(w, r, "ask")
	if !ok {
		return
	}
	if in.Text == "" {
		sniffReply(w, r, in, http.StatusBadRequest, "Type a question first.")
		return
	}
	if utf8.RuneCountInString(in.Text) > maxQuestion {
		sniffReply(w, r, in, http.StatusBadRequest, fmt.Sprintf("Questions are up to %d characters.", maxQuestion))
		return
	}
	if !validKey(in.ID) {
		in.ID = randomID()
	}
	if harmful(in.Text) || harmful(in.Name) {
		sniffReply(w, r, in, http.StatusOK, "") // thanked as usual, never stored
		return
	}
	if err := s.sniff.Ask(ss.Code, s.now(), in.ID, in.Text, in.Name, s.sniffWho(ss, in.Name), in.Secret); err != nil {
		sniffReply(w, r, in, http.StatusInternalServerError, "That didn't save, please try again.")
		return
	}
	sniffReply(w, r, in, http.StatusOK, "")
}

func (s *Server) handleSniffVote(w http.ResponseWriter, r *http.Request) {
	ss, in, ok := s.sniffGate(w, r, "vote")
	if !ok {
		return
	}
	if !validKey(in.Key) {
		sniffReply(w, r, in, http.StatusBadRequest, "Votes need the page's script.")
		return
	}
	if err := s.sniff.Vote(ss.Code, s.now(), in.Q, in.Key, in.On); err != nil {
		sniffReply(w, r, in, http.StatusBadRequest, "That question isn't there any more.")
		return
	}
	sniffReply(w, r, in, http.StatusOK, "")
}

func (s *Server) handleSniffRate(w http.ResponseWriter, r *http.Request) {
	ss, in, ok := s.sniffGate(w, r, "rate")
	if !ok {
		return
	}
	if in.Stars < 1 || in.Stars > 3 {
		sniffReply(w, r, in, http.StatusBadRequest, "Pick a rating first.")
		return
	}
	if utf8.RuneCountInString(in.Text) > maxComment {
		in.Text = string([]rune(in.Text)[:maxComment])
	}
	if !validKey(in.Key) {
		in.Key = randomID() // a plain form: a new rating every time
	}
	if harmful(in.Text) {
		in.Text = "" // the stars count, the words never reach the speaker
	}
	if harmful(in.Name) {
		in.Name = ""
	}
	if err := s.sniff.Rate(ss.Code, s.now(), in.Key, in.Stars, in.Text, in.Name, s.sniffWho(ss, in.Name)); err != nil {
		sniffReply(w, r, in, http.StatusInternalServerError, "That didn't save, please try again.")
		return
	}
	sniffReply(w, r, in, http.StatusOK, "")
}

func (s *Server) handleSniffDelete(w http.ResponseWriter, r *http.Request) {
	ss, in, ok := s.sniffGate(w, r, "del")
	if !ok {
		return
	}
	if err := s.sniff.Delete(ss.Code, s.now(), in.Q, in.Secret); err != nil {
		sniffReply(w, r, in, http.StatusForbidden, "Only the phone that asked can delete a question.")
		return
	}
	sniffReply(w, r, in, http.StatusOK, "")
}

// audienceQ is a question as the room sees it: the text, the first name the
// asker typed (never the matched surname or company), votes.
type audienceQ struct {
	ID       string `json:"id"`
	Text     string `json:"t"`
	Name     string `json:"n,omitempty"`
	Votes    int    `json:"v"`
	Answered bool   `json:"a,omitempty"`
	Test     bool   `json:"x,omitempty"`
	At       int64  `json:"at"`
}

func firstWord(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}

// audienceFeed is the room's view of a session.
func (s *Server) audienceFeed(ss SniffSession, voter string) map[string]any {
	now := s.now()
	v := s.sniff.View(ss.Code, now)
	qs := []audienceQ{}
	for _, q := range v.Questions {
		if q.Hidden {
			continue
		}
		qs = append(qs, audienceQ{ID: q.ID, Text: q.Text, Name: firstWord(q.Name), Votes: q.Votes, Answered: q.Answered, Test: q.Test, At: q.At.Unix()})
	}
	voted := []string{}
	for id := range s.sniff.Voted(ss.Code, now, voter) {
		voted = append(voted, id)
	}
	sort.Strings(voted)
	return map[string]any{"v": strconv.FormatInt(v.Version, 36), "phase": ss.Phase(now), "open": ss.Open(now), "qs": qs, "mine": voted}
}

func (s *Server) handleSniffFeed(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.sniff.Session(r.PathValue("code"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Get("v") == strconv.FormatInt(s.sniff.Version(ss.Code, s.now()), 36) {
		w.WriteHeader(http.StatusNoContent) // nothing new: no body at all
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.audienceFeed(ss, r.URL.Query().Get("k")))
}

func (s *Server) handleSniffAudience(w http.ResponseWriter, r *http.Request) {
	code := strings.ToUpper(r.PathValue("code"))
	if eventID, ok := s.sniff.EventByCode(code); ok {
		if r.PathValue("code") != code {
			http.Redirect(w, r, "/q/"+code, http.StatusMovedPermanently)
			return
		}
		s.handleSniffEvent(w, r, eventID, code)
		return
	}
	ss, ok := s.sniff.Session(code)
	if !ok {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNotFound)
		s.tmpls["sniff_ask.html"].ExecuteTemplate(w, "sniff_ask.html", map[string]any{"Missing": true, "Code": code})
		return
	}
	if r.PathValue("code") != ss.Code {
		http.Redirect(w, r, "/q/"+ss.Code, http.StatusMovedPermanently) // typed in lower case
		return
	}
	go s.host.EventGuests(ss.EventID) // warm the guest list for name matching
	now := s.now()
	_, conf := event.Conference(ss.EventName)
	data := map[string]any{
		"S":       ss,
		"Brand":   event.Brand(ss.EventName),
		"Conf":    conferenceName(ss.EventName),
		"Init":    s.audienceFeed(ss, ""),
		"Event":   ss.EventID,
		"Slug":    conf,
		"Rate":    !ss.End.IsZero() && now.After(ss.End.Add(-5*time.Minute)),
		"Sent":    r.URL.Query().Get("sent") != "",
		"Err":     r.URL.Query().Get("err"),
		"Open":    ss.Open(now),
		"Test":    ss.Phase(now) == event.PhaseTest,
		"Speaker": firstWord(ss.Who),
		"Max":     maxQuestion,
		"PinErr":  r.URL.Query().Get("pin"),
	}
	if ec, ok := s.sniff.HasEventCode(ss.EventID); ok {
		data["All"] = ec
	}
	s.render(w, "sniff_ask.html", data)
}

// sniffTile is one speaker on the event page.
type sniffTile struct {
	Code, Who, Kind string
}

// handleSniffEvent is the event's one QR: the title and every speaker's
// face and name. A tap opens that speaker's questions.
func (s *Server) handleSniffEvent(w http.ResponseWriter, r *http.Request, eventID, code string) {
	var tiles []sniffTile
	var name string
	talks := map[string]int{}
	list := s.sniff.Sessions(eventID)
	for _, ss := range list {
		if !ss.Hidden && !ss.Gone {
			talks[foldName(ss.Who)]++
		}
	}
	for _, ss := range list {
		name = ss.EventName
		if ss.Hidden || ss.Gone {
			continue
		}
		t := sniffTile{Code: ss.Code, Who: ss.Who}
		if ss.Who == "" {
			t.Who = ss.Title
		}
		if talks[foldName(ss.Who)] > 1 && ss.Kind != "" { // the same person twice: say which
			t.Kind = strings.ToUpper(ss.Kind[:1]) + ss.Kind[1:]
		}
		tiles = append(tiles, t)
	}
	s.render(w, "sniff_event.html", map[string]any{
		"Name":  name,
		"Brand": event.Brand(name),
		"Tiles": tiles,
		"Code":  code,
	})
}

// handleSniffPin opens the speaker's page with the speakers' PIN.
func (s *Server) handleSniffPin(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.sniff.Session(r.PathValue("code"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	back := func(msg string) {
		http.Redirect(w, r, "/q/"+ss.Code+"?pin="+url.QueryEscape(msg)+"#speaker", http.StatusSeeOther)
	}
	if strings.TrimSpace(r.FormValue("pin")) != sniffPIN {
		back("That's not the speakers' PIN. The organisers have it.")
		return
	}
	setSpeakerPIN(w, r)
	http.Redirect(w, r, speakerPath(ss), http.StatusSeeOther)
}

func conferenceName(eventName string) string {
	name, _ := event.Conference(eventName)
	return name
}

// --- Speaker ------------------------------------------------------------------

// The speaker's page asks for the PIN once per phone: entering it (there
// or under "I'm the speaker") leaves a cookie that opens every speaker's
// page for a year. Its value follows the PIN, so a new PIN asks again.
const sniffCookie = "sniff_speaker"

func sniffCookieValue() string {
	h := sha256.Sum256([]byte("sniff-speaker:" + sniffPIN))
	return hex.EncodeToString(h[:12])
}

func hasSpeakerPIN(r *http.Request) bool {
	c, err := r.Cookie(sniffCookie)
	return err == nil && c.Value == sniffCookieValue()
}

func setSpeakerPIN(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sniffCookie,
		Value:    sniffCookieValue(),
		Path:     "/",
		MaxAge:   60 * 60 * 24 * 365,
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteLaxMode,
	})
}

// sniffSpeakerSession finds the session by its speaker link: the name
// (grace) or the original token.
func (s *Server) sniffSpeakerSession(link string) (SniffSession, bool) {
	ss, ok := s.sniff.ByToken(link)
	if !ok {
		ss, ok = s.sniff.BySlug(link, s.now())
	}
	return ss, ok
}

// sniffByToken is the speaker link's session, once the PIN is in: without
// it the page asks for the PIN and everything else is refused.
func (s *Server) sniffByToken(w http.ResponseWriter, r *http.Request) (SniffSession, bool) {
	ss, ok := s.sniffSpeakerSession(r.PathValue("token"))
	if !ok {
		http.NotFound(w, r)
		return ss, false
	}
	if !hasSpeakerPIN(r) {
		if r.Method == http.MethodGet && r.URL.Path == "/sniff/"+r.PathValue("token") {
			w.WriteHeader(http.StatusUnauthorized)
			s.render(w, "sniff_pin.html", map[string]any{"S": ss, "Brand": event.Brand(ss.EventName), "Conf": conferenceName(ss.EventName),
				"Link": r.PathValue("token"), "Err": r.URL.Query().Get("pin")})
		} else {
			http.Error(w, "Enter the speaker PIN first", http.StatusForbidden)
		}
		return ss, false
	}
	return ss, true
}

// handleSniffSpeakerPin is the speaker page's own PIN box.
func (s *Server) handleSniffSpeakerPin(w http.ResponseWriter, r *http.Request) {
	link := r.PathValue("token")
	if _, ok := s.sniffSpeakerSession(link); !ok {
		http.NotFound(w, r)
		return
	}
	if strings.TrimSpace(r.FormValue("pin")) != sniffPIN {
		http.Redirect(w, r, "/sniff/"+link+"?pin="+url.QueryEscape("That's not the speakers' PIN. The organisers have it."), http.StatusSeeOther)
		return
	}
	setSpeakerPIN(w, r)
	http.Redirect(w, r, "/sniff/"+link, http.StatusSeeOther)
}

// speakerPath is the speaker's page: /sniff/grace, or the token link for a
// session without a speaker's name.
func speakerPath(ss SniffSession) string {
	if ss.Slug != "" {
		return "/sniff/" + ss.Slug
	}
	return "/sniff/" + ss.Token
}

// audienceURL is what the QR opens: the event's one link, or (for an
// event that has none yet) the session's own.
func (s *Server) audienceURL(r *http.Request, ss SniffSession) string {
	if ec, ok := s.sniff.HasEventCode(ss.EventID); ok {
		return s.baseURL(r) + "/q/" + ec
	}
	return s.baseURL(r) + "/q/" + ss.Code
}

func (s *Server) handleSniffSpeaker(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.sniffByToken(w, r)
	if !ok {
		return
	}
	now := s.now()
	link := s.audienceURL(r, ss)
	shard, ok := speakerShard(ss)
	s.render(w, "sniff_speaker.html", map[string]any{
		"Shard": shard,
		"Card":  ok && shard.Photo != "",
		"S":     ss,
		"Brand": event.Brand(ss.EventName),
		"Conf":  conferenceName(ss.EventName),
		"Link":  link,
		"Short": strings.TrimPrefix(strings.TrimPrefix(link, "https://"), "http://"),
		"Phase": ss.Phase(now),
		"When":  sessionWhen(ss),
		"Init":  s.speakerFeed(ss),
		"Self":  strings.TrimPrefix(speakerPath(ss), "/sniff/"),
	})
}

// sessionWhen is "Sat 7 Nov, 10:30 · main room" in the event's time zone.
func sessionWhen(ss SniffSession) string {
	var parts []string
	if !ss.Start.IsZero() {
		loc, err := time.LoadLocation(ss.EventTZ)
		if err != nil || ss.EventTZ == "" {
			loc = ss.Start.Location()
		}
		parts = append(parts, ss.Start.In(loc).Format("Mon 2 Jan, 15:04"))
	}
	if ss.Room != "" {
		parts = append(parts, ss.Room)
	}
	return strings.Join(parts, " · ")
}

// speakerQ is a question as the speaker sees it: with who asked, when the
// name matched exactly one person.
type speakerQ struct {
	ID       string `json:"id"`
	Text     string `json:"t"`
	Name     string `json:"n,omitempty"`
	Who      *Who   `json:"who,omitempty"`
	Votes    int    `json:"v"`
	Answered bool   `json:"a,omitempty"`
	Hidden   bool   `json:"h,omitempty"`
	Imp      bool   `json:"i,omitempty"`
	Test     bool   `json:"x,omitempty"`
	At       int64  `json:"at"`
}

type speakerR struct {
	Stars   int    `json:"s"`
	Comment string `json:"c,omitempty"`
	Name    string `json:"n,omitempty"`
	Who     *Who   `json:"who,omitempty"`
	Test    bool   `json:"x,omitempty"`
	At      int64  `json:"at"`
}

func (s *Server) speakerFeed(ss SniffSession) map[string]any {
	now := s.now()
	v := s.sniff.View(ss.Code, now)
	qs := []speakerQ{}
	for _, q := range v.Questions {
		qs = append(qs, speakerQ{ID: q.ID, Text: q.Text, Name: q.Name, Who: q.Who, Votes: q.Votes, Answered: q.Answered, Hidden: q.Hidden, Imp: q.Important, Test: q.Test, At: q.At.Unix()})
	}
	rs := []speakerR{}
	for _, x := range v.Ratings {
		rs = append(rs, speakerR{Stars: x.Stars, Comment: x.Comment, Name: x.Name, Who: x.Who, Test: x.Test, At: x.At.Unix()})
	}
	return map[string]any{"v": strconv.FormatInt(v.Version, 36), "phase": ss.Phase(now), "qs": qs, "rs": rs}
}

func (s *Server) handleSniffSpeakerFeed(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.sniffByToken(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Get("v") == strconv.FormatInt(s.sniff.Version(ss.Code, s.now()), 36) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.speakerFeed(ss))
}

func (s *Server) handleSniffMod(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.sniffByToken(w, r)
	if !ok {
		return
	}
	var in struct{ Q, Mod string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := s.sniff.Mod(ss.Code, s.now(), in.Q, in.Mod); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

func (s *Server) handleSniffQR(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.sniffByToken(w, r)
	if !ok {
		return
	}
	size := 1024
	if n, err := strconv.Atoi(r.URL.Query().Get("size")); err == nil && n >= 128 && n <= 2048 {
		size = n
	}
	img, err := qrcode.Encode(s.audienceURL(r, ss), qrcode.Medium, size)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, no-store")
	if r.URL.Query().Get("dl") != "" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+fileSafe("Sniff QR - "+ss.EventName)+`.png"`)
	}
	w.Write(img)
}

func (s *Server) handleSniffSlide(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.sniffByToken(w, r)
	if !ok {
		return
	}
	link := s.audienceURL(r, ss)
	sendSniffSlide(w, r, ss.EventName, link, "Sniff slide - "+ss.EventName)
}

// sendSniffSlide draws the event's slide: the same for every talk, since
// everyone scans the one QR.
func sendSniffSlide(w http.ResponseWriter, r *http.Request, eventName, link, file string) {
	img, err := pkg.GenerateSniffSlide(pkg.SniffSlide{
		Brand: event.Brand(eventName),
		Title: eventName,
		URL:   link,
		Short: strings.TrimPrefix(strings.TrimPrefix(link, "https://"), "http://"),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, no-store")
	if r.URL.Query().Get("dl") != "" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+fileSafe(file)+`.png"`)
	}
	w.Write(buf.Bytes())
}

func fileSafe(s string) string {
	s = strings.NewReplacer(`"`, "", "/", "-", "\\", "-", ":", " -", "\n", " ", "\r", "").Replace(s)
	if r := []rune(s); len(r) > 80 {
		s = string(r[:80])
	}
	return strings.TrimSpace(s)
}

var sniffCSVHeader = []string{"Session", "Type", "Time", "Question / comment", "Rating", "Votes", "Answered", "Name as typed", "Matched name", "Job title", "Company", "LinkedIn"}

// writeSniffCSV writes sessions' questions and ratings, with who asked.
func (s *Server) writeSniffCSV(sessions []SniffSession) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("\xef\xbb\xbf") // Excel reads UTF-8 names right
	cw := csv.NewWriter(&buf)
	cw.Write(sniffCSVHeader)
	now := s.now()
	for _, ss := range sessions {
		loc, err := time.LoadLocation(ss.EventTZ)
		if err != nil || ss.EventTZ == "" {
			loc = time.UTC
		}
		v := s.sniff.View(ss.Code, now)
		who := func(w *Who) []string {
			if w == nil {
				return []string{"", "", "", ""}
			}
			return []string{web.CSVSafe(w.Name), web.CSVSafe(w.Title), web.CSVSafe(w.Company), w.LinkedIn}
		}
		label := ss.Title
		if ss.Who != "" {
			label = ss.Who + " - " + ss.Title
		}
		qs := v.Questions
		sort.SliceStable(qs, func(i, j int) bool { return qs[i].Votes > qs[j].Votes })
		for _, q := range qs {
			ans := ""
			if q.Answered {
				ans = "yes"
			}
			typ := "Question"
			if q.Hidden {
				typ = "Question (hidden)"
			}
			name := q.Name
			if name == "" {
				name = "Anonymous"
			}
			cw.Write(append([]string{web.CSVSafe(label), typ, q.At.In(loc).Format("2006-01-02 15:04"), web.CSVSafe(q.Text), "", strconv.Itoa(q.Votes), ans, web.CSVSafe(name)}, who(q.Who)...))
		}
		for _, x := range v.Ratings {
			name := x.Name
			if name == "" {
				name = "Anonymous"
			}
			cw.Write(append([]string{web.CSVSafe(label), "Rating", x.At.In(loc).Format("2006-01-02 15:04"), web.CSVSafe(x.Comment), strconv.Itoa(x.Stars), "", "", web.CSVSafe(name)}, who(x.Who)...))
		}
	}
	cw.Flush()
	return buf.Bytes(), cw.Error()
}

func (s *Server) handleSniffExport(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.sniffByToken(w, r)
	if !ok {
		return
	}
	data, err := s.writeSniffCSV([]SniffSession{ss})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	web.SendCSV(w, fileSafe("Sniff - "+ss.Title), data)
}

// --- Admin ------------------------------------------------------------------

// lumaEvent finds one of the calendar's events by id.
func (s *Server) lumaEvent(id string) (luma.Event, error) {
	events, err := luma.ListEvents(s.cfg.LumaAPIKey)
	if err != nil {
		return luma.Event{}, err
	}
	for _, e := range events {
		if e.APIId == id {
			return e, nil
		}
	}
	return luma.Event{}, errors.New("event not found on the Luma calendar")
}

type sniffRow struct {
	SniffSession
	When      string
	Link      string
	SpeakLink string
	Questions int
	Ratings   int
	Avg       string
}

func (s *Server) handleSniffAdmin(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{"Flash": r.URL.Query().Get("msg")}
	ev, err := s.lumaEvent(r.PathValue("event"))
	if err != nil {
		data["Error"] = "Luma: " + err.Error()
		// Without Luma, still show the sessions already made.
		if list := s.sniff.Sessions(r.PathValue("event")); len(list) > 0 {
			ev = luma.Event{APIId: list[0].EventID, Name: list[0].EventName, Timezone: list[0].EventTZ}
		}
	}
	if ev.APIId != "" && err == nil {
		items, serr := EventSchedule(sites.Repos(), ev.APIId, ev.Name, r.URL.Query().Get("refresh") != "")
		if serr != nil {
			data["SchedError"] = "The schedule couldn't be read from the event page: " + serr.Error()
		} else if added, err := s.sniff.SyncSchedule(ev, items); err != nil {
			data["SchedError"] = err.Error()
		} else if added > 0 {
			data["Flash"] = fmt.Sprintf("%d new %s from the schedule.", added, web.Plural(added, "session", "sessions"))
		}
	}
	base := s.baseURL(r)
	if ev.APIId != "" {
		ec, err := s.sniff.EventCode(ev.APIId)
		if err != nil {
			data["SchedError"] = err.Error()
		} else {
			data["EventLink"] = base + "/q/" + ec
			data["EventShort"] = strings.TrimPrefix(strings.TrimPrefix(base+"/q/"+ec, "https://"), "http://")
		}
	}
	var rows, hidden []sniffRow
	var tsv strings.Builder
	now := s.now()
	for _, ss := range s.sniff.Sessions(ev.APIId) {
		q, n, avg := s.sniff.Counts(ss.Code, now)
		row := sniffRow{SniffSession: ss, When: sessionWhen(ss), Link: base + "/q/" + ss.Code, SpeakLink: base + speakerPath(ss), Questions: q, Ratings: n}
		if n > 0 {
			row.Avg = strconv.FormatFloat(avg, 'f', 1, 64)
		}
		if ss.Hidden {
			hidden = append(hidden, row)
			continue
		}
		rows = append(rows, row)
		fmt.Fprintf(&tsv, "%s\t%s\t%s\n", ss.Who, ss.Title, row.SpeakLink)
	}
	data["Event"] = ev
	data["Brand"] = event.Brand(ev.Name)
	data["Rows"] = rows
	data["Hidden"] = hidden
	data["TSV"] = tsv.String()
	if start, end := ev.Times(); !start.IsZero() {
		data["Phase"], data["Until"] = event.Label(event.Window{Start: start, End: end}, ev.Timezone, now)
	}
	s.render(w, "sniff_admin.html", data)
}

func (s *Server) sniffAdminBack(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/admin/sniff/"+r.PathValue("event")+"?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

func (s *Server) handleSniffAdminRefresh(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin/sniff/"+r.PathValue("event")+"?refresh=1", http.StatusSeeOther)
}

func (s *Server) handleSniffAdminAdd(w http.ResponseWriter, r *http.Request) {
	ev, err := s.lumaEvent(r.PathValue("event"))
	if err != nil {
		s.sniffAdminBack(w, r, "Luma: "+err.Error())
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		s.sniffAdminBack(w, r, "A session needs a title.")
		return
	}
	ss, err := s.sniff.AddSession(ev, title, r.FormValue("who"))
	if err != nil {
		s.sniffAdminBack(w, r, err.Error())
		return
	}
	s.sniffAdminBack(w, r, "Added "+ss.Title+": /q/"+ss.Code)
}

func (s *Server) handleSniffAdminReset(w http.ResponseWriter, r *http.Request) {
	ss, err := s.sniff.ResetToken(r.PathValue("code"))
	if err != nil {
		s.sniffAdminBack(w, r, err.Error())
		return
	}
	s.sniffAdminBack(w, r, "New speaker link for "+ss.Who+". The old one no longer works; the PIN stays "+sniffPIN+".")
}

func (s *Server) handleSniffAdminHide(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.sniff.Session(r.PathValue("code"))
	if !ok {
		s.sniffAdminBack(w, r, "No such session.")
		return
	}
	if err := s.sniff.SetHidden(ss.Code, !ss.Hidden); err != nil {
		s.sniffAdminBack(w, r, err.Error())
		return
	}
	s.sniffAdminBack(w, r, "")
}

// handleSniffAdminQR and handleSniffAdminSlide are the event's one QR and
// slide.
func (s *Server) sniffEventLink(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	list := s.sniff.Sessions(r.PathValue("event"))
	ec, ok := s.sniff.HasEventCode(r.PathValue("event"))
	if !ok || len(list) == 0 {
		http.NotFound(w, r)
		return "", "", false
	}
	return s.baseURL(r) + "/q/" + ec, list[0].EventName, true
}

func (s *Server) handleSniffAdminQR(w http.ResponseWriter, r *http.Request) {
	link, name, ok := s.sniffEventLink(w, r)
	if !ok {
		return
	}
	img, err := qrcode.Encode(link, qrcode.Medium, 1024)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, no-store")
	if r.URL.Query().Get("dl") != "" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+fileSafe("Sniff QR - "+name)+`.png"`)
	}
	w.Write(img)
}

func (s *Server) handleSniffAdminSlide(w http.ResponseWriter, r *http.Request) {
	link, name, ok := s.sniffEventLink(w, r)
	if !ok {
		return
	}
	sendSniffSlide(w, r, name, link, "Sniff slide - "+name)
}

func (s *Server) handleSniffAdminCSV(w http.ResponseWriter, r *http.Request) {
	list := s.sniff.Sessions(r.PathValue("event"))
	if len(list) == 0 {
		http.NotFound(w, r)
		return
	}
	data, err := s.writeSniffCSV(list)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	web.SendCSV(w, fileSafe(list[0].EventName+" - Sniff questions and feedback"), data)
}
