package snort

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// siteHeader is the lineup header of the four in-person sites.
var siteHeader = []string{"YouTube", "status", "name", "track", "day", "organization", "photo", "linkedin", "linkedin2", "twitter", "twitter2", "title", "abstract", "description", "bio"}

// Refusals. They never quote anything from a sheet.
var (
	errNoFeed     = fmt.Errorf("Snort can't read the sheets yet (no feed set up)")
	errNoTab      = fmt.Errorf("no tab for this event in the sheet, ask Marek")
	errTwoTabs    = fmt.Errorf("more than one tab fits this event in the sheet, ask Marek")
	errFeedDown   = fmt.Errorf("the sheet could not be read, try again in a minute")
	errColumns    = fmt.Errorf("the sheet's columns don't match the website's, ask Marek")
	errEmpty      = fmt.Errorf("the sheet's tab is empty, nothing pushed")
	errPrivate    = fmt.Errorf("the sheet still holds an email address or a phone number, nothing pushed: ask Marek")
	errNoSiteFile = fmt.Errorf("the website has no lineup file for this event yet, ask Marek")
)

// feedAnswer is what the Snort feed (an Apps Script of Marek's) sends: the
// tab's cells with the email column already cut, inside Google.
type feedAnswer struct {
	OK     bool       `json:"ok"`
	Error  string     `json:"error"` // notab, twotabs, ...
	Header []string   `json:"header"`
	Rows   [][]string `json:"rows"`
}

// readFeed asks the feed for one event's tab. Errors never carry the
// feed's address: it holds the key.
func readFeed(addr string, ev Event) (header []string, rows [][]string, err error) {
	if addr == "" {
		return nil, nil, errNoFeed
	}
	sep := "?"
	if strings.Contains(addr, "?") {
		sep = "&"
	}
	c := &http.Client{Timeout: 60 * time.Second}
	resp, err := c.Get(addr + sep + ev.feedQuery().Encode())
	if err != nil {
		return nil, nil, errFeedDown
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, nil, errFeedDown
	}
	var a feedAnswer
	if json.Unmarshal(data, &a) != nil {
		return nil, nil, errFeedDown
	}
	if !a.OK {
		switch a.Error {
		case "notab":
			return nil, nil, errNoTab
		case "twotabs":
			return nil, nil, errTwoTabs
		case "private":
			return nil, nil, errPrivate
		}
		return nil, nil, errFeedDown
	}
	return a.Header, a.Rows, nil
}

var (
	emailRe = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(\.[A-Za-z0-9\-]+)*\.[A-Za-z]{2,}`)
	// A phone number: a + and at least nine digits, maybe split by spaces,
	// dots, dashes or brackets. Years and counts in abstracts don't start with +.
	phoneRe = regexp.MustCompile(`\+\s?\(?\d[\d\s().\-]{7,}\d`)
)

// lineupFile turns the feed's cells into the site's file, or refuses.
// want is the header the site's file has (siteHeader for a new one).
func lineupFile(want, header []string, rows [][]string) ([]byte, int, error) {
	header = trimCells(header)
	// Columns past the site's header that are empty everywhere are the
	// sheet's spare columns.
	for len(header) > len(want) && header[len(header)-1] == "" && columnEmpty(rows, len(header)-1) {
		header = header[:len(header)-1]
	}
	if len(header) != len(want) {
		return nil, 0, errColumns
	}
	for i := range want {
		if header[i] != want[i] {
			return nil, 0, errColumns
		}
	}
	var keep [][]string
	for _, r := range rows {
		r = fit(r, len(want))
		if blank(r) {
			continue
		}
		keep = append(keep, r)
	}
	if len(keep) == 0 {
		return nil, 0, errEmpty
	}
	for _, r := range append([][]string{header}, keep...) {
		for _, cell := range r {
			if emailRe.MatchString(cell) || phoneRe.MatchString(cell) {
				return nil, 0, errPrivate
			}
		}
	}
	return writeCSV(header, keep), len(keep), nil
}

func trimCells(r []string) []string {
	out := make([]string, len(r))
	for i, c := range r {
		out[i] = strings.TrimSpace(c)
	}
	return out
}

func columnEmpty(rows [][]string, i int) bool {
	for _, r := range rows {
		if i < len(r) && strings.TrimSpace(r[i]) != "" {
			return false
		}
	}
	return true
}

// fit cuts a row to n cells (the cut ones must be empty, see lineupFile) or pads it.
func fit(r []string, n int) []string {
	out := make([]string, n)
	copy(out, r)
	return out
}

func blank(r []string) bool {
	for _, c := range r {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

// writeCSV writes the byte form the sites' lineups have (Google's CSV
// export): UTF-8, CRLF between rows, LF inside a cell, no newline at the end,
// quotes only where a cell needs them.
func writeCSV(header []string, rows [][]string) []byte {
	var b bytes.Buffer
	line := func(r []string) {
		for i, c := range r {
			if i > 0 {
				b.WriteByte(',')
			}
			c = strings.ReplaceAll(c, "\r\n", "\n")
			c = strings.ReplaceAll(c, "\r", "\n")
			if strings.ContainsAny(c, ",\"\n") {
				b.WriteByte('"')
				b.WriteString(strings.ReplaceAll(c, `"`, `""`))
				b.WriteByte('"')
			} else {
				b.WriteString(c)
			}
		}
	}
	line(header)
	for _, r := range rows {
		b.WriteString("\r\n")
		line(r)
	}
	return b.Bytes()
}

// parseCSV reads a site's lineup file (header, rows).
func parseCSV(data []byte) ([]string, [][]string, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	all, err := r.ReadAll()
	if err != nil {
		return nil, nil, err
	}
	if len(all) == 0 {
		return nil, nil, fmt.Errorf("empty file")
	}
	return all[0], all[1:], nil
}

// photos are the headshot files a site lineup uses (the photo column).
func photos(data []byte) []string {
	header, rows, err := parseCSV(data)
	if err != nil {
		return nil
	}
	col := -1
	for i, h := range header {
		if h == "photo" {
			col = i
		}
	}
	if col < 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range rows {
		if col < len(r) {
			p := strings.TrimSpace(r[col])
			if p != "" && safeName.MatchString(p) && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}
