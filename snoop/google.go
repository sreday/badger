package snoop

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Google endpoints; tests point them at a fake server.
var (
	authURL     = "https://accounts.google.com/o/oauth2/v2/auth"
	tokenURL    = "https://oauth2.googleapis.com/token"
	calendarAPI = "https://www.googleapis.com/calendar/v3"
	meetAPI     = "https://meet.googleapis.com/v2"
	driveAPI    = "https://www.googleapis.com/drive/v3"
	uploadAPI   = "https://www.googleapis.com/upload/drive/v3"
	userinfoAPI = "https://openidconnect.googleapis.com/v1/userinfo"
)

// Scopes Snoop asks for once, on "Connect Google".
var scopes = []string{
	"openid", "email",
	"https://www.googleapis.com/auth/calendar.readonly",
	"https://www.googleapis.com/auth/meetings.space.readonly", // conference records, transcripts, notes
	"https://www.googleapis.com/auth/meetings.space.settings", // switch on transcripts + Gemini notes for Marek's own Meets
	"https://www.googleapis.com/auth/drive.readonly",          // read Meet's transcript + Gemini notes docs
	"https://www.googleapis.com/auth/drive.file",              // write Snoop's own docs
}

// ErrNotConnected means nobody has clicked "Connect Google" yet.
var ErrNotConnected = errors.New("Google isn't connected: open /admin and click Connect Google")

// APIError is a non-2xx answer from Google.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Google API %d: %s", e.Status, truncate(e.Body, 300))
}

func isStatus(err error, codes ...int) bool {
	var ae *APIError
	if !errors.As(err, &ae) {
		return false
	}
	for _, c := range codes {
		if ae.Status == c {
			return true
		}
	}
	return false
}

// googleToken is what's kept on disk (root-only): the refresh token is the
// whole of Snoop's access to Marek's Google account.
type googleToken struct {
	RefreshToken string    `json:"refresh_token"`
	Email        string    `json:"email"`
	Connected    time.Time `json:"connected"`
	FolderID     string    `json:"folder_id,omitempty"` // the Snoop folder on Drive
}

// Google talks to Calendar, Meet and Drive as the connected account.
type Google struct {
	clientID, clientSecret string
	path                   string
	http                   *http.Client

	mu      sync.Mutex
	tok     googleToken
	access  string
	expires time.Time
}

func NewGoogle(clientID, clientSecret, dataDir string) (*Google, error) {
	g := &Google{
		clientID:     clientID,
		clientSecret: clientSecret,
		path:         filepath.Join(dataDir, "snoop", "google.json"),
		http:         &http.Client{Timeout: 60 * time.Second},
	}
	data, err := os.ReadFile(g.path)
	if errors.Is(err, os.ErrNotExist) {
		return g, nil
	}
	if err != nil {
		return nil, err
	}
	return g, json.Unmarshal(data, &g.tok)
}

// Connected returns the connected account's email, or "".
func (g *Google) Connected() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.tok.RefreshToken == "" {
		return ""
	}
	return g.tok.Email
}

// AuthURL is where "Connect Google" sends the browser.
func (g *Google) AuthURL(redirect, state, login string) string {
	v := url.Values{
		"client_id":     {g.clientID},
		"redirect_uri":  {redirect},
		"response_type": {"code"},
		"scope":         {strings.Join(scopes, " ")},
		"access_type":   {"offline"},
		"prompt":        {"consent"}, // always hand back a refresh token
		"state":         {state},
		"login_hint":    {login},
	}
	return authURL + "?" + v.Encode()
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func (g *Google) postToken(v url.Values) (tokenResponse, error) {
	v.Set("client_id", g.clientID)
	v.Set("client_secret", g.clientSecret)
	resp, err := g.http.PostForm(tokenURL, v)
	if err != nil {
		return tokenResponse{}, err
	}
	defer resp.Body.Close()
	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return tr, fmt.Errorf("token answer: %w", err)
	}
	if tr.Error != "" || tr.AccessToken == "" {
		return tr, fmt.Errorf("token: %s %s", tr.Error, tr.ErrorDesc)
	}
	return tr, nil
}

