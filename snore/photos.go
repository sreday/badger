package snore

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Headshots: Snore fetches them itself, because the scouting run can't
// always open the pages it cites. For a card without a photo it reads up to
// three pages the card already links (the speaker's own pages first) and
// takes the picture labelled with their name, or the page's preview image
// on a page about them. Only links on the card are fetched, one at a time.

const (
	photoPages = 3
	photoRetry = 7 * 24 * time.Hour
	photoPause = time.Second
)

var (
	imgTag  = regexp.MustCompile(`(?is)<img\b[^>]*>`)
	metaTag = regexp.MustCompile(`(?is)<meta\b[^>]*>`)
	attr    = regexp.MustCompile(`(?is)\b(src|alt|title|property|name|content)\s*=\s*("([^"]*)"|'([^']*)')`)
)

func attrs(tag string) map[string]string {
	out := map[string]string{}
	for _, m := range attr.FindAllStringSubmatch(tag, -1) {
		v := m[3]
		if v == "" {
			v = m[4]
		}
		out[strings.ToLower(m[1])] = html.UnescapeString(strings.TrimSpace(v))
	}
	return out
}

// pickPhoto finds a speaker's headshot in a page: an <img> whose alt (or
// title) is their name, else the page's og:image when the page is about
// them (their name is in its address).
func pickPhoto(page, pageURL, name string) string {
	key := normName(name)
	if key == "" {
		return ""
	}
	for _, t := range imgTag.FindAllString(page, -1) {
		a := attrs(t)
		label := normName(a["alt"])
		if label == "" {
			label = normName(a["title"])
		}
		if label == key && a["src"] != "" {
			if u := absURL(pageURL, a["src"]); u != "" {
				return u
			}
		}
	}
	if !aboutThem(pageURL, key) {
		return ""
	}
	for _, t := range metaTag.FindAllString(page, -1) {
		a := attrs(t)
		if p := strings.ToLower(a["property"] + a["name"]); p == "og:image" || p == "twitter:image" {
			if u := absURL(pageURL, a["content"]); u != "" {
				return u
			}
		}
	}
	return ""
}

// aboutThem: the page's address carries the speaker's name (jane-doe,
// jane_doe, janedoe).
func aboutThem(pageURL, key string) bool {
	u, err := url.Parse(pageURL)
	if err != nil {
		return false
	}
	path := strings.ToLower(u.Path)
	f := strings.Fields(key)
	for _, sep := range []string{"-", "_", ""} {
		if strings.Contains(path, strings.Join(f, sep)) {
			return true
		}
	}
	return false
}

func absURL(base, ref string) string {
	b, err := url.Parse(base)
	if err != nil {
		return ""
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	u := b.ResolveReference(r)
	if u.Scheme != "https" && u.Scheme != "http" {
		return ""
	}
	return u.String()
}

// photoPagesFor: the card's links, the ones about the speaker first.
func photoPagesFor(c *Card) []string {
	var mine, rest []string
	seen := map[string]bool{}
	add := func(u string) {
		if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") || seen[u] {
			return
		}
		if strings.Contains(u, "linkedin.com") || strings.Contains(u, "x.com/") || strings.Contains(u, "twitter.com/") {
			return // never those
		}
		seen[u] = true
		if aboutThem(u, c.Key) {
			mine = append(mine, u)
		} else {
			rest = append(rest, u)
		}
	}
	for _, u := range c.Links {
		add(u)
	}
	for _, t := range c.Talks {
		add(t.URL)
	}
	out := append(mine, rest...)
	if len(out) > photoPages {
		out = out[:photoPages]
	}
	return out
}

func fetchPage(u string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Badger/1.0)")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.Contains(ct, "html") {
		return "", fmt.Errorf("not a page")
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	return string(b), err
}

// findPhotos looks for the headshots still missing. Called without the
// lock; slow on purpose (a pause between pages).
func (s *Server) findPhotos() {
	type want struct {
		key, name, photo string
		pages            []string
	}
	s.mu.Lock()
	now := s.now()
	var todo []want
	seen := map[string]bool{}
	for _, cs := range s.st.Cards {
		for _, c := range cs {
			if c.PhotoBox != "" || seen[c.Key] || now.Sub(c.PhotoTried) < photoRetry {
				continue
			}
			seen[c.Key] = true
			todo = append(todo, want{c.Key, c.Name, c.Photo, photoPagesFor(c)})
		}
	}
	s.mu.Unlock()
	for _, w := range todo {
		box := ""
		if w.photo != "" {
			box = s.keepPhoto(w.photo)
		}
		for _, p := range w.pages {
			if box != "" {
				break
			}
			page, err := s.page(p)
			time.Sleep(s.pause)
			if err != nil {
				continue
			}
			if img := pickPhoto(page, p, w.name); img != "" {
				box = s.keepPhoto(img)
			}
		}
		s.mu.Lock()
		for _, cs := range s.st.Cards {
			for _, c := range cs {
				if c.Key == w.key {
					c.PhotoTried = s.now()
					if box != "" {
						c.PhotoBox = box
					}
				}
			}
		}
		if box != "" {
			s.note("photo", "Photo found for %s", w.name)
		}
		s.save()
		s.mu.Unlock()
	}
}

// photosLater looks for missing headshots in the background, one run at a
// time.
func (s *Server) photosLater() {
	s.mu.Lock()
	if s.photoBusy {
		s.mu.Unlock()
		return
	}
	s.photoBusy = true
	s.mu.Unlock()
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		s.findPhotos()
		s.checkLinks()
		s.mu.Lock()
		s.photoBusy = false
		s.mu.Unlock()
	}()
}

// linkCheck: how often a talk link is opened again.
const linkCheck = 7 * 24 * time.Hour

// linkStatus opens a talk page and gives its HTTP status (0: no answer).
func linkStatus(u string) int {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return 0
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Snore)")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}

// checkLinks opens each talk link on the board once a week. A page that
// answers 404 or 410 is marked dead: the card still lists the conference,
// without a link. Any other answer (a block, a timeout) leaves the link.
func (s *Server) checkLinks() {
	s.mu.Lock()
	now := s.now()
	var todo []string
	seen := map[string]bool{}
	for _, cs := range s.st.Cards {
		for _, c := range cs {
			for _, t := range c.Talks {
				if t.URL != "" && !seen[t.URL] && now.Sub(t.Checked) >= linkCheck {
					seen[t.URL] = true
					todo = append(todo, t.URL)
				}
			}
		}
	}
	s.mu.Unlock()
	for _, u := range todo {
		code := s.status(u)
		time.Sleep(s.pause)
		s.mu.Lock()
		if code == http.StatusNotFound || code == http.StatusGone {
			s.note("link", "Talk link gone (%d), shown without a link: %s", code, u)
		}
		for _, cs := range s.st.Cards {
			for _, c := range cs {
				for i := range c.Talks {
					if c.Talks[i].URL == u {
						c.Talks[i].Checked = s.now()
						c.Talks[i].Dead = code == http.StatusNotFound || code == http.StatusGone
					}
				}
			}
		}
		s.mu.Unlock()
	}
	if len(todo) > 0 {
		s.mu.Lock()
		s.save()
		s.mu.Unlock()
	}
}
