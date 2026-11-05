package snoop

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"strings"
	"time"
)

// DocTitle is the Drive doc's name: date first, so the Snoop folder sorts by day.
func DocTitle(c *Call, tz *time.Location) string {
	title := c.Title
	if title == "" {
		title = "Meet " + c.MeetingCode
	}
	return c.Start.In(tz).Format("2006-01-02 15:04") + " · " + title
}

// DocHTML is the body of Snoop's Drive doc for a call. Drive converts it to
// a Google Doc, so it sticks to plain tags.
func DocHTML(c *Call, tz *time.Location, adminURL string) string {
	esc := html.EscapeString
	var b strings.Builder
	b.WriteString(`<html><head><meta charset="utf-8"></head><body>`)
	fmt.Fprintf(&b, "<h1>%s</h1>", esc(c.Title))
	fmt.Fprintf(&b, "<p>%s – %s · Meet <code>%s</code>",
		esc(c.Start.In(tz).Format("Mon 2 Jan 2006, 15:04")), esc(c.End.In(tz).Format("15:04 MST")), esc(c.MeetingCode))
	if c.Organizer != "" {
		fmt.Fprintf(&b, " · organised by %s", esc(c.Organizer))
	}
	b.WriteString("</p>")
	if len(c.Attendees) > 0 {
		var people []string
		for _, a := range c.Attendees {
			if a.Name != "" {
				people = append(people, esc(a.Name)+" &lt;"+esc(a.Email)+"&gt;")
			} else {
				people = append(people, esc(a.Email))
			}
		}
		fmt.Fprintf(&b, "<p><b>People:</b> %s</p>", strings.Join(people, ", "))
	}

	if n := c.Notes(); n != nil {
		if len(n.NextSteps) > 0 {
			b.WriteString("<h2>Next steps</h2><ul>")
			for i, s := range n.NextSteps {
				mark := "☐"
				if c.Ticked[i] {
					mark = "☑"
				}
				fmt.Fprintf(&b, "<li>%s %s</li>", mark, esc(s))
			}
			b.WriteString("</ul>")
		}
		if n.Summary != "" {
			b.WriteString("<h2>Summary</h2>")
			for _, p := range strings.Split(n.Summary, "\n\n") {
				fmt.Fprintf(&b, "<p>%s</p>", esc(p))
			}
		}
		fmt.Fprintf(&b, "<p><i>Notes by %s.</i></p>", esc(n.By))
	} else {
		b.WriteString("<p><i>No notes for this call.</i></p>")
	}

	var links []string
	if c.NotesDoc != "" {
		links = append(links, fmt.Sprintf(`<a href="https://docs.google.com/document/d/%s">Gemini's full notes</a>`, esc(c.NotesDoc)))
	}
	if c.TranscriptDoc != "" {
		links = append(links, fmt.Sprintf(`<a href="https://docs.google.com/document/d/%s">Meet's transcript</a>`, esc(c.TranscriptDoc)))
	}
	if adminURL != "" {
		links = append(links, fmt.Sprintf(`<a href="%s">Snoop</a>`, esc(adminURL)))
	}
	if len(links) > 0 {
		fmt.Fprintf(&b, "<p>%s</p>", strings.Join(links, " · "))
	}

	lines := c.Lines()
	fmt.Fprintf(&b, "<h2>Transcript</h2><p><i>%s, %d lines.</i></p>", esc(c.TranscriptSource()), len(lines))
	prev := ""
	for _, l := range lines {
		if l.Speaker != prev {
			fmt.Fprintf(&b, "<p><b>%s</b> <i>%s</i><br>%s</p>", esc(l.Speaker), l.At.In(tz).Format("15:04:05"), esc(l.Text))
		} else {
			fmt.Fprintf(&b, "<p>%s</p>", esc(l.Text))
		}
		prev = l.Speaker
	}
	b.WriteString("</body></html>")
	return b.String()
}

func hashOf(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}