// Exchange finishes "Connect Google": swaps the code for tokens and saves
// the refresh token.
// Only the traced account is accepted: another one would trace the wrong calls.
func (g *Google) Exchange(code, redirect, want string) (string, error) {
	tr, err := g.postToken(url.Values{
		"code":         {code},
		"redirect_uri": {redirect},
		"grant_type":   {"authorization_code"},
	})
	if err != nil {
		return "", err
	}
	if tr.RefreshToken == "" {
		return "", errors.New("Google sent no refresh token; remove Snoop at myaccount.google.com/permissions and connect again")
	}
	// Asked with the new access token directly: nothing is saved until we
	// know it's the right account.
	var who struct {
		Email string `json:"email"`
	}
	data, err := g.doWith(tr.AccessToken, "GET", userinfoAPI, "", nil)
	if err == nil {
		err = json.Unmarshal(data, &who)
	}
	if err != nil {
		return "", fmt.Errorf("reading the account's email: %w", err)
	}
	if want != "" && !strings.EqualFold(who.Email, want) {
		return "", fmt.Errorf("signed in as %s: Snoop traces %s, so connect with that account", who.Email, want)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	folder := g.tok.FolderID
	if g.tok.Email != who.Email {
		folder = "" // another account's Drive
	}
	g.tok = googleToken{RefreshToken: tr.RefreshToken, Email: who.Email, Connected: time.Now().UTC(), FolderID: folder}
	g.access, g.expires = tr.AccessToken, time.Now().Add(time.Duration(tr.ExpiresIn)*time.Second-time.Minute)
	return who.Email, g.saveLocked()
}

func (g *Google) saveLocked() error {
	data, err := json.MarshalIndent(g.tok, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(g.path, data)
}

func (g *Google) accessToken() (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.tok.RefreshToken == "" {
		return "", ErrNotConnected
	}
	if g.access != "" && time.Now().Before(g.expires) {
		return g.access, nil
	}
	tr, err := g.postToken(url.Values{
		"refresh_token": {g.tok.RefreshToken},
		"grant_type":    {"refresh_token"},
	})
	if err != nil {
		return "", fmt.Errorf("refreshing Google access (connect again on /admin if this keeps failing): %w", err)
	}
	g.access, g.expires = tr.AccessToken, time.Now().Add(time.Duration(tr.ExpiresIn)*time.Second-time.Minute)
	return g.access, nil
}

func (g *Google) do(method, u, contentType string, body io.Reader) ([]byte, error) {
	tok, err := g.accessToken()
	if err != nil {
		return nil, err
	}
	return g.doWith(tok, method, u, contentType, body)
}

func (g *Google) doWith(tok, method, u, contentType string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequest(method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 50<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, &APIError{Status: resp.StatusCode, Body: string(data)}
	}
	return data, nil
}

func (g *Google) getJSON(u string, out any) error {
	data, err := g.do("GET", u, "", nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func (g *Google) sendJSON(method, u string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	data, err := g.do(method, u, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// --- Calendar --------------------------------------------------------------

// CalEvent is the part of a Calendar event Snoop uses.
type CalEvent struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Summary     string `json:"summary"`
	HangoutLink string `json:"hangoutLink"`
	Description string `json:"description"`
	Start       struct {
		DateTime time.Time `json:"dateTime"`
	} `json:"start"`
	End struct {
		DateTime time.Time `json:"dateTime"`
	} `json:"end"`
	Organizer struct {
		Email string `json:"email"`
		Self  bool   `json:"self"`
	} `json:"organizer"`
	Attendees []struct {
		Email          string `json:"email"`
		DisplayName    string `json:"displayName"`
		Self           bool   `json:"self"`
		ResponseStatus string `json:"responseStatus"`
		Resource       bool   `json:"resource"`
	} `json:"attendees"`
	ConferenceData struct {
		ConferenceID       string `json:"conferenceId"`
		ConferenceSolution struct {
			Key struct {
				Type string `json:"type"`
			} `json:"key"`
		} `json:"conferenceSolution"`
	} `json:"conferenceData"`
	Attachments []struct {
		FileID   string `json:"fileId"`
		FileURL  string `json:"fileUrl"`
		Title    string `json:"title"`
		MimeType string `json:"mimeType"`
	} `json:"attachments"`
}

// MeetingCode returns the event's Meet code ("abc-defg-hij"), or "".
func (e *CalEvent) MeetingCode() string {
	if e.ConferenceData.ConferenceSolution.Key.Type == "hangoutsMeet" && e.ConferenceData.ConferenceID != "" {
		return e.ConferenceData.ConferenceID
	}
	return meetingCodeFromURL(e.HangoutLink)
}

func meetingCodeFromURL(u string) string {
	m := meetCodeRe.FindStringSubmatch(u)
	if m == nil {
		return ""
	}
	return m[1]
}

// Events lists the calendar's events between from and to, recurring ones
// expanded. Cancelled instances are included so their traces get marked.
func (g *Google) Events(calendarID string, from, to time.Time) ([]CalEvent, error) {
	var all []CalEvent
	page := ""
	for {
		v := url.Values{
			"timeMin":      {from.UTC().Format(time.RFC3339)},
			"timeMax":      {to.UTC().Format(time.RFC3339)},
			"singleEvents": {"true"},
			"showDeleted":  {"true"},
			"maxResults":   {"250"},
		}
		if page != "" {
			v.Set("pageToken", page)
		}
		var resp struct {
			Items         []CalEvent `json:"items"`
			NextPageToken string     `json:"nextPageToken"`
		}
		if err := g.getJSON(calendarAPI+"/calendars/"+url.PathEscape(calendarID)+"/events?"+v.Encode(), &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Items...)
		if resp.NextPageToken == "" {
			return all, nil
		}
		page = resp.NextPageToken
	}
}

// --- Meet ------------------------------------------------------------------

// Space is a Meet meeting space.
type Space struct {
	Name        string `json:"name"` // spaces/{id}
	MeetingCode string `json:"meetingCode"`
	Config      struct {
		ArtifactConfig struct {
			TranscriptionConfig struct {
				AutoTranscriptionGeneration string `json:"autoTranscriptionGeneration"`
			} `json:"transcriptionConfig"`
			SmartNotesConfig struct {
				AutoSmartNotesGeneration string `json:"autoSmartNotesGeneration"`
			} `json:"smartNotesConfig"`
		} `json:"artifactConfig"`
	} `json:"config"`
}

// Space looks a Meet up by its meeting code.
func (g *Google) Space(code string) (Space, error) {
	var sp Space
	err := g.getJSON(meetAPI+"/spaces/"+url.PathEscape(code), &sp)
	return sp, err
}

// Arm switches on automatic transcripts, and Gemini notes where the account
// has them, for a space Marek owns. Meet shows its usual banners to
// everyone in the call. notes is false when Gemini notes aren't available
// to the account (the extension's notes stand in).
func (g *Google) Arm(spaceName string) (notes bool, err error) {
	const transcript = "config.artifactConfig.transcriptionConfig.autoTranscriptionGeneration"
	const smartNotes = "config.artifactConfig.smartNotesConfig.autoSmartNotesGeneration"
	patch := func(mask string, ac map[string]any) error {
		body := map[string]any{"config": map[string]any{"artifactConfig": ac}}
		return g.sendJSON("PATCH", meetAPI+"/"+spaceName+"?updateMask="+url.QueryEscape(mask), body, nil)
	}
	err = patch(transcript+","+smartNotes, map[string]any{
		"transcriptionConfig": map[string]string{"autoTranscriptionGeneration": "ON"},
		"smartNotesConfig":    map[string]string{"autoSmartNotesGeneration": "ON"},
	})
	if err == nil || !featureUnavailable(err) {
		return err == nil, err
	}
	return false, patch(transcript, map[string]any{
		"transcriptionConfig": map[string]string{"autoTranscriptionGeneration": "ON"},
	})
}

// featureUnavailable: the account's Workspace plan or admin settings don't
// allow it (Google says FEATURE_UNAVAILABLE_TO_USER), as opposed to not
// owning the Meet.
func featureUnavailable(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && strings.Contains(ae.Body, "FEATURE_UNAVAILABLE_TO_USER")
}

// Conference is one conference record (one sitting of a Meet).
type Conference struct {
	Name      string    `json:"name"` // conferenceRecords/{id}
	StartTime time.Time `json:"startTime"`
	EndTime   time.Time `json:"endTime"`
	Space     string    `json:"space"`
}

// Conferences lists conference records matching a Meet API filter, e.g.
// `space.meeting_code = "abc-defg-hij"` or `start_time >= "..."`.
func (g *Google) Conferences(filter string) ([]Conference, error) {
	var all []Conference
	page := ""
	for {
		v := url.Values{"filter": {filter}, "pageSize": {"100"}}
		if page != "" {
			v.Set("pageToken", page)
		}
		var resp struct {
			ConferenceRecords []Conference `json:"conferenceRecords"`
			NextPageToken     string       `json:"nextPageToken"`
		}
		if err := g.getJSON(meetAPI+"/conferenceRecords?"+v.Encode(), &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.ConferenceRecords...)
		if resp.NextPageToken == "" {
			return all, nil
		}
		page = resp.NextPageToken
	}
}

// Artifact is a transcript or a set of Gemini notes.
type Artifact struct {
	Name            string `json:"name"`
	State           string `json:"state"` // STARTED, ENDED, FILE_GENERATED
	DocsDestination struct {
		Document  string `json:"document"`
		ExportURI string `json:"exportUri"`
	} `json:"docsDestination"`
}

// Transcripts lists a conference's transcripts.
func (g *Google) Transcripts(conference string) ([]Artifact, error) {
	var resp struct {
		Transcripts []Artifact `json:"transcripts"`
	}
	err := g.getJSON(meetAPI+"/"+conference+"/transcripts", &resp)
	return resp.Transcripts, err
}

// SmartNotes lists a conference's Gemini notes.
func (g *Google) SmartNotes(conference string) ([]Artifact, error) {
	var resp struct {
		SmartNotes []Artifact `json:"smartNotes"`
	}
	err := g.getJSON(meetAPI+"/"+conference+"/smartNotes", &resp)
	return resp.SmartNotes, err
}

type transcriptEntry struct {
	Participant string    `json:"participant"`
	Text        string    `json:"text"`
	StartTime   time.Time `json:"startTime"`
}

// Entries returns a transcript's lines, speakers resolved to names.
func (g *Google) Entries(conference, transcript string) ([]Line, error) {
	names, err := g.participantNames(conference)
	if err != nil {
		return nil, err
	}
	var lines []Line
	page := ""
	for {
		v := url.Values{"pageSize": {"100"}}
		if page != "" {
			v.Set("pageToken", page)
		}
		var resp struct {
			TranscriptEntries []transcriptEntry `json:"transcriptEntries"`
			NextPageToken     string            `json:"nextPageToken"`
		}
		if err := g.getJSON(meetAPI+"/"+transcript+"/entries?"+v.Encode(), &resp); err != nil {
			return nil, err
		}
		for _, e := range resp.TranscriptEntries {
			who := names[e.Participant]
			if who == "" {
				who = "Unknown"
			}
			lines = append(lines, Line{At: e.StartTime, Speaker: who, Text: strings.TrimSpace(e.Text)})
		}
		if resp.NextPageToken == "" {
			return lines, nil
		}
		page = resp.NextPageToken
	}
}

func (g *Google) participantNames(conference string) (map[string]string, error) {
	names := make(map[string]string)
	page := ""
	for {
		v := url.Values{"pageSize": {"250"}}
		if page != "" {
			v.Set("pageToken", page)
		}
		var resp struct {
			Participants []struct {
				Name         string `json:"name"`
				SignedinUser struct {
					DisplayName string `json:"displayName"`
				} `json:"signedinUser"`
				AnonymousUser struct {
					DisplayName string `json:"displayName"`
				} `json:"anonymousUser"`
				PhoneUser struct {
					DisplayName string `json:"displayName"`
				} `json:"phoneUser"`
			} `json:"participants"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := g.getJSON(meetAPI+"/"+conference+"/participants?"+v.Encode(), &resp); err != nil {
			return nil, err
		}
		for _, p := range resp.Participants {
			for _, n := range []string{p.SignedinUser.DisplayName, p.AnonymousUser.DisplayName, p.PhoneUser.DisplayName} {
				if n != "" {
					names[p.Name] = n
					break
				}
			}
		}
		if resp.NextPageToken == "" {
			return names, nil
		}
		page = resp.NextPageToken
	}
}

// --- Drive -----------------------------------------------------------------

// ExportHTML returns a Google Doc as HTML.
func (g *Google) ExportHTML(fileID string) (string, error) {
	data, err := g.do("GET", driveAPI+"/files/"+url.PathEscape(fileID)+"/export?mimeType=text%2Fhtml", "", nil)
	return string(data), err
}

// folder returns the id of the Snoop folder on Drive, creating it once.
func (g *Google) folder(name string) (string, error) {
	g.mu.Lock()
	id := g.tok.FolderID
	g.mu.Unlock()
	if id != "" {
		return id, nil
	}
	var f struct {
		ID string `json:"id"`
	}
	err := g.sendJSON("POST", driveAPI+"/files", map[string]any{
		"name":     name,
		"mimeType": "application/vnd.google-apps.folder",
	}, &f)
	if err != nil {
		return "", fmt.Errorf("creating the %s folder on Drive: %w", name, err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.tok.FolderID = f.ID
	return f.ID, g.saveLocked()
}

// WriteDoc creates (docID == "") or replaces a Google Doc from HTML, in the
// Snoop folder, and returns its id.
func (g *Google) WriteDoc(folderName, docID, title, htmlBody string) (string, error) {
	if docID != "" {
		_, err := g.do("PATCH", uploadAPI+"/files/"+url.PathEscape(docID)+"?uploadType=media", "text/html; charset=utf-8", strings.NewReader(htmlBody))
		if err == nil || !isStatus(err, 404) {
			return docID, err
		}
		// Deleted on Drive: make a new one.
	}
	folder, err := g.folder(folderName)
	if err != nil {
		return "", err
	}
	meta, _ := json.Marshal(map[string]any{
		"name":     title,
		"mimeType": "application/vnd.google-apps.document",
		"parents":  []string{folder},
	})
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, _ := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {"application/json; charset=UTF-8"}})
	part.Write(meta)
	part, _ = mw.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/html; charset=UTF-8"}})
	part.Write([]byte(htmlBody))
	mw.Close()

	data, err := g.do("POST", uploadAPI+"/files?uploadType=multipart", "multipart/related; boundary="+mw.Boundary(), &body)
	if err != nil {
		if isStatus(err, 404) {
			// The folder was deleted on Drive: forget it so the next run makes a new one.
			g.mu.Lock()
			g.tok.FolderID = ""
			g.saveLocked()
			g.mu.Unlock()
		}
		return "", err
	}
	var f struct {
		ID string `json:"id"`
	}
	return f.ID, json.Unmarshal(data, &f)
}

// docID pulls a Google Doc's id out of "documents/{id}", a docs URL, or a
// bare id.
func docID(s string) string {
	s = strings.TrimPrefix(s, "documents/")
	if m := docURLRe.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return s
}
