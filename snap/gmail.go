package snap

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Gmail reads mark@sreday.com through the Gmail API, with the Google token
// Snoop already holds (Snoop asks for gmail.readonly on Connect Google).
// Reading mail this way costs nothing; only the excerpts Snap hands to
// Claude cost tokens.
type Gmail struct {
	Token func() (string, error)
	API   string // https://gmail.googleapis.com/gmail/v1/users/me (tests point it elsewhere)
	http  *http.Client
}

func NewGmail(token func() (string, error)) *Gmail {
	return &Gmail{Token: token, API: "https://gmail.googleapis.com/gmail/v1/users/me", http: &http.Client{Timeout: 60 * time.Second}}
}

// GmailError is a non-2xx answer from Gmail.
type GmailError struct {
	Status int
	Body   string
}

func (e *GmailError) Error() string {
	b := e.Body
	if len(b) > 300 {
		b = b[:300]
	}
	if e.Status == http.StatusForbidden && strings.Contains(b, "insufficient") {
		return "Gmail says Snap may not read mail yet: open Snoop, Google, Reconnect, and allow Gmail (read-only)"
	}
	return fmt.Sprintf("Gmail API %d: %s", e.Status, b)
}

func (g *Gmail) get(path string, q url.Values, out any) error {
	tok, err := g.Token()
	if err != nil {
		return err
	}
	u := g.API + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := g.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return &GmailError{resp.StatusCode, string(body)}
	}
	return json.Unmarshal(body, out)
}

// ThreadRef is one row of a thread search.
type ThreadRef struct {
	ID        string `json:"id"`
	HistoryID string `json:"historyId"`
}

// List returns every thread matching q, newest first.
func (g *Gmail) List(q string) ([]ThreadRef, error) {
	var out []ThreadRef
	page := ""
	for i := 0; i < 40; i++ { // 40 x 500 threads: a year of mail and then some
		v := url.Values{"q": {q}, "maxResults": {"500"}}
		if page != "" {
			v.Set("pageToken", page)
		}
		var r struct {
			Threads       []ThreadRef `json:"threads"`
			NextPageToken string      `json:"nextPageToken"`
		}
		if err := g.get("/threads", v, &r); err != nil {
			return out, err
		}
		out = append(out, r.Threads...)
		if r.NextPageToken == "" {
			break
		}
		page = r.NextPageToken
	}
	return out, nil
}

type gHeader struct{ Name, Value string }

type gPart struct {
	MimeType string    `json:"mimeType"`
	Headers  []gHeader `json:"headers"`
	Body     struct {
		Data string `json:"data"`
	} `json:"body"`
	Parts []gPart `json:"parts"`
}

type gMessage struct {
	ID           string   `json:"id"`
	LabelIDs     []string `json:"labelIds"`
	Snippet      string   `json:"snippet"`
	InternalDate string   `json:"internalDate"`
	Payload      gPart    `json:"payload"`
}

type gThread struct {
	ID        string     `json:"id"`
	HistoryID string     `json:"historyId"`
	Messages  []gMessage `json:"messages"`
}

var metaHeaders = []string{"From", "To", "Cc", "Subject", "List-Unsubscribe", "List-Id", "Precedence", "Auto-Submitted", "X-Autoreply"}

// Thread fetches a thread: headers and snippets only (full=false), or
// with the bodies.
func (g *Gmail) Thread(id string, full bool) (*ParsedThread, error) {
	v := url.Values{}
	if full {
		v.Set("format", "full")
	} else {
		v.Set("format", "metadata")
		for _, h := range metaHeaders {
			v.Add("metadataHeaders", h)
		}
	}
	var t gThread
	if err := g.get("/threads/"+url.PathEscape(id), v, &t); err != nil {
		return nil, err
	}
	return parseThread(t), nil
}

// ParsedThread is a Gmail thread reduced to what Snap needs.
type ParsedThread struct {
	ID        string
	HistoryID string
	Subject   string
	Mails     []Mail
}

// Mail is one message of a thread.
type Mail struct {
	FromMe    bool // sent by Mark (Gmail's SENT label)
	From      mail.Address
	To        []mail.Address
	At        time.Time
	Snippet   string
	Automated bool   // a newsletter, notification or auto-reply
	Text      string // the new text only: no quoted reply, no signature (full fetch)
}

