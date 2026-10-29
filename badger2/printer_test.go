package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sreday/badger/pkg"
)

func TestParsePrinterState(t *testing.T) {
	for _, c := range []struct{ out, state, detail string }{
		{"printer Brother_QL_820NWB is idle.  enabled since Wed Oct 29 10:00:01 2026\n\tDescription: Brother QL-820NWB\n", stateIdle, ""},
		{"printer Brother_QL_820NWB now printing Brother_QL_820NWB-42.  enabled since Wed Oct 29 10:00:01 2026\n", statePrinting, ""},
		{"printer Brother_QL_820NWB disabled since Wed Oct 29 10:00:01 2026 -\n\tPaused\n", stateOff, "Paused"},
		{"printer Brother_QL_820NWB is idle.  enabled since Wed Oct 29 10:00:01 2026\n\tWaiting for printer to become available.\n", stateOff, "Waiting for printer to become available."},
		{"printer Brother_QL_820NWB is idle.  enabled since Wed Oct 29 2026\n\tAlerts: offline-report\n", stateOff, ""},
		{"printer Brother_QL_820NWB now printing Brother_QL_820NWB-7.  enabled since Wed Oct 29 2026\n\tThe printer is not connected.\n", stateOff, "The printer is not connected."},
	} {
		if s, d := parsePrinterState(c.out); s != c.state || d != c.detail {
			t.Errorf("%q: got %s %q, want %s %q", c.out, s, d, c.state, c.detail)
		}
	}
}

func TestParseJobs(t *testing.T) {
	out := "Brother_QL_820NWB-41    mark            123904   Wed Oct 29 10:01:02 2026\n" +
		"Brother_QL_820NWB-42    mark            123904   Wed Oct 29 10:01:09 2026\n" +
		"Other_Printer-3         mark            1024     Wed Oct 29 10:01:09 2026\n"
	jobs := parseJobs(out, "Brother_QL_820NWB")
	if len(jobs) != 2 || jobs[0].ID != "Brother_QL_820NWB-41" || jobs[1].ID != "Brother_QL_820NWB-42" {
		t.Errorf("jobs = %+v", jobs)
	}
	if jobNumber("Brother_QL_820NWB-42") != 42 {
		t.Error("job number")
	}
}

// fakeCUPS stands in for lpstat and lpr: one printer, jobs that wait in
// the queue until done() moves them to completed.
type fakeCUPS struct {
	mu        sync.Mutex
	printers  []string
	state     string // lpstat -p first line's state part
	queued    []string
	completed []string
	next      int
	printed   [][]string // lpr arguments
}

func (f *fakeCUPS) run(name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	line := func(id string) string { return id + "    mark    123904   Wed Oct 29 10:01:02 2026\n" }
	switch {
	case name == "lpstat" && len(args) == 1 && args[0] == "-e":
		return []byte(strings.Join(f.printers, "\n") + "\n"), nil
	case name == "lpstat" && args[0] == "-l":
		return []byte("printer " + args[2] + " " + f.state + "  enabled since Wed Oct 29 2026\n"), nil
	case name == "lpstat" && args[0] == "-W":
		var out strings.Builder
		if args[1] != "not-completed" {
			for _, j := range f.completed {
				out.WriteString(line(j))
			}
		}
		if args[1] != "completed" {
			for _, j := range f.queued {
				out.WriteString(line(j))
			}
		}
		return []byte(out.String()), nil
	case name == "/usr/bin/lpr":
		f.printed = append(f.printed, args)
		f.next++
		f.queued = append(f.queued, args[1]+"-"+itoa(f.next))
		return nil, nil
	}
	return nil, os.ErrNotExist
}

func (f *fakeCUPS) done() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completed = append(f.completed, f.queued...)
	f.queued = nil
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func newTestServer(t *testing.T, cups *fakeCUPS) (*Server, *httptest.Server) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	old := runCmd
	runCmd = cups.run
	t.Cleanup(func() { runCmd = old })
	cfg := Config{PrintCmd: "/usr/bin/lpr -P {printer} -o PageSize=62x100mm {path}", Printer: "Brother_QL_820NWB"}
	srv, err := newServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv.guests["evt-1"] = map[string]pkg.BadgeData{
		"ada@example.com": {Name: "Ada Lovelace", Email: "ada@example.com", Title: "CTO", Company: "Analytical", LinkedIn: "https://www.linkedin.com/in/ada"},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /print/{eventID}/{email}", srv.handlePrintBadge)
	mux.HandleFunc("GET /printer", srv.handlePrinterInfo)
	mux.HandleFunc("POST /printer", srv.handleSetPrinter)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return srv, ts
}

