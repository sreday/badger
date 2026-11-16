package snap

// The anonymized audience list. A sponsor or prospect asks who attends;
// Snap builds them two A to Z lists, job titles and companies, from the
// registrations on Luma (exportluma.go) and puts them on a private page,
// /snap/<secret>. No names, no emails, and a title is never tied to a
// company.
//
// Claude (the quick-question routine, qa.go) cleans the raw answers into
// generalized titles and proper company names (kept in export-titles.json
// and export-companies.json, so each raw answer is cleaned once).
//
// How a list is made is not in this repo: what Claude is told, what a list
// needs and what is left out is the recipe, a private file on the server
// (exportrecipe.go). Without it the lists are off.
//
// Everything lives on the server (data/snap/exports.json), so a link works
// from anywhere and outlives any one machine.

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sreday/badger/web"
)

const (
	exportLife    = 90 * 24 * time.Hour // how long a link works
	exportTimeout = 30 * time.Minute    // no answer from Claude by then = failed
	exportMarker  = "SNAP ANONYMIZED LIST"
	secretLen     = 10 // Marek 2026-11-16: ten characters is enough, no longer links
	secretChars   = "abcdefghjkmnpqrstuvwxyz23456789"
)

// ExportLink is one private page.
type ExportLink struct {
	ID        string    `json:"id"`
	Secret    string    `json:"secret"`
	Sponsor   string    `json:"sponsor"`
	Created   time.Time `json:"created"`
	Expires   time.Time `json:"expires"`
	Killed    time.Time `json:"killed"`
	Titles    []string  `json:"titles"`
	Companies []string  `json:"companies"`
	Thread    string    `json:"thread"`
	Events    []string  `json:"events"`
	Fit       int       `json:"fit"`
	Covers    string    `json:"covers"`
	Asked     string    `json:"asked"`
	LeftOut   int       `json:"left_out,omitempty"` // people left out, by the recipe
}

// Live: the page still shows the lists.
func (l ExportLink) Live(now time.Time) bool { return l.Killed.IsZero() && now.Before(l.Expires) }

// Label is the page's title.
func (l ExportLink) Label() string {
	switch len(l.Events) {
	case 0:
		return "Who attends"
	case 1:
		return "Who attends " + l.Events[0]
	case 2:
		return "Who attends " + l.Events[0] + " and " + l.Events[1]
	}
	return fmt.Sprintf("Who attends %s and %d more events", l.Events[0], len(l.Events)-1)
}

// ExportJob is a list in the making, kept on its thread.
type ExportJob struct {
	ID       string    `json:"id,omitempty"`
	Status   string    `json:"status,omitempty"` // asking, done, nofit, failed
	At       time.Time `json:"at,omitempty"`
	A        int       `json:"a"` // the option Mark clicked (-1: none)
	Asked    string    `json:"asked,omitempty"`
	Sponsor  string    `json:"sponsor,omitempty"`
	Round    int       `json:"round,omitempty"`
	Events   []string  `json:"events,omitempty"`
	EventIDs []string  `json:"event_ids,omitempty"`
	Draft    bool      `json:"draft,omitempty"` // Claude drafts the reply with the link once it's built
	Note     string    `json:"note,omitempty"`
	Link     string    `json:"link,omitempty"`
	// The marks Claude sent with the lists (named by the recipe), kept so the
	// list can be published as it is without a second ask.
	Marks map[string][]string `json:"marks,omitempty"`
}

// Working: Claude is on it.
func (j ExportJob) Working(now time.Time) bool {
	return j.Status == "asking" && now.Sub(j.At) <= exportTimeout
}

// CanExport: Luma can be read, the routine that cleans the lists can be
// started, and the recipe is on the server.
func (s *Server) CanExport() bool { return s.cfg.LumaAPIKey != "" && s.CanAsk() && s.recipe() != nil }

// --- Storage -----------------------------------------------------------------

func (s *Server) loadExports() {
	read := func(name string, v any) {
		if data, err := os.ReadFile(filepath.Join(s.dir, name)); err == nil {
			json.Unmarshal(data, v)
		}
	}
	read("exports.json", &s.exports)
	read("export-titles.json", &s.exTitles)
	read("export-companies.json", &s.exComps)
	if s.exTitles == nil {
		s.exTitles = map[string]string{}
	}
	if s.exComps == nil {
		s.exComps = map[string]string{}
	}
}

