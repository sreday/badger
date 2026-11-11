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
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

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
//	/sniff/<secret>/<name> the speaker: questions and feedback live, the QR, the slide
//	/sniff/<secret>       the organizers: every session's links
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
	// A plain form (no JavaScript) sends the question and the rating together.
	if in.Form && in.Stars >= 1 && in.Stars <= 3 {
		name := in.Name
		if harmful(name) {
			name = ""
		}
		if err := s.sniff.Rate(ss.Code, s.now(), randomID(), in.Stars, "", name, s.sniffWho(ss, name)); err != nil {
			sniffReply(w, r, in, http.StatusInternalServerError, "That didn't save, please try again.")
			return
		}
		if in.Text == "" {
			sniffReply(w, r, in, http.StatusOK, "")
			return
		}
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
		// Never stored, never reaches the speaker; the asker's phone is told,
		// so it can say "You can do better than this".
		if in.Form {
			sniffReply(w, r, in, http.StatusOK, "")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Write([]byte(`{"ok":true,"filtered":true}`))
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
// asker typed (never the matched surname or company), votes, and the
// presenter's heart. The presenter's phone also gets the hidden ones.
type audienceQ struct {
	ID       string `json:"id"`
	Text     string `json:"t"`
	Name     string `json:"n,omitempty"`
	Votes    int    `json:"v"`
	Answered bool   `json:"a,omitempty"`
	Heart    bool   `json:"i,omitempty"`
	Hidden   bool   `json:"h,omitempty"`
	Test     bool   `json:"x,omitempty"`
	At       int64  `json:"at"`
}

func firstWord(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}

// audienceFeed is the room's view of a session; the presenter's adds the
// hidden questions.
func (s *Server) audienceFeed(ss SniffSession, voter string, presenter bool, my []string) map[string]any {
	now := s.now()
	v := s.sniff.View(ss.Code, now)
	qs := []audienceQ{}
	// The room never gets anyone's questions: each phone shows its own. A
	// phone only learns which of its own (by their ids) the speaker hearted.
	mine := map[string]bool{}
	for _, id := range my {
		mine[id] = true
	}
	hearts := []string{}
	for _, q := range v.Questions {
		if !presenter {
			if q.Important && mine[q.ID] {
				hearts = append(hearts, q.ID)
			}
			continue
		}
		qs = append(qs, audienceQ{ID: q.ID, Text: q.Text, Name: firstWord(q.Name), Heart: q.Important, Hidden: q.Hidden, Test: q.Test, At: q.At.Unix()})
	}
	return map[string]any{"v": strconv.FormatInt(v.Version, 36), "phase": ss.Phase(now), "open": ss.Open(now), "qs": qs, "hearts": hearts}
}

// myIDs is the phone's own question ids from ?my=a,b,c (at most 50, each a
// valid random id).
func myIDs(r *http.Request) []string {
	var out []string
	for _, id := range strings.Split(r.URL.Query().Get("my"), ",") {
		if validKey(id) && len(out) < 50 {
			out = append(out, id)
		}
	}
	return out
}

func (s *Server) handleSniffFeed(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.sniff.Session(r.PathValue("code"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if !s.newer(r, ss.Code) {
		w.WriteHeader(http.StatusNoContent) // nothing new: no body at all
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.audienceFeed(ss, r.URL.Query().Get("k"), r.URL.Query().Get("p") != "" && hasSpeakerPIN(r), myIDs(r)))
}

func (s *Server) handleSniffAudience(w http.ResponseWriter, r *http.Request) {
	code := strings.ToUpper(r.PathValue("code"))
	if eventID, ok := s.sniff.EventByCode(code); ok {
		// The old one-QR-per-event link: talks have their own QR now, so
		// nobody asks about (or rates) a talk they didn't see.
		name := ""
		if list := s.sniff.Sessions(eventID); len(list) > 0 {
			name = list[0].EventName
		}
		s.render(w, "sniff_event.html", map[string]any{"Name": name, "Brand": event.Brand(name)})
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
	presenter := hasSpeakerPIN(r)
	init := s.audienceFeed(ss, "", presenter, nil)
	s.render(w, "sniff_ask.html", map[string]any{
		"S":         ss,
		"Brand":     event.Brand(ss.EventName),
		"Conf":      conferenceName(ss.EventName),
		"Init":      init,
		"Event":     ss.EventID,
		"Sent":      r.URL.Query().Get("sent") != "",
		"Err":       r.URL.Query().Get("err"),
		"Open":      ss.Open(now),
		"Test":      ss.Phase(now) == event.PhaseTest,
		"Speaker":   firstWord(ss.Who),
		"Max":       maxQuestion,
		"PinErr":    r.URL.Query().Get("pin"),
		"Presenter": presenter,
	})
}

// handleSniffPin is "I'm the speaker": the speakers' PIN turns this page
// into the presenter's view (hearts and hides), on this phone, for a year.
func (s *Server) handleSniffPin(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.sniff.Session(r.PathValue("code"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if strings.TrimSpace(r.FormValue("pin")) != sniffPIN {
		http.Redirect(w, r, "/q/"+ss.Code+"?pin="+url.QueryEscape("That's not the speakers' PIN.")+"#presenter", http.StatusSeeOther)
		return
	}
	setSpeakerPIN(w, r)
	http.Redirect(w, r, "/q/"+ss.Code, http.StatusSeeOther)
}

// presenterMods are all the PIN allows on the phone: a heart (top of the
// list) and hiding a question. Nothing to edit, nothing to download.
var presenterMods = map[string]bool{"important": true, "unimportant": true, "hide": true, "show": true}

func (s *Server) handleSniffPresenterMod(w http.ResponseWriter, r *http.Request) {
	if !hasSpeakerPIN(r) {
		http.Error(w, "Enter the speakers' PIN first", http.StatusForbidden)
		return
	}
	ss, ok := s.sniff.Session(r.PathValue("code"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	var in struct{ Q, Mod string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil || !presenterMods[in.Mod] {
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

// sniffByToken is the session behind a speaker page's token (its feed,
// CSV, hearts), once the PIN is in; refused without it.
func (s *Server) sniffByToken(w http.ResponseWriter, r *http.Request) (SniffSession, bool) {
	ss, ok := s.sniff.ByToken(r.PathValue("token"))
	if !ok {
		http.NotFound(w, r)
		return ss, false
	}
	return s.speakerPINGate(w, r, ss, false)
}

// speakerPINGate lets the request through once the PIN is in. Without it
// the page itself asks for the PIN and anything else is refused.
func (s *Server) speakerPINGate(w http.ResponseWriter, r *http.Request, ss SniffSession, page bool) (SniffSession, bool) {
	if !hasSpeakerPIN(r) {
		if page {
			w.WriteHeader(http.StatusUnauthorized)
			s.render(w, "sniff_pin.html", map[string]any{"S": ss, "Brand": event.Brand(ss.EventName), "Conf": conferenceName(ss.EventName),
				"Link": ss.Token, "Err": r.URL.Query().Get("pin")})
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
	ss, ok := s.sniff.ByToken(link)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if strings.TrimSpace(r.FormValue("pin")) != sniffPIN {
		http.Redirect(w, r, s.speakerPath(ss)+"?pin="+url.QueryEscape("That's not the speakers' PIN. The organisers have it."), http.StatusSeeOther)
		return
	}
	setSpeakerPIN(w, r)
	http.Redirect(w, r, s.speakerPath(ss), http.StatusSeeOther)
}

// speakerPath is the speaker's desktop page: /sniff/<event secret>/grace
// (the event gets its secret here if it has none yet).
func (s *Server) speakerPath(ss SniffSession) string {
	if sec, err := s.sniff.EventSecret(ss.EventID); err == nil {
		if name := s.sniff.SpeakerNames(ss.EventID)[ss.Code]; name != "" {
			return "/sniff/" + sec + "/" + name
		}
	}
	return "/sniff/" + ss.Token
}

// audienceURL is what the talk's QR opens: its own /q/<code>, so the room
// can only ask about the talk in front of it.
func (s *Server) audienceURL(r *http.Request, ss SniffSession) string {
	return s.baseURL(r) + "/q/" + ss.Code
}

// handleSniffLink is /sniff/<one word>: an event's secret opens the
// event's page (anyone with the link; the link is never listed). Older
// speaker links: the 128-bit token goes to the speaker's page (PIN there),
// a first-name link (/sniff/grace, guessable) only to the talk's audience
// page, never to the secret.
func (s *Server) handleSniffLink(w http.ResponseWriter, r *http.Request) {
	link := r.PathValue("token")
	if eventID, ok := s.sniff.EventBySecret(link); ok {
		s.handleSniffAdmin(w, r, eventID)
		return
	}
	if ss, ok := s.sniff.ByToken(link); ok {
		http.Redirect(w, r, s.speakerPath(ss), http.StatusPermanentRedirect)
		return
	}
	if ss, ok := s.sniff.BySlug(link, s.now()); ok {
		http.Redirect(w, r, "/q/"+ss.Code, http.StatusSeeOther)
		return
	}
	http.NotFound(w, r)
}

// handleSniffSpeakerByName is /sniff/<event secret>/<speaker name>: the
// speaker's desktop page, behind the PIN (it's in the speakers' email).
func (s *Server) handleSniffSpeakerByName(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.sniff.BySecretName(r.PathValue("secret"), r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if ss, ok = s.speakerPINGate(w, r, ss, true); !ok {
		return
	}
	s.renderSpeaker(w, r, ss)
}

func (s *Server) renderSpeaker(w http.ResponseWriter, r *http.Request, ss SniffSession) {
	now := s.now()
	card := speakerCard{Who: ss.Who, Title: ss.Title, Phase: string(ss.Phase(now)), Initials: initials(ss.Who)}
	if card.Who == "" {
		card.Who, card.Title = ss.Title, ""
	}
	if sh, ok := speakerShard(ss); ok {
		card.Photo, card.LinkedIn, card.LinkedIn2 = sh.Photo, sh.LinkedIn, sh.LinkedIn2
	}
	back := ""
	if sec, ok := s.sniff.HasEventSecret(ss.EventID); ok {
		back = "/sniff/" + sec
	}
	s.render(w, "sniff_speaker.html", map[string]any{
		"Card":  card,
		"S":     ss,
		"Brand": event.Brand(ss.EventName),
		"Conf":  conferenceName(ss.EventName),
		"When":  sessionWhen(ss),
		"Init":  s.speakerFeed(ss),
		"Back":  back,
		"Dash":  s.speakerPath(ss),
		"PIN":   sniffPIN,
		"Test":  ss.Phase(now) == event.PhaseTest,
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

// attendee is who asked, as the speaker gets it after the event: the
// registration's details, the same as sponsors get.
type attendee struct {
	First    string `json:"fn,omitempty"`
	Last     string `json:"ln,omitempty"`
	Email    string `json:"e,omitempty"`
	Title    string `json:"t,omitempty"`
	Company  string `json:"c,omitempty"`
	LinkedIn string `json:"li,omitempty"`
}

// attendeeOf is a matched asker's details (T&C 16.1(b)(ix): name, email,
// job title, company, LinkedIn, the same as sponsors get).
func (s *Server) attendeeOf(ss SniffSession, w *Who, guests map[string]luma.Guest) *attendee {
	if w == nil {
		return nil
	}
	a := &attendee{Title: w.Title, Company: w.Company, LinkedIn: w.LinkedIn}
	a.First, a.Last, _ = strings.Cut(w.Name, " ")
	if g, ok := guests[w.Guest]; ok && w.Guest != "" {
		if g.FirstName != "" || g.LastName != "" {
			a.First, a.Last = g.FirstName, g.LastName
		}
		a.Email = g.Email
	}
	return a
}

// guestsByID is the event's guest list by guest id.
func (s *Server) guestsByID(eventID string) map[string]luma.Guest {
	m := map[string]luma.Guest{}
	for _, g := range guestsOf(s.host.EventGuests(eventID)) {
		m[g.ID] = g
	}
	return m
}

// speakerQ is a question as the speaker's desktop page sees it: who asked
// (the attendee the typed name matched), votes, the heart.
type speakerQ struct {
	ID     string    `json:"id"`
	Text   string    `json:"t"`
	Name   string    `json:"n,omitempty"`
	Who    *attendee `json:"who,omitempty"`
	Votes  int       `json:"v"`
	Hidden bool      `json:"h,omitempty"`
	Heart  bool      `json:"i,omitempty"`
	Test   bool      `json:"x,omitempty"`
	At     int64     `json:"at"`
}

type speakerR struct {
	Stars   int       `json:"s"`
	Comment string    `json:"c,omitempty"`
	Name    string    `json:"n,omitempty"`
	Who     *attendee `json:"who,omitempty"`
	Test    bool      `json:"x,omitempty"`
	At      int64     `json:"at"`
}

func (s *Server) speakerFeed(ss SniffSession) map[string]any {
	now := s.now()
	v := s.sniff.View(ss.Code, now)
	guests := s.guestsByID(ss.EventID)
	who := func(w *Who) *attendee { return s.attendeeOf(ss, w, guests) }
	qs := []speakerQ{}
	for _, q := range v.Questions {
		qs = append(qs, speakerQ{ID: q.ID, Text: q.Text, Name: q.Name, Who: who(q.Who), Votes: q.Votes, Hidden: q.Hidden, Heart: q.Important, Test: q.Test, At: q.At.Unix()})
	}
	rs := []speakerR{}
	for _, x := range v.Ratings {
		rs = append(rs, speakerR{Stars: x.Stars, Comment: x.Comment, Name: x.Name, Who: who(x.Who), Test: x.Test, At: x.At.Unix()})
	}
	return map[string]any{"v": strconv.FormatInt(v.Version, 36), "phase": ss.Phase(now), "qs": qs, "rs": rs}
}

func (s *Server) handleSniffSpeakerFeed(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.sniffByToken(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if !s.newer(r, ss.Code) {
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
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil || !presenterMods[in.Mod] {
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

func (s *Server) handleSniffQR(w http.ResponseWriter, r *http.Request, ss SniffSession) {
	size := 1024
	if n, err := strconv.Atoi(r.URL.Query().Get("size")); err == nil && n >= 128 && n <= 2048 {
		size = n
	}
	// The head in the middle, the logo's corners round it; ?t=1 transparent
	// (keyed out: no white at all, to paste onto a slide).
	q := r.URL.Query()
	qr, err := pkg.SniffQR(s.audienceURL(r, ss), size, pkg.SniffQROpts{Transparent: q.Get("t") != ""})
	var buf bytes.Buffer
	if err == nil {
		err = png.Encode(&buf, qr)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, no-store")
	if r.URL.Query().Get("dl") != "" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+fileSafe("Sniff QR - "+talkLabel(ss))+`.png"`)
	}
	w.Write(buf.Bytes())
}

// talkLabel is "Grace Hopper - Bugs" (or the title alone).
func talkLabel(ss SniffSession) string {
	if ss.Who == "" {
		return ss.Title
	}
	return ss.Who + " - " + ss.Title
}

func (s *Server) handleSniffSlide(w http.ResponseWriter, r *http.Request, ss SniffSession) {
	sendSniffSlide(w, r, pkg.SniffSlide{Brand: event.Brand(ss.EventName), Title: ss.Title, Speaker: ss.Who}, s.audienceURL(r, ss), "Sniff slide - "+talkLabel(ss))
}

// sendSniffSlide draws a slide for the deck: the talk's own QR, so the room
// can only ask about the talk in front of it.
func sendSniffSlide(w http.ResponseWriter, r *http.Request, slide pkg.SniffSlide, link, file string) {
	slide.URL = link
	slide.Short = strings.TrimPrefix(strings.TrimPrefix(link, "https://"), "http://")
	img, err := pkg.GenerateSniffSlide(slide)
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

var sniffCSVHeader = []string{"Session", "Type", "Time", "Question / comment", "Stars", "Hearted", "Asked as",
	"First name", "Last name", "Email", "Job title", "Company", "LinkedIn"}

// writeSniffCSV writes sessions' questions and ratings with who asked: the
// sponsor-ready fields of the matched attendee, at any time.
func (s *Server) writeSniffCSV(sessions []SniffSession) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("\xef\xbb\xbf") // Excel reads UTF-8 names right
	cw := csv.NewWriter(&buf)
	cw.Write(sniffCSVHeader)
	now := s.now()
	guests := map[string]map[string]luma.Guest{}
	for _, ss := range sessions {
		loc, err := time.LoadLocation(ss.EventTZ)
		if err != nil || ss.EventTZ == "" {
			loc = time.UTC
		}
		if guests[ss.EventID] == nil {
			guests[ss.EventID] = s.guestsByID(ss.EventID)
		}
		who := func(w *Who) []string {
			a := s.attendeeOf(ss, w, guests[ss.EventID])
			if a == nil {
				return []string{"", "", "", "", "", ""}
			}
			return []string{web.CSVSafe(a.First), web.CSVSafe(a.Last), web.CSVSafe(a.Email), web.CSVSafe(a.Title), web.CSVSafe(a.Company), a.LinkedIn}
		}
		asked := func(name string) string {
			if name == "" {
				return "Anonymous"
			}
			return web.CSVSafe(name)
		}
		v := s.sniff.View(ss.Code, now)
		qs := v.Questions
		sort.SliceStable(qs, func(i, j int) bool {
			if qs[i].Important != qs[j].Important {
				return qs[i].Important
			}
			return qs[i].At.Before(qs[j].At)
		})
		for _, q := range qs {
			typ, heart := "Question", ""
			if q.Hidden {
				typ = "Question (hidden)"
			}
			if q.Important {
				heart = "yes"
			}
			cw.Write(append([]string{web.CSVSafe(talkLabel(ss)), typ, q.At.In(loc).Format("2006-01-02 15:04"), web.CSVSafe(q.Text), "", heart, asked(q.Name)}, who(q.Who)...))
		}
		for _, x := range v.Ratings {
			cw.Write(append([]string{web.CSVSafe(talkLabel(ss)), "Rating", x.At.In(loc).Format("2006-01-02 15:04"), web.CSVSafe(x.Comment), strconv.Itoa(x.Stars), "", asked(x.Name)}, who(x.Who)...))
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

// speakerCard is one speaker on the event page: the card the event's
// website draws, and their links.
type speakerCard struct {
	Who, Title, Photo, LinkedIn, LinkedIn2, Initials, Phase string
	Web                                                     int    // place in the website's schedule
	First, Last, Search                                     string // for sorting and the search bar
	Dash                                                    string // /sniff/<secret>/<name>, the desktop page
}

func initials(who string) string {
	var out []rune
	for _, w := range strings.Fields(who) {
		if r := []rune(w); len(r) > 0 && len(out) < 2 && unicode.IsLetter(r[0]) {
			out = append(out, unicode.ToUpper(r[0]))
		}
	}
	return string(out)
}

// handleSniffAdmin is the event's page, /sniff/<secret>: the speakers'
// tutorial and every speaker's card, for anyone with the link. The
// speaker's own page behind each card asks for the PIN.
func (s *Server) handleSniffAdmin(w http.ResponseWriter, r *http.Request, eventID string) {
	data := map[string]any{}
	ev, err := s.lumaEvent(eventID)
	if err != nil {
		// Without Luma, still show the sessions already made.
		if list := s.sniff.Sessions(eventID); len(list) > 0 {
			ev = luma.Event{APIId: list[0].EventID, Name: list[0].EventName, Timezone: list[0].EventTZ}
		}
	}
	if ev.APIId == "" {
		http.NotFound(w, r)
		return
	}
	if err == nil {
		if items, serr := EventSchedule(sites.Repos(), ev.APIId, ev.Name, false); serr == nil {
			if _, err := s.sniff.SyncSchedule(ev, items); err != nil {
				log.Printf("Sniff: schedule for %s: %v", ev.Name, err)
			}
		}
	}
	now := s.now()
	var cards []speakerCard
	for i, ss := range s.sniff.Sessions(ev.APIId) {
		if ss.Hidden || ss.Gone {
			continue
		}
		c := speakerCard{Who: ss.Who, Title: ss.Title, Phase: string(ss.Phase(now)), Web: i, Dash: s.speakerPath(ss), Initials: initials(ss.Who)}
		if c.Who == "" {
			c.Who, c.Title = ss.Title, ""
		}
		if sh, ok := speakerShard(ss); ok {
			c.Photo, c.LinkedIn, c.LinkedIn2 = sh.Photo, sh.LinkedIn, sh.LinkedIn2
		}
		f := strings.Fields(foldName(c.Who))
		if len(f) > 0 {
			c.First, c.Last = f[0], f[len(f)-1]
		}
		c.Search = foldName(c.Who + " " + c.Title + " " + ss.Org)
		cards = append(cards, c)
	}
	data["Event"] = ev
	data["Brand"] = event.Brand(ev.Name)
	data["Cards"] = cards
	data["PIN"] = sniffPIN
	data["Secret"], _ = s.sniff.HasEventSecret(ev.APIId)
	if start, end := ev.Times(); !start.IsZero() {
		phase, _ := event.Label(event.Window{Start: start, End: end}, ev.Timezone, now)
		data["Test"] = phase == event.PhaseTest
	}
	s.render(w, "sniff_admin.html", data)
}

func (s *Server) sniffAdminBack(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, s.eventPagePath(r.PathValue("event"))+"?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

func (s *Server) handleSniffAdminRefresh(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, s.eventPagePath(r.PathValue("event"))+"?refresh=1", http.StatusSeeOther)
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

// handleSniffLogout is the speaker view's "Log out": the PIN cookie goes
// (on this phone, for every talk) and the phone is back on the question
// form; "I'm the speaker" asks for the PIN again.
func (s *Server) handleSniffLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sniffCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https", SameSite: http.SameSiteLaxMode})
	code := strings.ToUpper(r.PathValue("code"))
	if ss, ok := s.sniff.Session(code); ok {
		code = ss.Code
	}
	http.Redirect(w, r, "/q/"+url.PathEscape(code), http.StatusSeeOther)
}

// longPoll is how long a feed request with wait=1 is held open when
// nothing has changed: a new question reaches the speaker the moment it's
// asked, without polling every few seconds.
var longPoll = 25 * time.Second

// newer says whether the session changed since the version the page has
// (?v=). With ?wait=1 it waits for a change, up to longPoll.
func (s *Server) newer(r *http.Request, code string) bool {
	have := r.URL.Query().Get("v")
	cur := func() bool { return have != strconv.FormatInt(s.sniff.Version(code, s.now()), 36) }
	if have == "" || cur() {
		return true
	}
	if r.URL.Query().Get("wait") == "" {
		return false
	}
	deadline := time.NewTimer(longPoll)
	defer deadline.Stop()
	for {
		ch := s.sniff.Changed()
		if cur() {
			return true
		}
		select {
		case <-ch:
			if cur() {
				return true
			}
		case <-deadline.C:
			return false
		case <-r.Context().Done():
			return false
		}
	}
}