func getInfo(t *testing.T, ts *httptest.Server) PrinterInfo {
	t.Helper()
	resp, err := http.Get(ts.URL + "/printer")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var info PrinterInfo
	json.NewDecoder(resp.Body).Decode(&info)
	return info
}

func TestPrintShowsTheNewJob(t *testing.T) {
	cups := &fakeCUPS{printers: []string{"Brother_QL_820NWB", "Brother_QL_820NWB_2"}, state: "is idle.", next: 40, completed: []string{"Brother_QL_820NWB-40"}}
	_, ts := newTestServer(t, cups)

	if info := getInfo(t, ts); info.State != stateIdle || info.Printer != "Brother_QL_820NWB" || len(info.Printers) != 2 {
		t.Fatalf("before = %+v", info)
	}

	resp, err := http.Post(ts.URL+"/print/evt-1/ada@example.com", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	var job PrintJob
	json.NewDecoder(resp.Body).Decode(&job)
	resp.Body.Close()
	if job.ID != "Brother_QL_820NWB-41" || job.Name != "Ada Lovelace" || job.Status != "queued" {
		t.Fatalf("job = %+v", job)
	}
	// One printer only, and the badge file was saved.
	if len(cups.printed) != 1 || cups.printed[0][1] != "Brother_QL_820NWB" {
		t.Fatalf("lpr = %v", cups.printed)
	}
	if _, err := os.Stat(cups.printed[0][len(cups.printed[0])-1]); err != nil {
		t.Errorf("badge file: %v", err)
	}

	// The panel shows it queued (printing once CUPS says so), with the name.
	cups.state = "now printing Brother_QL_820NWB-41."
	info := getInfo(t, ts)
	if info.State != statePrinting || len(info.Jobs) < 1 || info.Jobs[0].ID != job.ID || info.Jobs[0].Name != "Ada Lovelace" || info.Jobs[0].Status != "printing" {
		t.Fatalf("while printing = %+v", info)
	}
	// Then printed; another app's job has no name.
	cups.done()
	cups.state = "is idle."
	info = getInfo(t, ts)
	if info.Jobs[0].ID != job.ID || info.Jobs[0].Status != "printed" || info.Jobs[1].Name != "" {
		t.Errorf("after = %+v", info)
	}
}

func TestPrintFailureShows(t *testing.T) {
	cups := &fakeCUPS{printers: []string{"Brother_QL_820NWB"}, state: "is idle."}
	srv, ts := newTestServer(t, cups)
	srv.cfg.PrintCmd = "/usr/bin/missing-lpr {path}" // the fake doesn't know it: fails
	resp, _ := http.Post(ts.URL+"/print/evt-1/ada@example.com", "", nil)
	var job PrintJob
	json.NewDecoder(resp.Body).Decode(&job)
	resp.Body.Close()
	if job.Status != "failed" || job.Error == "" {
		t.Fatalf("job = %+v", job)
	}
	if info := getInfo(t, ts); len(info.Jobs) != 1 || info.Jobs[0].Status != "failed" || info.Jobs[0].Name != "Ada Lovelace" {
		t.Errorf("info = %+v", info)
	}
}

func TestChoosePrinter(t *testing.T) {
	cups := &fakeCUPS{printers: []string{"Brother_QL_820NWB", "Zebra"}, state: "disabled since Wed Oct 29 2026 -"}
	_, ts := newTestServer(t, cups)
	if resp, _ := http.PostForm(ts.URL+"/printer", url.Values{"printer": {"Nope"}}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown printer: %d", resp.StatusCode)
	}
	resp, _ := http.PostForm(ts.URL+"/printer", url.Values{"printer": {"Zebra"}})
	var info PrinterInfo
	json.NewDecoder(resp.Body).Decode(&info)
	resp.Body.Close()
	if info.Printer != "Zebra" || info.State != stateOff {
		t.Fatalf("info = %+v", info)
	}
	// Remembered in ~/.badger/config.json, next to the print command.
	data, err := os.ReadFile(filepath.Join(appDir(), "config.json"))
	if err != nil || !strings.Contains(string(data), `"printer": "Zebra"`) || !strings.Contains(string(data), "print_cmd") {
		t.Errorf("config = %s (%v)", data, err)
	}
}

func TestOldConfigTakesFirstPrinter(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	os.MkdirAll(appDir(), 0755)
	os.WriteFile(filepath.Join(appDir(), "config.json"), []byte(`{"print_cmd":"lpr {path}","printers":["A","B"]}`), 0644)
	if cfg := loadConfig(); cfg.Printer != "A" || cfg.PrintCmd != "lpr {path}" {
		t.Errorf("cfg = %+v", cfg)
	}
}
