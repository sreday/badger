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

// Badger 2.0 prints to the printers switched on on the page (one or more,
// from the printers this computer knows: CUPS on macOS and Linux). Each
// badge goes to the one with the shortest queue that isn't off. The page
// shows each printer's state (printing, idle, off) and the jobs, read from
// CUPS with lpstat.

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

// PrintJob is one job in a printer's queue, or one we sent.
type PrintJob struct {
	ID      string    `json:"id"`                // CUPS job id, e.g. Brother_QL_820NWB-42
	Printer string    `json:"printer,omitempty"` // which printer it went to
	Name    string    `json:"name,omitempty"`    // whose badge, when we sent it
	For     string    `json:"for,omitempty"`     // the guest's email, so the page can find their row
	At      time.Time `json:"at"`                // when it was sent
	Status  string    `json:"status"`            // queued, printing, printed, failed
	Error   string    `json:"error,omitempty"`   // why it failed
}

// PrinterStatus is one switched-on printer's state.
type PrinterStatus struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"` // CUPS's own words, e.g. "Waiting for printer to become available."
	Queue  int    `json:"queue"`            // jobs waiting or printing
}

// PrinterInfo is everything the printer panel shows.
type PrinterInfo struct {
	Printers []string        `json:"printers"` // every printer this computer knows
	On       []PrinterStatus `json:"on"`       // the ones Badger prints to
	Jobs     []PrintJob      `json:"jobs"`     // failed, queued, then recently printed
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

// printer is the printers Badger 2.0 prints to, and the jobs it sent.
type printer struct {
	mu    sync.Mutex
	names []string   // switched on, in the order chosen
	sent  []PrintJob // newest first, at most 50
}

// Names are the switched-on printers.
func (p *printer) Names() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.names...)
}

// SetNames switches printers on (the rest off) and remembers them in
// ~/.badger/config.json.
func (p *printer) SetNames(names []string) error {
	p.mu.Lock()
	p.names = append([]string(nil), names...)
	p.mu.Unlock()
	return saveConfigPrinters(names)
}

// status reads a switched-on printer's state and queue length.
func status(name string) PrinterStatus {
	state, detail := printerState(name)
	return PrinterStatus{Name: name, State: state, Detail: detail, Queue: len(queueJobs(name, "not-completed"))}
}

// pick is where the next badge goes: the switched-on printer with the
// shortest queue that isn't off (the first one chosen wins a tie). When
// every printer is off, the first one: the job waits there and prints when
// it's back.
func (p *printer) pick() string {
	best, bestScore := "", 0
	for i, n := range p.Names() {
		st := status(n)
		score := st.Queue*100 + i
		if st.State == stateOff || st.State == stateNone {
			score += 1_000_000
		}
		if best == "" || score < bestScore {
			best, bestScore = n, score
		}
	}
	return best
}

// Info is what the printer panel shows: each switched-on printer's state,
// and the jobs - failed ones, then the queues, then the last few printed,
// newest first. Our own jobs carry the person's name.
func (p *printer) Info() PrinterInfo {
	info := PrinterInfo{Printers: listPrinters(), On: []PrinterStatus{}, Jobs: []PrintJob{}}
	if info.Printers == nil {
		info.Printers = []string{}
	}
	p.mu.Lock()
	sent := map[string]PrintJob{}
	failed := []PrintJob{}
	for _, j := range p.sent {
		if j.ID != "" {
			sent[j.ID] = j
		}
		if j.Status == "failed" && time.Since(j.At) < 10*time.Minute {
			failed = append(failed, j)
		}
	}
	p.mu.Unlock()

	label := func(j PrintJob, printer, status string) PrintJob {
		if s, ok := sent[j.ID]; ok {
			j.Name, j.At = s.Name, s.At
		}
		j.Printer, j.Status = printer, status
		return j
	}
	var queued, printed []PrintJob
	for _, name := range p.Names() {
		st := status(name)
		info.On = append(info.On, st)
		q := queueJobs(name, "not-completed")
		for i := len(q) - 1; i >= 0; i-- {
			s := "queued"
			if i == 0 && st.State == statePrinting {
				s = "printing"
			}
			queued = append(queued, label(q[i], name, s))
		}
		done := queueJobs(name, "completed")
		for i, n := len(done)-1, 0; i >= 0 && n < 5; i, n = i-1, n+1 {
			printed = append(printed, label(done[i], name, "printed"))
		}
	}
	// Newest first across printers (CUPS numbers jobs across all of them).
	newest := func(js []PrintJob) {
		sort.SliceStable(js, func(a, b int) bool { return jobNumber(js[a].ID) > jobNumber(js[b].ID) })
	}
	newest(queued)
	newest(printed)
	if len(printed) > 5 {
		printed = printed[:5]
	}
	info.Jobs = append(append(append(info.Jobs, failed...), queued...), printed...)
	return info
}

// Print saves the badge and sends it to a printer (see pick): the new job,
// with its CUPS id when CUPS gives one.
func (p *printer) Print(cfg Config, img image.Image, pathPrefix, name string) PrintJob {
	job := PrintJob{Name: name, At: time.Now(), Status: "queued"}
	fail := func(format string, args ...any) PrintJob {
		job.Status, job.Error = "failed", fmt.Sprintf(format, args...)
		log.Printf("Print %s: %s", name, job.Error)
		p.remember(job)
		return job
	}
	printerName := p.pick()
	if printerName == "" {
		return fail("no printer switched on")
	}
	job.Printer = printerName
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

// saveConfigPrinters writes the switched-on printers into
// ~/.badger/config.json as "printers", keeping everything else in it.
func saveConfigPrinters(names []string) error {
	path := filepath.Join(appDir(), "config.json")
	cfg := map[string]any{}
	data, err := os.ReadFile(path)
	if err != nil {
		data = embeddedConfig
	}
	json.Unmarshal(data, &cfg)
	delete(cfg, "printer")
	cfg["printers"] = names
	out, err := json.MarshalIndent(cfg, "", "    ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(appDir(), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, out, 0644)
}

// JobStatuses is where each job is now: queued, printing or printed, read
// from CUPS for the switched-on printers. A job CUPS no longer lists at all
// has finished and been cleared away: printed.
func (p *printer) JobStatuses(ids []string) map[string]string {
	out := map[string]string{}
	waiting, done := map[string]string{}, map[string]bool{}
	for _, name := range p.Names() {
		state, _ := printerState(name)
		for i, j := range queueJobs(name, "not-completed") {
			waiting[j.ID] = "queued"
			if i == 0 && state == statePrinting {
				waiting[j.ID] = "printing"
			}
		}
		for _, j := range queueJobs(name, "completed") {
			done[j.ID] = true
		}
	}
	p.mu.Lock()
	failed := map[string]bool{}
	for _, j := range p.sent {
		if j.Status == "failed" && j.ID != "" {
			failed[j.ID] = true
		}
	}
	p.mu.Unlock()
	for _, id := range ids {
		switch {
		case failed[id]:
			out[id] = "failed"
		case waiting[id] != "":
			out[id] = waiting[id]
		default:
			out[id] = "printed"
		}
	}
	return out
}
