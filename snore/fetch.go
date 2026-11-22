package snore

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Every public source is read the same polite way: the host's robots rules
// first (a disallowed path is never fetched), its crawl delay (at least one
// second between two requests to the same host), a timeout and a size cap.

const (
	clientName = "Mozilla/5.0 (compatible; Snore/1.0)"
	fetchMax   = 16 << 20
	robotsFor  = 24 * time.Hour
)

// rawGet is one HTTP GET: status, body. Tests replace it.
type rawGet func(u string, hdr http.Header) (int, []byte, error)

func httpGet(u string, hdr http.Header) (int, []byte, error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return 0, nil, err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", clientName)
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, fetchMax))
	return resp.StatusCode, b, err
}

type robotsRules struct {
	rules []robotsRule
	delay time.Duration
	at    time.Time
}

type robotsRule struct {
	allow bool
	re    *regexp.Regexp
	n     int // pattern length: the longest match wins
}

// parseRobots reads the rules for every client ("User-agent: *").
func parseRobots(text string) robotsRules {
	var out robotsRules
	mine, inGroup := false, false
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		switch k {
		case "user-agent":
			if !inGroup {
				mine = false
			}
			inGroup = true
			if v == "*" {
				mine = true
			}
		case "allow", "disallow":
			inGroup = false
			if !mine || v == "" {
				continue
			}
			p := regexp.QuoteMeta(v)
			p = strings.ReplaceAll(p, `\*`, ".*")
			if strings.HasSuffix(p, `\$`) {
				p = strings.TrimSuffix(p, `\$`) + "$"
			}
			if re, err := regexp.Compile("^" + p); err == nil {
				out.rules = append(out.rules, robotsRule{allow: k == "allow", re: re, n: len(v)})
			}
		case "crawl-delay":
			inGroup = false
			if mine {
				if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 && f <= 60 {
					out.delay = time.Duration(f * float64(time.Second))
				}
			}
		default:
			inGroup = false
		}
	}
	return out
}

// allowed: the longest matching rule decides; no rule means allowed.
func (r robotsRules) allowed(pathQuery string) bool {
	best, allow := -1, true
	for _, ru := range r.rules {
		if ru.re.MatchString(pathQuery) && ru.n > best {
			best, allow = ru.n, ru.allow
		}
	}
	return allow
}

// polite keeps the robots rules and the last request time per host.
type polite struct {
	mu     sync.Mutex
	robots map[string]robotsRules
	last   map[string]time.Time
	min    time.Duration
}

var errDisallowed = fmt.Errorf("the site asks not to be read there")

// get fetches u politely. withRobots false is for documented APIs called
// with a key, which have their own terms and quotas.
func (s *Server) get(u string, hdr http.Header, withRobots bool) ([]byte, error) {
	pu, err := url.Parse(u)
	if err != nil || (pu.Scheme != "https" && pu.Scheme != "http") {
		return nil, fmt.Errorf("not a web address")
	}
	host := pu.Scheme + "://" + pu.Host
	p := s.pol
	var rr robotsRules
	if withRobots {
		p.mu.Lock()
		cached, ok := p.robots[host]
		p.mu.Unlock()
		rr = cached
		if !ok || s.now().Sub(rr.at) > robotsFor {
			code, body, err := s.raw(host+"/robots.txt", nil)
			rr = robotsRules{at: s.now()}
			if err == nil && code == http.StatusOK {
				rr = parseRobots(string(body))
				rr.at = s.now()
			}
			p.mu.Lock()
			p.robots[host] = rr
			p.mu.Unlock()
		}
		pq := pu.EscapedPath()
		if pu.RawQuery != "" {
			pq += "?" + pu.RawQuery
		}
		if !rr.allowed(pq) {
			return nil, errDisallowed
		}
	}
	wait := p.min
	if rr.delay > wait {
		wait = rr.delay
	}
	p.mu.Lock()
	next := p.last[host].Add(wait)
	if d := time.Until(next); d > 0 {
		p.mu.Unlock()
		time.Sleep(d)
		p.mu.Lock()
	}
	p.last[host] = time.Now()
	p.mu.Unlock()
	code, body, err := s.raw(u, hdr)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", code)
	}
	return bytes.TrimPrefix(body, []byte("\xef\xbb\xbf")), nil
}
