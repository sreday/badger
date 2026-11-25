package snort

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// apiURL is GitHub's API, rawURL its file server (tests point both at a fake).
var (
	apiURL = "https://api.github.com"
	rawURL = "https://raw.githubusercontent.com"
)

// errNotFound is a file or folder the repo does not have.
var errNotFound = errors.New("not found")

// errMoved is main moving on while a commit was being made.
var errMoved = errors.New("main moved on")

// github talks to GitHub with one token per owner (a fine-grained token
// belongs to one owner: sreday, Prompt-Engineering-Conference, conf42).
type github struct {
	tokens map[string]string // owner -> token
	client *http.Client
}

// parseTokens reads SNORT_GITHUB_TOKENS: "owner=token,owner=token".
func parseTokens(s string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && k != "" && v != "" {
			out[strings.ToLower(k)] = v
		}
	}
	return out
}

func (g *github) token(repo string) string {
	owner, _, _ := strings.Cut(repo, "/")
	return g.tokens[strings.ToLower(owner)]
}

// canWrite says whether there is a token for the repo's owner.
func (g *github) canWrite(repo string) bool { return g.token(repo) != "" }

func (g *github) do(method, repo, p string, body any, accept string) ([]byte, int, error) {
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, apiURL+"/repos/"+repo+"/"+p, rd)
	if err != nil {
		return nil, 0, err
	}
	if accept == "" {
		accept = "application/vnd.github+json"
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "snort-by-badger")
	if t := g.token(repo); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c := g.client
	if c == nil {
		c = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("GitHub did not answer")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, resp.StatusCode, errNotFound
	}
	if resp.StatusCode >= 300 {
		return data, resp.StatusCode, fmt.Errorf("GitHub answered %d", resp.StatusCode)
	}
	return data, resp.StatusCode, nil
}

func (g *github) get(repo, p string) ([]byte, error) {
	data, _, err := g.do("GET", repo, p, nil, "")
	return data, err
}

// raw is a file's exact bytes on main.
func (g *github) raw(repo, file string) ([]byte, error) {
	data, _, err := g.do("GET", repo, "contents/"+escapePath(file)+"?ref=main", nil, "application/vnd.github.raw+json")
	return data, err
}

// file is a file of main from GitHub's file server: no hourly limit, may
// be a few minutes old. For the events list and the Conf42 skies; a push
// reads through raw, which is always fresh.
func (g *github) file(repo, file string) ([]byte, error) {
	c := g.client
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequest("GET", rawURL+"/"+repo+"/main/"+escapePath(file), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "snort-by-badger")
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GitHub did not answer")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub answered %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// commit puts the files on main in one commit and returns its id. When
// main moves on meanwhile (Conf42's src does every few minutes) it starts
// again from the new tip, a few times.
func (g *github) commit(repo, message string, files map[string][]byte) (string, error) {
	if !g.canWrite(repo) {
		return "", fmt.Errorf("Snort has no GitHub key for this site yet")
	}
	// The blobs first: they don't depend on the tip.
	blobs := map[string]string{}
	for p, data := range files {
		out, _, err := g.do("POST", repo, "git/blobs", map[string]string{"content": base64.StdEncoding.EncodeToString(data), "encoding": "base64"}, "")
		if err != nil {
			return "", err
		}
		var b struct{ SHA string }
		if json.Unmarshal(out, &b) != nil || b.SHA == "" {
			return "", fmt.Errorf("GitHub kept no file")
		}
		blobs[p] = b.SHA
	}
	var last error
	for try := 0; try < 5; try++ {
		sha, err := g.commitOnce(repo, message, blobs)
		if err == nil {
			return sha, nil
		}
		last = err
		if !errors.Is(err, errMoved) {
			return "", err
		}
		time.Sleep(time.Duration(try+1) * 700 * time.Millisecond)
	}
	return "", last
}

func (g *github) commitOnce(repo, message string, blobs map[string]string) (string, error) {
	out, err := g.get(repo, "git/ref/heads/main")
	if err != nil {
		return "", err
	}
	var ref struct{ Object struct{ SHA string } }
	if json.Unmarshal(out, &ref) != nil || ref.Object.SHA == "" {
		return "", fmt.Errorf("GitHub sent no main branch")
	}
	tip := ref.Object.SHA
	if out, err = g.get(repo, "git/commits/"+tip); err != nil {
		return "", err
	}
	var c struct{ Tree struct{ SHA string } }
	if json.Unmarshal(out, &c) != nil || c.Tree.SHA == "" {
		return "", fmt.Errorf("GitHub sent no tree")
	}
	type entry struct {
		Path string `json:"path"`
		Mode string `json:"mode"`
		Type string `json:"type"`
		SHA  string `json:"sha"`
	}
	var tree []entry
	for p, sha := range blobs {
		tree = append(tree, entry{p, "100644", "blob", sha})
	}
	if out, _, err = g.do("POST", repo, "git/trees", map[string]any{"base_tree": c.Tree.SHA, "tree": tree}, ""); err != nil {
		return "", err
	}
	var t struct{ SHA string }
	if json.Unmarshal(out, &t) != nil || t.SHA == "" {
		return "", fmt.Errorf("GitHub made no tree")
	}
	if out, _, err = g.do("POST", repo, "git/commits", map[string]any{"message": message, "tree": t.SHA, "parents": []string{tip}}, ""); err != nil {
		return "", err
	}
	var nc struct{ SHA string }
	if json.Unmarshal(out, &nc) != nil || nc.SHA == "" {
		return "", fmt.Errorf("GitHub made no commit")
	}
	_, code, err := g.do("PATCH", repo, "git/refs/heads/main", map[string]any{"sha": nc.SHA, "force": false}, "")
	if code == http.StatusUnprocessableEntity || code == http.StatusConflict {
		return "", errMoved
	}
	if err != nil {
		return "", err
	}
	return nc.SHA, nil
}

// buildState says how the site build of a commit is going: building, live,
// failed or "" when GitHub has not started one yet.
func (g *github) buildState(repo, sha string) string {
	out, err := g.get(repo, "actions/runs?head_sha="+url.QueryEscape(sha)+"&per_page=10")
	if err != nil {
		return ""
	}
	var r struct {
		Runs []struct {
			Name, Path, Status, Conclusion string
		} `json:"workflow_runs"`
	}
	if json.Unmarshal(out, &r) != nil || len(r.Runs) == 0 {
		return ""
	}
	state := "live"
	for _, run := range r.Runs {
		switch {
		case run.Status != "completed":
			state = "building"
		case run.Conclusion != "success" && run.Conclusion != "skipped" && run.Conclusion != "neutral":
			return "failed"
		}
	}
	return state
}
