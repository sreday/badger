package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sreday/badger/pkg"
)

// Badger 2.0 prints to one printer, chosen on the page from the printers
// this computer knows (CUPS: macOS and Linux). The page shows the
// printer's state (printing, idle, off) and its jobs, read from CUPS with
// lpstat.

// runCmd runs a command and returns its output; tests replace it.
var runCmd = func(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// Printer states shown on the page.
const (
	statePrinting = "printing"
	stateIdle     = "idle"
	stateOff      = "off"
	stateNone     = "none"    // no printer chosen, or it's gone
	stateUnknown  = "unknown" // no CUPS here (Windows)
)

// PrintJob is one job in the printer's queue, or one we sent.
type PrintJob struct {
	ID     string    `json:"id"`              // CUPS job id, e.g. Brother_QL_820NWB-42
	Name   string    `json:"name,omitempty"`  // whose badge, when we sent it
	At     time.Time `json:"at"`              // when it was sent
	Status string    `json:"status"`          // queued, printing, printed, failed
	Error  string    `json:"error,omitempty"` // why it failed
}

// PrinterInfo is everything the printer panel shows.
type PrinterInfo struct {
	Printer  string     `json:"printer"`
	State    string     `json:"state"`
	Detail   string     `json:"detail,omitempty"` // CUPS's own words, e.g. "Waiting for printer to become available."
	Printers []string   `json:"printers"`
	Jobs     []PrintJob `json:"jobs"` // newest first: queued, then recently printed
}

// listPrinters returns the printers CUPS knows, by name.
func listPrinters() []string {
	out, err := runCmd("lpstat", "-e")
	if err != nil {
		return nil
	}
	var names []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if n := strings.TrimSpace(sc.Text()); n != "" && !strings.Contains(n, " ") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

// offlineHints are CUPS's words for a printer that's switched off or
// unplugged, though the queue itself is still enabled.
var offlineHints = []string{"offline", "not connected", "waiting for printer to become available", "unable to locate printer", "not responding", "connection refused", "unable to connect"}

// printerState reads a printer's state from `lpstat -l -p`.
func printerState(name string) (state, detail string) {
	if name == "" {
		return stateNone, "No printer chosen"
	}
	out, err := runCmd("lpstat", "-l", "-p", name)
	if err != nil {
		if _, lookErr := exec.LookPath("lpstat"); lookErr != nil && runtime.GOOS == "windows" {
			return stateUnknown, "Printer status needs a Mac or Linux"
		}
		return stateNone, strings.TrimSpace(string(out))
	}
	return parsePrinterState(string(out))
}

func parsePrinterState(out string) (state, detail string) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return stateNone, ""
	}
	first := strings.ToLower(lines[0])
	for _, l := range lines[1:] {
		l = strings.TrimSpace(l)
		if l != "" && detail == "" && !strings.Contains(l, ":") {
			detail = l // CUPS's message line, e.g. "Waiting for printer to become available."
		}
	}
	lower := strings.ToLower(out)
	switch {
	case strings.Contains(first, "disabled"):
		return stateOff, detail
	case containsAny(lower, offlineHints):
		return stateOff, detail
	case strings.Contains(first, "now printing"):
		return statePrinting, detail
	case strings.Contains(first, "is idle"):
		return stateIdle, detail
	}
	return stateUnknown, detail
}

func containsAny(s string, subs []string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

// lpstatJob is one line of `lpstat -o`: "Brother_QL_820NWB-42  mark  123904  Wed Oct 29 10:01:02 2026".
var lpstatJob = regexp.MustCompile(`^(\S+-(\d+))\s+\S+\s+\d+\s+(.*)$`)

// queueJobs returns a printer's jobs from lpstat: which (not-completed or
// completed), oldest first.
func queueJobs(name, which string) []PrintJob {
	if name == "" {
		return nil
	}
	out, err := runCmd("lpstat", "-W", which, "-o", name)
	if err != nil {
		return nil
	}
	return parseJobs(string(out), name)
}

func parseJobs(out, printer string) []PrintJob {
	var jobs []PrintJob
	for _, l := range strings.Split(out, "\n") {
		m := lpstatJob.FindStringSubmatch(strings.TrimSpace(l))
		if m == nil || !strings.HasPrefix(m[1], printer+"-") {
			continue
		}
		jobs = append(jobs, PrintJob{ID: m[1]})
	}
	return jobs
}

// jobNumber is the number at the end of a CUPS job id (Brother-42 -> 42).
func jobNumber(id string) int {
	n, _ := strconv.Atoi(id[strings.LastIndex(id, "-")+1:])
	return n
}

// printer is Badger 2.0's one printer and the jobs it sent.
type printer struct {
	mu   sync.Mutex
	name string
	sent []PrintJob // newest first, at most 50
}

// Name is the chosen printer.
func (p *printer) Name() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.name
}

