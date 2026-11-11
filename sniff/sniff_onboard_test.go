package sniff

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sreday/badger/luma"
)

func TestEventSlug(t *testing.T) {
	for name, want := range map[string]string{
		"SREday NYC 2026 Q4":                                      "sreday-nyc-2026-q4",
		"PLATFORMday San Francisco 2026 Q4":                       "platformday-san-francisco-2026-q4",
		"LLMday + Prompt Engineering Conference - London 2026 Q4": "llmday-london-2026-q4",
		"LLMday Tel Aviv 2026 Q4":                                 "llmday-tel-aviv-2026-q4",
		"SREday Kraków 2027 Q2":                                   "sreday-krakow-2027-q2",
		"DevOps Not Dead Katowice 2027 Q2":                        "devopsnotdead-katowice-2027-q2",
		"SREday Test":                                             "sreday-test",
	} {
		if got := eventSlug(name); got != want {
			t.Errorf("eventSlug(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestPickEvent(t *testing.T) {
	evs := []luma.Event{
		{APIId: "a", Name: "SREday London 2026 Q4"},
		{APIId: "b", Name: "LLMday + Prompt Engineering Conference - London 2026 Q4"},
		{APIId: "c", Name: "LLMday Tel Aviv 2026 Q4"},
		{APIId: "d", Name: "SREday London 2027 Q1"},
	}
	for _, c := range []struct{ brand, folder, want string }{
		{"sreday", "2026-london-q4", "a"},
		{"llmday", "2026-london-q4", "b"},
		{"pec", "2026-london-q4", "b"},
		{"llmday", "2026-tel-aviv-q4", "c"},
		{"sreday", "2027-london-q1", "d"},
		{"platformday", "2026-london-q4", ""}, // two Londons, neither PLATFORMday: no guess
		{"sreday", "2026-paris-q4", ""},
		{"sreday", "london", ""},
		{"devopsnotdead", "2027-london-q1", ""}, // only SREday London that quarter: not theirs
	} {
		ev, ok := pickEvent(evs, c.brand, c.folder)
		if got := map[bool]string{true: ev.APIId}[ok]; got != c.want {
			t.Errorf("pickEvent(%s, %s) = %q, want %q", c.brand, c.folder, got, c.want)
		}
	}
}

// The readable link replaces an older random one, which keeps working as
// a redirect (event page and speaker pages).
func TestReadableSecretAndOldLinks(t *testing.T) {
	st := newSniffTest(t)
	s := st.srv.sniff
	// An old random secret, as made before readable links.
	s.mu.Lock()
	s.secrets["evt-1"] = "k7mq4x2pw9"
	s.mu.Unlock()
	plain := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := plain.Get(st.ts.URL + "/sniff/k7mq4x2pw9")
	if err != nil {
		t.Fatal(err)
	}
	cur, _ := s.HasEventSecret("evt-1")
	if !strings.HasPrefix(cur, "sreday-test-") || len(cur) != len("sreday-test-")+6 {
		t.Fatalf("readable secret: %q", cur)
	}
	if resp.StatusCode != http.StatusPermanentRedirect || resp.Header.Get("Location") != "/sniff/"+cur {
		t.Fatalf("old event link: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	name := s.SpeakerNames("evt-1")[st.ss.Code]
	resp, _ = plain.Get(st.ts.URL + "/sniff/k7mq4x2pw9/" + name)
	if resp.StatusCode != http.StatusPermanentRedirect || resp.Header.Get("Location") != "/sniff/"+cur+"/"+name {
		t.Errorf("old speaker link: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, _ := plain.Get(st.ts.URL + "/sniff/" + cur); resp.StatusCode != 200 {
		t.Errorf("new event link: %d", resp.StatusCode)
	}
	// Kept across a restart.
	raw, _ := os.ReadFile(filepath.Join(s.dir, "sniff_secret_aliases.json"))
	if !strings.Contains(string(raw), "k7mq4x2pw9") {
		t.Errorf("aliases not saved: %s", raw)
	}
	if again, _ := s.EventSecret("evt-1"); again != cur {
		t.Errorf("secret changed: %q then %q", cur, again)
	}
}

// The onboarding script gets an event's link with the key, never without.
func TestSniffLinkAPI(t *testing.T) {
	st := newSniffTest(t)
	key, err := st.srv.sniff.LinkKey()
	if err != nil || len(key) < 32 {
		t.Fatalf("key %q: %v", key, err)
	}
	if again, _ := st.srv.sniff.LinkKey(); again != key {
		t.Fatal("key changed")
	}
	get := func(q, k string) (int, map[string]any) {
		req, _ := http.NewRequest("GET", st.ts.URL+"/api/sniff/link?"+q, nil)
		if k != "" {
			req.Header.Set("X-Sniff-Key", k)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var v map[string]any
		json.NewDecoder(resp.Body).Decode(&v)
		return resp.StatusCode, v
	}
	if code, _ := get("brand=sreday&event=2026-test-q4", ""); code != 401 {
		t.Errorf("no key: %d", code)
	}
	if code, _ := get("brand=sreday&event=2026-test-q4", "nope"); code != 401 {
		t.Errorf("wrong key: %d", code)
	}
	// "SREday Test" has no year or quarter, so no folder finds it.
	if code, _ := get("brand=sreday&event=2026-test-q4", key); code != 404 {
		t.Errorf("unknown event: %d", code)
	}
}
