package sites

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestParsePartners(t *testing.T) {
	got := parsePartners("# comment\nnon_sponsor_orgs:\n  - hockeystick.png\n  - Packt.png           # media partner\nsponsors_note: x\ncommunity_partners:\n  - \"cloud native lisbon.png\"\nhidden_duplicates:\n  - ing-old.png\nother:\n  - harness.png\n")
	for _, logo := range []string{"hockeystick.png", "packt.png", "cloud native lisbon.png", "ing-old.png"} {
		if !got[logo] {
			t.Errorf("%s should be a partner", logo)
		}
	}
	if got["harness.png"] || len(got) != 4 {
		t.Errorf("partners = %v", got)
	}
}

func TestNameFromLogo(t *testing.T) {
	for logo, want := range map[string]string{
		"harness.png":        "Harness",
		"gdg_london.png":     "GDG London",
		"kube-events.png":    "Kube Events",
		"ksug.ai.png":        "Ksug AI",
		"pe-norway-full.png": "PE Norway",
		"DevIT_black.png":    "DevIT",
	} {
		if got := nameFromLogo(logo); got != want {
			t.Errorf("nameFromLogo(%q) = %q, want %q", logo, got, want)
		}
	}
}

func TestFindEventDirSparesTheAPI(t *testing.T) {
	// The folder named like the event comes from raw files alone, and 40
	// phones asking at once cost one lookup (the API allows 60 an hour).
	var api, meta atomic.Int32
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/"):
			api.Add(1)
			http.Error(w, "rate limited", http.StatusForbidden)
		case r.URL.Path == "/o/b/HEAD/2026-san-francisco-q4/metadata.yml":
			meta.Add(1)
			io.WriteString(w, "luma_evt: evt-sf\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer gh.Close()
	api0, raw0 := APIURL, RawURL
	APIURL, RawURL = gh.URL, gh.URL
	t.Cleanup(func() { APIURL, RawURL = api0, raw0 })

	var wg sync.WaitGroup
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := FindEventDir([]string{"o/a", "o/b"}, "evt-sf", "PLATFORMday San Francisco 2026 Q4")
			if err != nil || d.Repo != "o/b" || d.Dir != "2026-san-francisco-q4" || d.Metadata != "luma_evt: evt-sf\n" {
				t.Errorf("found %+v, %v", d, err)
			}
		}()
	}
	wg.Wait()
	if api.Load() != 0 {
		t.Errorf("%d GitHub API calls", api.Load())
	}
	if n := meta.Load(); n > 40 {
		t.Errorf("metadata read %d times", n)
	}
}

func TestFolderGuess(t *testing.T) {
	for name, want := range map[string]string{
		"SREday San Francisco 2026 Q4": "2026-san-francisco-q4",
		"LLMday + PEC London 2026 Q4":  "2026-london-q4",
		"PLATFORMday NYC 2026 Q4":      "2026-nyc-q4",
		"SREday Amsterdam 2026 Q4":     "2026-amsterdam-q4",
	} {
		if got := folderGuess(name); got != want {
			t.Errorf("folderGuess(%q) = %q, want %q", name, got, want)
		}
	}
}