// SetName chooses the printer and remembers it in ~/.badger/config.json.
func (p *printer) SetName(name string) error {
	p.mu.Lock()
	p.name = name
	p.mu.Unlock()
	return saveConfigPrinter(name)
}

// Info is what the printer panel shows: state, choices, and jobs - the
// queue first, then the last few printed, newest first. Our own jobs
// carry the person's name.
func (p *printer) Info() PrinterInfo {
	name := p.Name()
	state, detail := printerState(name)
	info := PrinterInfo{Printer: name, State: state, Detail: detail, Printers: listPrinters(), Jobs: []PrintJob{}}
	if info.Printers == nil {
		info.Printers = []string{}
	}
	p.mu.Lock()
	sent := map[string]PrintJob{}
	for _, j := range p.sent {
		if j.ID != "" {
			sent[j.ID] = j
		}
	}
	failed := []PrintJob{}
	for _, j := range p.sent {
		if j.Status == "failed" && time.Since(j.At) < 10*time.Minute {
			failed = append(failed, j)
		}
	}
	p.mu.Unlock()

	label := func(j PrintJob, status string) PrintJob {
		if s, ok := sent[j.ID]; ok {
			j.Name, j.At = s.Name, s.At
		}
		j.Status = status
		return j
	}
	queued := queueJobs(name, "not-completed")
	for i := len(queued) - 1; i >= 0; i-- {
		status := "queued"
		if i == 0 && state == statePrinting {
			status = "printing"
		}
		info.Jobs = append(info.Jobs, label(queued[i], status))
	}
	done := queueJobs(name, "completed")
	for i, n := len(done)-1, 0; i >= 0 && n < 5; i, n = i-1, n+1 {
		info.Jobs = append(info.Jobs, label(done[i], "printed"))
	}
	info.Jobs = append(failed, info.Jobs...)
	return info
}

// Print saves the badge and sends it to the printer: the new job, with
// its CUPS id when CUPS gives one.
func (p *printer) Print(cfg Config, img image.Image, pathPrefix, name string) PrintJob {
	job := PrintJob{Name: name, At: time.Now(), Status: "queued"}
	fail := func(format string, args ...any) PrintJob {
		job.Status, job.Error = "failed", fmt.Sprintf(format, args...)
		log.Printf("Print %s: %s", name, job.Error)
		p.remember(job)
		return job
	}
	printerName := p.Name()
	if printerName == "" {
		return fail("no printer chosen")
	}
	if cfg.PrintCmd == "" {
		return fail("no print command configured")
	}
	if dir := filepath.Dir(pathPrefix); dir != "" && dir != "." {
		os.MkdirAll(dir, 0755)
	}
	filename := pathPrefix + strings.ReplaceAll(strings.ToLower(name), " ", "_") + ".png"
	pngData, err := pkg.EncodeBadgePNG(img)
	if err != nil {
		return fail("encoding badge: %v", err)
	}
	if err := os.WriteFile(filename, pngData, 0644); err != nil {
		return fail("saving badge: %v", err)
	}

	// The job CUPS creates is the first one numbered above every job it
	// already has.
	before := 0
	for _, j := range append(queueJobs(printerName, "all"), queueJobs(printerName, "not-completed")...) {
		if n := jobNumber(j.ID); n > before {
			before = n
		}
	}
	cmdStr := strings.ReplaceAll(cfg.PrintCmd, "{printer}", printerName)
	parts := strings.Fields(cmdStr)
	for i := range parts {
		parts[i] = strings.ReplaceAll(parts[i], "{path}", filename) // the path may contain spaces
	}
	log.Printf("Print command: %s", strings.Join(parts, " "))
	if out, err := runCmd(parts[0], parts[1:]...); err != nil {
		return fail("%v %s", err, strings.TrimSpace(string(out)))
	}
	for _, j := range append(queueJobs(printerName, "not-completed"), queueJobs(printerName, "all")...) {
		if n := jobNumber(j.ID); n > before && (job.ID == "" || n < jobNumber(job.ID)) {
			job.ID = j.ID
		}
	}
	p.remember(job)
	return job
}

func (p *printer) remember(j PrintJob) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append([]PrintJob{j}, p.sent...)
	if len(p.sent) > 50 {
		p.sent = p.sent[:50]
	}
}

// saveConfigPrinter writes the chosen printer into ~/.badger/config.json,
// keeping everything else in it.
func saveConfigPrinter(name string) error {
	path := filepath.Join(appDir(), "config.json")
	cfg := map[string]any{}
	data, err := os.ReadFile(path)
	if err != nil {
		data = embeddedConfig
	}
	json.Unmarshal(data, &cfg)
	cfg["printer"] = name
	out, err := json.MarshalIndent(cfg, "", "    ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(appDir(), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, out, 0644)
}