// expireExportsLocked drops the lists of pages that are gone: a dead link
// keeps its line in the menu, not its data.
func (s *Server) expireExportsLocked(now time.Time) {
	changed := false
	for i := range s.exports {
		if l := &s.exports[i]; !l.Live(now) && (len(l.Titles) > 0 || len(l.Companies) > 0) {
			l.Titles, l.Companies, changed = nil, nil, true
		}
	}
	if changed {
		writeJSON(s.dir, "exports.json", s.exports)
	}
}

func newSecret() string {
	b := make([]byte, secretLen)
	rand.Read(b)
	for i := range b {
		b[i] = secretChars[int(b[i])%len(secretChars)]
	}
	return string(b)
}

var validSecret = regexp.MustCompile(`^[A-Za-z0-9_-]{10,80}$`)

// liveExport finds a page by its secret.
func (s *Server) liveExport(secret string) (ExportLink, bool) {
	if !validSecret.MatchString(secret) {
		return ExportLink{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireExportsLocked(s.now())
	for _, l := range s.exports {
		if subtle.ConstantTimeCompare([]byte(l.Secret), []byte(secret)) == 1 {
			return l, true
		}
	}
	return ExportLink{}, false
}

func (s *Server) exportURL(l ExportLink) string {
	return strings.TrimRight(s.cfg.PublicURL, "/") + s.cfg.Base + "/" + l.Secret
}

// oldSecretLen: the first lists (15 November 2026) went out as
// scan.sreday.com/<60 characters>, with no /snap in front.
const oldSecretLen = 60

// OldLink keeps those first links working: an email that went out with one
// must never land on a 404. It answers only a 60-character secret Snap
// still knows, and sends the visitor to the page's address of today.
func (s *Server) OldLink(w http.ResponseWriter, r *http.Request) {
	secret := r.PathValue("secret")
	if len(secret) != oldSecretLen {
		http.NotFound(w, r)
		return
	}
	if _, ok := s.liveExport(secret); !ok {
		http.NotFound(w, r)
		return
	}
	to := s.cfg.Base + "/" + secret
	if r.URL.RawQuery != "" {
		to += "?" + r.URL.RawQuery
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, to, http.StatusFound)
}

// ExportRow is a link in the Exports menu.
type ExportRow struct {
	ID, Label, Date, Events, Left, URL string
	Fit                                int
	Live                               bool
}

func (s *Server) exportRows(now time.Time) []ExportRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireExportsLocked(now)
	var out []ExportRow
	for i := len(s.exports) - 1; i >= 0; i-- {
		l := s.exports[i]
		r := ExportRow{ID: l.ID, Label: l.Sponsor, Date: l.Created.In(s.tz).Format("2 Jan 2006"), Events: strings.Join(l.Events, ", "), Fit: l.Fit, Live: l.Live(now), URL: s.exportURL(l)}
		switch {
		case !l.Killed.IsZero():
			r.Left = "killed"
		case !l.Live(now):
			r.Left = "expired"
		default:
			r.Left = fmt.Sprintf("%d days left", int(l.Expires.Sub(now).Hours()/24)+1)
		}
		out = append(out, r)
	}
	return out
}

// --- The page ----------------------------------------------------------------

// handleExportPage: GET /{secret}, the sponsor's page; ?dl=titles,
// companies or both downloads the lists.
func (s *Server) handleExportPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	l, ok := s.liveExport(r.PathValue("secret"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !l.Live(s.now()) {
		w.WriteHeader(http.StatusGone)
		s.render(w, "export.html", map[string]any{"Gone": "This list is no longer available. Ask Mark for a fresh one."})
		return
	}
	switch r.URL.Query().Get("dl") {
	case "titles":
		web.SendCSV(w, "Job titles", listCSV("Job title", l.Titles))
	case "companies":
		web.SendCSV(w, "Companies", listCSV("Company", l.Companies))
	case "both": // two files, never one table: a title is not tied to a company
		var buf bytes.Buffer
		z := zip.NewWriter(&buf)
		for _, f := range []struct {
			name, head string
			rows       []string
		}{{"Job titles.csv", "Job title", l.Titles}, {"Companies.csv", "Company", l.Companies}} {
			if fw, err := z.Create(f.name); err == nil {
				fw.Write(listCSV(f.head, f.rows))
			}
		}
		z.Close()
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="Job titles and companies.zip"`)
		w.Write(buf.Bytes())
	default:
		s.render(w, "export.html", map[string]any{"Label": l.Label(), "Titles": l.Titles, "Companies": l.Companies})
	}
}

func listCSV(head string, rows []string) []byte {
	var buf bytes.Buffer
	buf.WriteString("\xef\xbb\xbf") // Excel reads it as UTF-8
	cw := csv.NewWriter(&buf)
	cw.Write([]string{head})
	for _, r := range rows {
		cw.Write([]string{web.CSVSafe(r)})
	}
	cw.Flush()
	return buf.Bytes()
}

// handleExportKill: POST /admin/export/{id}/kill ends a link now.
func (s *Server) handleExportKill(w http.ResponseWriter, r *http.Request) {
	id, now := r.PathValue("id"), s.now()
	s.mu.Lock()
	for i := range s.exports {
		if s.exports[i].ID == id && s.exports[i].Killed.IsZero() {
			s.exports[i].Killed = now
		}
	}
	s.expireExportsLocked(now)
	writeJSON(s.dir, "exports.json", s.exports)
	s.mu.Unlock()
	back(w, r)
}

// --- Building a list ---------------------------------------------------------

// handleExport: POST /admin/thread/{id}/export. a is the option Mark
// clicked; event names the event when no option does; force=1 publishes a
// list the recipe held back; draft=0 builds the page without
// drafting a reply.
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	id, ok := s.known(w, r)
	if !ok {
		return
	}
	if !s.CanExport() {
		http.Error(w, "Anonymized lists need the Luma key, the quick-question routine and the recipe on the server", http.StatusServiceUnavailable)
		return
	}
	t, _ := s.store.Get(id)
	now := s.now()
	if t.Export.Working(now) {
		back(w, r)
		return
	}
	if r.FormValue("force") == "1" && t.Export.Status == "nofit" {
		if err := s.finishExport(id, true); err != nil {
			s.failExport(id, err.Error())
		}
		back(w, r)
		return
	}
	a := -1
	if v := r.FormValue("a"); v != "" {
		fmt.Sscanf(v, "%d", &a)
	}
	asked := strings.TrimSpace(r.FormValue("event"))
	if asked == "" && a >= 0 && a < len(t.Actions) {
		asked = t.Actions[a].Event
	}
	if asked == "" {
		asked = t.Event
	}
	if asked == "" && a >= 0 && a < len(t.Actions) { // an option without its event: its words and the subject may name one
		asked = t.Actions[a].Title + " " + t.Subject
	}
	if asked == "" {
		http.Error(w, "Which event? The thread doesn't name one", http.StatusBadRequest)
		return
	}
	sponsor := strings.TrimSpace(t.Company)
	if sponsor == "" {
		if d := workDomains(t.People); len(d) > 0 { // no company on the card yet: the name Snap knows for their domain, else the domain
			sponsor = d[0]
			s.mu.Lock()
			if f, ok := s.facts[companyKey(strings.SplitN(d[0], ".", 2)[0])]; ok && f.Company != "" {
				sponsor = f.Company
			}
			s.mu.Unlock()
		} else {
			sponsor = "them"
		}
	}
	var rb [6]byte
	rand.Read(rb[:])
	job := ExportJob{ID: fmt.Sprintf("x%x", rb), Status: "asking", At: now, A: a, Asked: clip(asked, 120), Sponsor: sponsor, Round: 1, Draft: r.FormValue("draft") != "0"}
	s.store.Update(id, func(t *Thread) { t.Export = job })
	if a >= 0 && a < len(t.Actions) {
		s.recordPick("option", "", &t, a+1)
	}
	go func() {
		if err := s.askExport(id, true); err != nil {
			log.Printf("Snap: anonymized list: %v", err)
			s.failExport(id, err.Error())
		}
	}()
	back(w, r)
}

func (s *Server) failExport(id, why string) {
	s.store.Update(id, func(t *Thread) { t.Export.Status, t.Export.Note = "failed", "No list: "+clip(why, 200) })
}

// askExport picks the round's events and, with fire, starts the routine.
func (s *Server) askExport(id string, fire bool) error {
	rc := s.recipe()
	if rc == nil {
		return fmt.Errorf("the lists' recipe is not on the server")
	}
	t, _ := s.store.Get(id)
	events, err := s.exportEvents(t.Export.Asked, t.Export.Round)
	if err != nil {
		return err
	}
	var names, ids []string
	for _, e := range events {
		names, ids = append(names, e.Name), append(ids, e.ID)
	}
	if _, err := s.exportPeople(ids); err != nil {
		return err
	}
	now := s.now()
	s.store.Update(id, func(t *Thread) {
		t.Export.Events, t.Export.EventIDs, t.Export.At, t.Export.Status = names, ids, now, "asking"
	})
	if !fire {
		return nil
	}
	c := s.qq()
	text := exportMarker + " " + t.Export.ID + "\n\n" + fill(rc.Ask, "id", t.Export.ID, "sponsor", t.Export.Sponsor, "subject", t.Subject, "thread", t.ID)
	return s.fireTrigger(c.Routine, c.Token, text)
}

// exportAsk is what Claude gets for one list.
type exportAsk struct {
	Sponsor    string   `json:"sponsor"`
	Facts      string   `json:"sponsor_facts,omitempty"`
	Ask        string   `json:"their_ask,omitempty"`
	Asked      string   `json:"event_asked_about"`
	Events     []string `json:"events_covered"`
	Titles     []string `json:"job_titles"`
	Companies  []string `json:"companies"`
	TitleVocab []string `json:"title_vocabulary"`
	CompVocab  []string `json:"known_companies"`
}

// exportInstructions: the routine's instructions for a list. They are the
// recipe's, not this repo's.
func (s *Server) exportInstructions() string {
	if rc := s.recipe(); rc != nil {
		return rc.Instructions
	}
	return ""
}

// exportItems: the lists waiting for Claude, for GET /api/qa.
func (s *Server) exportItems(now time.Time, block func(Thread) map[string]any) []qaItem {
	var items []qaItem
	for _, t := range s.store.All() {
		j := t.Export
		if !j.Working(now) || len(j.EventIDs) == 0 {
			continue
		}
		people, err := s.exportPeople(j.EventIDs)
		if err != nil {
			log.Printf("Snap: anonymized list: %v", err)
			continue
		}
		ask := exportAsk{Sponsor: j.Sponsor, Asked: j.Asked, Events: j.Events, Ask: strings.TrimSpace(t.Label + ". " + t.Summary)}
		s.mu.Lock()
		if f, ok := s.facts[companyKey(j.Sponsor)]; ok {
			ask.Facts = f.Facts
		}
		rawT, rawC, vocT, vocC := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
		for _, p := range people {
			if v, ok := s.exTitles[p.title]; ok {
				vocT[v] = true
			} else if p.title != "" {
				rawT[p.title] = true
			}
			if v, ok := s.exComps[p.company]; ok {
				vocC[v] = true
			} else if p.company != "" {
				rawC[p.company] = true
			}
		}
		s.mu.Unlock()
		delete(vocT, "")
		delete(vocC, "")
		ask.Titles, ask.Companies, ask.TitleVocab, ask.CompVocab = sortedSet(rawT), sortedSet(rawC), sortedSet(vocT), sortedSet(vocC)
		items = append(items, qaItem{ID: j.ID, Mode: "export", Question: "Clean the lists and judge the fit for " + j.Sponsor + ".", Asked: j.At.In(s.tz).Format("Mon 2 Jan 2006 15:04"), Thread: block(t), Export: &ask})
	}
	return items
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i]), strings.ToLower(out[j])
		if a != b {
			return a < b
		}
		return out[i] < out[j]
	})
	return out
}

// exportAnswer is Claude's part of an answer in POST /api/qa: the cleaned
// labels. The marks that go with them are named by the recipe and read
// from the raw answer (marksOf).
type exportAnswer struct {
	Titles    map[string]string `json:"titles"`
	Companies map[string]string `json:"companies"`
}

// cleanLabel is a generalized title or a company name fit to show: never
// an address, a link or a number that could be a phone.
func cleanLabel(v string) string {
	v = strings.TrimSpace(exSpaces.ReplaceAllString(v, " "))
	if len(v) > 80 || piiLike.MatchString(v) || strings.ContainsAny(v, "<>\"") {
		return ""
	}
	return v
}

// takeExport takes one batch for the job id; it reports whether id was a
// list. raw is the same answer as it came, for the recipe's marks.
func (s *Server) takeExport(id string, a exportAnswer, raw map[string]json.RawMessage, errText string) bool {
	var tid string
	for _, t := range s.store.All() {
		if t.Export.ID == id && t.Export.Status == "asking" {
			tid = t.ID
		}
	}
	if tid == "" {
		return false
	}
	rc := s.recipe()
	if rc == nil {
		s.failExport(tid, "the lists' recipe is not on the server")
		return true
	}
	t, _ := s.store.Get(tid)
	marks, done := rc.marksOf(raw)
	if !done && len(a.Titles) == 0 && len(a.Companies) == 0 {
		if errText == "" {
			errText = "Claude came back without the lists"
		}
		s.failExport(tid, errText)
		return true
	}
	people, err := s.exportPeople(t.Export.EventIDs)
	if err != nil {
		s.failExport(tid, err.Error())
		return true
	}
	rawT, rawC := map[string]bool{}, map[string]bool{}
	for _, p := range people {
		rawT[p.title], rawC[p.company] = true, true
	}
	s.mu.Lock()
	for k, v := range a.Titles { // only answers to what was asked: nothing invented gets in
		if k = exKey(k); k != "" && rawT[k] {
			s.exTitles[k] = cleanLabel(v)
		}
	}
	for k, v := range a.Companies {
		if k = exKey(k); k != "" && rawC[k] {
			s.exComps[k] = cleanLabel(v)
		}
	}
	writeJSON(s.dir, "export-titles.json", s.exTitles)
	writeJSON(s.dir, "export-companies.json", s.exComps)
	s.mu.Unlock()
	if !done {
		return true // more batches to come
	}
	s.store.Update(tid, func(t *Thread) { t.Export.Marks = marks })
	s.noteTook("export", s.now().Sub(t.Export.At))
	if err := s.finishExport(tid, false); err != nil {
		s.failExport(tid, err.Error())
	}
	return true
}

// exportResult is what decide makes of one audience.
type exportResult struct {
	titles, companies []string
	score, leftOut    int  // percent; people left out
	people            int  // people with a usable title, after that
	ok                bool // the recipe's bar was reached
}

// decide builds the two lists by the recipe. Each person has a cleaned
// title and company. The marks are sets of labels: people of a hidden
// company are never in; the score is the share of people whose title is
// counted; when the recipe names titles that may be left out, people
// holding them go while the score is under the bar, as few as it takes.
func decide(rc *exportRecipe, people []exPerson, titles, comps map[string]string, marks map[string][]string) exportResult {
	set := func(name string) map[string]bool {
		m := map[string]bool{}
		for _, v := range marks[name] {
			m[strings.ToLower(strings.TrimSpace(v))] = true
		}
		delete(m, "")
		return m
	}
	countS, dropS, hideS := set(rc.Marks.Count), set(rc.Marks.Drop), set(rc.Marks.Hide)
	type who struct {
		title, company string
		counts, drops  bool
	}
	var in []who
	n, countN, dropN := 0, 0, 0
	for _, p := range people {
		w := who{title: titles[p.title], company: comps[p.company]}
		if w.company != "" && hideS[strings.ToLower(w.company)] {
			continue
		}
		if w.title == "" && w.company == "" {
			continue
		}
		if w.title != "" {
			n++
			k := strings.ToLower(w.title)
			w.counts = countS[k]
			w.drops = dropS[k] && !w.counts
			if w.counts {
				countN++
			}
			if w.drops {
				dropN++
			}
		}
		in = append(in, w)
	}
	var res exportResult
	cut := 0
	if rc.Bar > 0 && n > 0 && countN*100 < rc.Bar*n {
		cut = n - countN*100/rc.Bar // the fewest to leave out for the rest to reach the bar
		if cut > dropN {
			cut = dropN
		}
	}
	sort.SliceStable(in, func(i, j int) bool { return in[i].title+"\x00"+in[i].company < in[j].title+"\x00"+in[j].company })
	tS, cS := map[string]bool{}, map[string]bool{}
	for _, w := range in {
		if w.drops && res.leftOut < cut {
			res.leftOut++
			continue
		}
		tS[w.title], cS[w.company] = true, true
	}
	delete(tS, "")
	delete(cS, "")
	res.titles, res.companies = sortedSet(tS), sortedSet(cS)
	res.people = n - res.leftOut
	if res.people > 0 {
		res.score = countN * 100 / res.people
	}
	res.ok = res.people >= rc.People && res.score >= rc.Bar
	return res
}

func covers(events []string) string {
	switch len(events) {
	case 0:
		return "the people who registered"
	case 1:
		return "the people who registered for " + events[0]
	case 2:
		return "the people who registered for " + events[0] + " and " + events[1]
	}
	return fmt.Sprintf("the people who registered for %s and %d more events", events[0], len(events)-1)
}

// finishExport turns Claude's answer into a page, another round or a "no
// list" note, by the recipe. force publishes the list as it is.
func (s *Server) finishExport(id string, force bool) error {
	rc := s.recipe()
	if rc == nil {
		return fmt.Errorf("the lists' recipe is not on the server")
	}
	t, _ := s.store.Get(id)
	j := t.Export
	if j.Marks == nil {
		return fmt.Errorf("this list was asked for before the last update: ask for it again")
	}
	people, err := s.exportPeople(j.EventIDs)
	if err != nil {
		return err
	}
	s.mu.Lock()
	res := decide(rc, people, s.exTitles, s.exComps, j.Marks)
	s.mu.Unlock()
	vals := []any{"sponsor", j.Sponsor, "events", strings.Join(j.Events, ", "), "covers", covers(j.Events), "score", res.score, "bar", rc.Bar,
		"people", res.people, "titles", len(res.titles), "companies", len(res.companies)}
	if !res.ok && !force {
		if j.Round < rc.Rounds {
			s.store.Update(id, func(t *Thread) { t.Export.Round++ })
			if err := s.askExport(id, false); err != nil { // the routine looks again after its last POST
				return err
			}
			if now, _ := s.store.Get(id); len(now.Export.EventIDs) > len(j.EventIDs) {
				return nil
			}
		}
		note := fill(rc.Notes.Short, vals...)
		if res.people < rc.People {
			note = fill(rc.Notes.Few, vals...)
		}
		if note == "" {
			note = "No list yet for " + j.Sponsor + "."
		}
		s.store.Update(id, func(t *Thread) { t.Export.Status, t.Export.Note = "nofit", note })
		return nil
	}
	if len(res.titles) == 0 {
		return fmt.Errorf("nothing left to show")
	}
	now := s.now()
	l := ExportLink{ID: j.ID, Secret: newSecret(), Sponsor: j.Sponsor, Created: now, Expires: now.Add(exportLife), Titles: res.titles, Companies: res.companies,
		Thread: id, Events: j.Events, Fit: res.score, Covers: covers(j.Events), Asked: j.Asked, LeftOut: res.leftOut}
	s.mu.Lock()
	s.exports = append(s.exports, l)
	writeJSON(s.dir, "exports.json", s.exports)
	s.mu.Unlock()
	note := fill(rc.Notes.Ready, vals...)
	if note == "" {
		note = "List ready for " + j.Sponsor + "."
	}
	s.store.Update(id, func(t *Thread) { t.Export.Status, t.Export.Note, t.Export.Link, t.Export.At = "done", note, l.ID, now })
	if j.Draft && s.CanDraft() {
		if err := s.startDraft(id, j.A, ""); err != nil {
			log.Printf("Snap: draft with the anonymized list: %v", err)
		}
	}
	return nil
}

// threadExport: the live page built for a thread, if any.
func (s *Server) threadExport(t Thread) (ExportLink, bool) {
	if t.Export.Status != "done" || t.Export.Link == "" {
		return ExportLink{}, false
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.exports {
		if l.ID == t.Export.Link && l.Live(now) {
			return l, true
		}
	}
	return ExportLink{}, false
}

// exportLine tells the drafter about the page built for this reply, in the
// recipe's words.
func (s *Server) exportLine(t Thread) string {
	l, ok := s.threadExport(t)
	if !ok || s.now().Sub(t.Export.At) > 24*time.Hour {
		return ""
	}
	text := "Snap built {sponsor} a private page for this reply: {link}\nGive them that link on a line of its own.\n"
	if rc := s.recipe(); rc != nil && rc.Drafter != "" {
		text = rc.Drafter
	}
	return fill(text, "sponsor", l.Sponsor, "link", s.exportURL(l))
}