func parseThread(t gThread) *ParsedThread {
	p := &ParsedThread{ID: t.ID, HistoryID: t.HistoryID}
	for _, m := range t.Messages {
		h := map[string]string{}
		for _, x := range m.Payload.Headers {
			h[strings.ToLower(x.Name)] = x.Value
		}
		if p.Subject == "" {
			p.Subject = strings.TrimSpace(h["subject"])
		}
		ms, _ := strconv.ParseInt(m.InternalDate, 10, 64)
		ml := Mail{At: time.UnixMilli(ms).UTC(), Snippet: html.UnescapeString(m.Snippet)}
		for _, l := range m.LabelIDs {
			if l == "SENT" {
				ml.FromMe = true
			}
		}
		if a, err := mail.ParseAddress(h["from"]); err == nil {
			ml.From = *a
		} else {
			ml.From = mail.Address{Address: strings.TrimSpace(h["from"])}
		}
		for _, k := range []string{"to", "cc"} {
			if l, err := mail.ParseAddressList(h[k]); err == nil {
				for _, a := range l {
					ml.To = append(ml.To, *a)
				}
			}
		}
		ml.Automated = automated(h, ml.From.Address)
		if text := bodyText(m.Payload); text != "" {
			ml.Text = newText(text)
		}
		p.Mails = append(p.Mails, ml)
	}
	return p
}

var robotFrom = regexp.MustCompile(`(?i)(^|[._+-])(no-?reply|do-?not-?reply|notifications?|notify|mailer-daemon|postmaster|bounce[s]?|alerts?|updates?|news(letter)?|digest|billing|invoices?|receipts?|calendar-notification)([._+-]|@)|@(luma-mail\.com|calendly\.com|github\.com|google\.com|linkedin\.com|zoom\.us|stripe\.com|eventbrite\.com|docusign\.net|slack\.com|hubspot\.com|intercom-mail\.com|mailchimp\.com)$`)

// automated: list mail, bulk, auto-replies and robots' addresses.
func automated(h map[string]string, from string) bool {
	switch {
	case h["list-unsubscribe"] != "", h["list-id"] != "":
		return true
	case strings.EqualFold(h["precedence"], "bulk"), strings.EqualFold(h["precedence"], "list"), strings.EqualFold(h["precedence"], "junk"):
		return true
	case h["auto-submitted"] != "" && !strings.EqualFold(h["auto-submitted"], "no"):
		return true
	case h["x-autoreply"] != "":
		return true
	}
	return robotFrom.MatchString(from)
}

// bodyText is the message's plain text (else its HTML, as text).
func bodyText(p gPart) string {
	if t := findPart(p, "text/plain"); t != "" {
		return t
	}
	if t := findPart(p, "text/html"); t != "" {
		return htmlToText(t)
	}
	return ""
}

func findPart(p gPart, mime string) string {
	if strings.HasPrefix(p.MimeType, mime) && p.Body.Data != "" {
		b, err := base64.URLEncoding.DecodeString(p.Body.Data)
		if err != nil {
			b, err = base64.RawURLEncoding.DecodeString(p.Body.Data)
		}
		if err == nil {
			return string(b)
		}
	}
	for _, c := range p.Parts {
		if t := findPart(c, mime); t != "" {
			return t
		}
	}
	return ""
}

var (
	tagRe      = regexp.MustCompile(`(?s)<(script|style|head)[^>]*>.*?</(script|style|head)>|<[^>]+>`)
	blockRe    = regexp.MustCompile(`(?i)<(br|/p|/div|/li|/tr|/h\d)[^>]*>`)
	quoteHead  = regexp.MustCompile(`(?im)^(\s*On .{6,200}wrote:\s*$|\s*Le .{6,200}a écrit\s*:|\s*Am .{6,200}schrieb.*:|-{2,}\s*Original Message\s*-{2,}|_{10,}|\s*From: .+\n\s*(Sent|Date): )`)
	wrappedOn  = regexp.MustCompile(`(?is)\n\s*On [^\n]{6,120}\n[^\n]{0,120}wrote:\s*\n`)
	sigRe      = regexp.MustCompile(`(?m)^-- ?$`)
	blankLines = regexp.MustCompile(`\n{3,}`)
	spaces     = regexp.MustCompile(`[ \t\x{a0}]+`)
)

func htmlToText(s string) string {
	s = blockRe.ReplaceAllString(s, "\n")
	s = tagRe.ReplaceAllString(s, "")
	return html.UnescapeString(s)
}

// newText keeps what this message adds: the quoted thread underneath
// ("On ... wrote:", "> ...") and the signature go.
func newText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if loc := wrappedOn.FindStringIndex(s); loc != nil {
		s = s[:loc[0]]
	}
	if loc := quoteHead.FindStringIndex(s); loc != nil {
		s = s[:loc[0]]
	}
	if loc := sigRe.FindStringIndex(s); loc != nil {
		s = s[:loc[0]]
	}
	var keep []string
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), ">") {
			continue
		}
		keep = append(keep, strings.TrimSpace(spaces.ReplaceAllString(l, " ")))
	}
	s = strings.Join(keep, "\n")
	s = blankLines.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}
