package snap

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Snap's own Google connection: Gmail read-only, nothing else. It's its
// own, not Snoop's (each Badger tool lives in its own folder): its own OAuth
// client ("Snap" in the Cloud console, SNAP_GOOGLE_CLIENT_ID /
// SNAP_GOOGLE_CLIENT_SECRET) with the redirect URI
// https://scan.sreday.com/snap/admin/google/callback.

const (
	googleAuthURL  = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL = "https://oauth2.googleapis.com/token"
	googleUserinfo = "https://openidconnect.googleapis.com/v1/userinfo"
)

var googleScopes = []string{"openid", "email", "https://www.googleapis.com/auth/gmail.readonly"}

// ErrNotConnected: nobody has clicked Connect Google in Snap yet.
var ErrNotConnected = errors.New("Google isn't connected: click Connect Google in Snap's Gmail menu")

type googleToken struct {
	RefreshToken string    `json:"refresh_token"`
	Email        string    `json:"email"`
	Connected    time.Time `json:"connected"`
}

// Google holds Snap's refresh token (DATA_DIR/snap/google.json, 0600).
type Google struct {
	ClientID, ClientSecret string
	TokenURL, UserinfoURL  string // tests point these elsewhere
	path                   string
	secret                 []byte // signs the OAuth state
	http                   *http.Client

	mu      sync.Mutex
	tok     googleToken
	access  string
	expires time.Time
}

func NewGoogle(clientID, clientSecret, dir string) (*Google, error) {
	g := &Google{ClientID: clientID, ClientSecret: clientSecret, TokenURL: googleTokenURL, UserinfoURL: googleUserinfo,
		path: filepath.Join(dir, "google.json"), http: &http.Client{Timeout: 30 * time.Second}}
	secretPath := filepath.Join(dir, "secret")
	if data, err := os.ReadFile(secretPath); err == nil && len(data) >= 32 {
		g.secret = data
	} else {
		g.secret = make([]byte, 32)
		if _, err := rand.Read(g.secret); err != nil {
			return nil, err
		}
		if err := os.WriteFile(secretPath, g.secret, 0o600); err != nil {
			return nil, err
		}
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

// CanConnect: the OAuth client is configured.
func (g *Google) CanConnect() bool { return g.ClientID != "" && g.ClientSecret != "" }

// Connected returns the connected account and when, or "".
func (g *Google) Connected() (string, time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.tok.RefreshToken == "" {
		return "", time.Time{}
	}
	return g.tok.Email, g.tok.Connected
}

// State is the signed, 15-minute OAuth state.
func (g *Google) State(t time.Time) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	m := hmac.New(sha256.New, g.secret)
	m.Write([]byte("snap-oauth:" + ts))
	return ts + "." + hex.EncodeToString(m.Sum(nil))
}

func (g *Google) ValidState(state string, now time.Time) bool {
	ts, _, ok := strings.Cut(state, ".")
	sec, err := strconv.ParseInt(ts, 10, 64)
	if !ok || err != nil {
		return false
	}
	t := time.Unix(sec, 0)
	return now.Sub(t) < 15*time.Minute && hmac.Equal([]byte(state), []byte(g.State(t)))
}

func (g *Google) AuthURL(redirect, state, login string) string {
	v := url.Values{
		"client_id":     {g.ClientID},
		"redirect_uri":  {redirect},
		"response_type": {"code"},
		"scope":         {strings.Join(googleScopes, " ")},
		"access_type":   {"offline"},
		"prompt":        {"consent"}, // always hand back a refresh token
		"state":         {state},
		"login_hint":    {login},
	}
	return googleAuthURL + "?" + v.Encode()
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func (g *Google) postToken(v url.Values) (tokenResponse, error) {
	v.Set("client_id", g.ClientID)
	v.Set("client_secret", g.ClientSecret)
	var tr tokenResponse
	resp, err := g.http.PostForm(g.TokenURL, v)
	if err != nil {
		return tr, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	json.Unmarshal(body, &tr)
	if resp.StatusCode != http.StatusOK || tr.AccessToken == "" {
		return tr, fmt.Errorf("Google token %d: %s %s", resp.StatusCode, tr.Error, tr.ErrorDesc)
	}
	return tr, nil
}

// Exchange turns the callback's code into a refresh token, for the one
// account Snap reads (want) only.
func (g *Google) Exchange(code, redirect, want string) (string, error) {
	tr, err := g.postToken(url.Values{"code": {code}, "redirect_uri": {redirect}, "grant_type": {"authorization_code"}})
	if err != nil {
		return "", err
	}
	if tr.RefreshToken == "" {
		return "", errors.New("Google sent no refresh token; remove Snap at myaccount.google.com/permissions and connect again")
	}
	req, _ := http.NewRequest(http.MethodGet, g.UserinfoURL, nil)
	req.Header.Set("Authorization", "Bearer "+tr.AccessToken)
	resp, err := g.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var who struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&who); err != nil || who.Email == "" {
		return "", fmt.Errorf("reading the account's email: %v", err)
	}
	if want != "" && !strings.EqualFold(who.Email, want) {
		return "", fmt.Errorf("signed in as %s: Snap reads %s, so connect with that account", who.Email, want)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.tok = googleToken{RefreshToken: tr.RefreshToken, Email: who.Email, Connected: time.Now().UTC()}
	g.access, g.expires = tr.AccessToken, time.Now().Add(time.Duration(tr.ExpiresIn)*time.Second-time.Minute)
	data, _ := json.Marshal(g.tok)
	return who.Email, os.WriteFile(g.path, data, 0o600)
}

// AccessToken is a fresh access token (refreshed when it's about to expire).
func (g *Google) AccessToken() (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.tok.RefreshToken == "" {
		return "", ErrNotConnected
	}
	if g.access != "" && time.Now().Before(g.expires) {
		return g.access, nil
	}
	tr, err := g.postToken(url.Values{"refresh_token": {g.tok.RefreshToken}, "grant_type": {"refresh_token"}})
	if err != nil {
		return "", fmt.Errorf("refreshing Google access (click Reconnect in Snap's Gmail menu if this keeps failing): %w", err)
	}
	g.access, g.expires = tr.AccessToken, time.Now().Add(time.Duration(tr.ExpiresIn)*time.Second-time.Minute)
	return g.access, nil
}
